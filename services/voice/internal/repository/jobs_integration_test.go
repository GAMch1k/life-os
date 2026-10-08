package repository_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
)

func TestLeaseReclaimAndMaxAttemptRecovery(t *testing.T) {
	databaseURL := os.Getenv("VOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("voice_test_%d", time.Now().UnixNano())
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	for _, name := range []string{"000001_template_baseline.up.sql", "000002_job_leases.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}

	queries := store.New(pool)
	job, err := queries.CreateAudioJob(ctx, store.CreateAudioJobParams{Source: "web", SourceMetadata: []byte(`{}`), ObjectKey: "lease-recovery.wav", ContentType: "audio/wav", ByteSize: 10, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	owner := "worker-before-crash"
	claimed, err := queries.ClaimAudioJob(ctx, store.ClaimAudioJobParams{LeaseOwner: &owner, LeaseSeconds: 60})
	if err != nil || claimed.Status != "processing" || claimed.AttemptCount != 1 || !claimed.LeaseExpiresAt.Valid {
		t.Fatalf("claim = %#v, err = %v", claimed, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE audio_jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	reason := "worker lease expired before completion"
	reclaimed, err := queries.ReclaimExpiredAudioJobs(ctx, store.ReclaimExpiredAudioJobsParams{AttemptCount: 3, ErrorSummary: &reason, AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}})
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Status != "retryable" || reclaimed[0].LeaseOwner != nil {
		t.Fatalf("reclaim = %#v, err = %v", reclaimed, err)
	}

	for attempt := int32(2); attempt <= 3; attempt++ {
		owner := fmt.Sprintf("restarted-worker-%d", attempt)
		claimed, err = queries.ClaimAudioJob(ctx, store.ClaimAudioJobParams{LeaseOwner: &owner, LeaseSeconds: 60})
		if err != nil || claimed.AttemptCount != attempt {
			t.Fatalf("claim attempt %d = %#v, err = %v", attempt, claimed, err)
		}
		if _, err := pool.Exec(ctx, "UPDATE audio_jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1", job.ID); err != nil {
			t.Fatal(err)
		}
		reclaimed, err = queries.ReclaimExpiredAudioJobs(ctx, store.ReclaimExpiredAudioJobsParams{AttemptCount: 3, ErrorSummary: &reason, AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}})
		if err != nil || len(reclaimed) != 1 {
			t.Fatalf("reclaim attempt %d = %#v, err = %v", attempt, reclaimed, err)
		}
		if attempt < 3 && reclaimed[0].Status != "retryable" {
			t.Fatalf("attempt %d status = %s, want retryable", attempt, reclaimed[0].Status)
		}
		if attempt == 3 && reclaimed[0].Status != "failed" {
			t.Fatalf("max attempt status = %s, want failed", reclaimed[0].Status)
		}
	}
	if _, err := queries.ClaimAudioJob(ctx, store.ClaimAudioJobParams{LeaseOwner: &owner, LeaseSeconds: 60}); err != pgx.ErrNoRows {
		t.Fatalf("failed job was claimable: %v", err)
	}

	active, err := queries.CreateAudioJob(ctx, store.CreateAudioJobParams{Source: "web", SourceMetadata: []byte(`{}`), ObjectKey: "active-lease.wav", ContentType: "audio/wav", ByteSize: 10, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	owner = "active-worker"
	claimed, err = queries.ClaimAudioJob(ctx, store.ClaimAudioJobParams{LeaseOwner: &owner, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RenewAudioJobLease(ctx, store.RenewAudioJobLeaseParams{ID: claimed.ID, LeaseOwner: &owner, LeaseSeconds: 60}); err != nil {
		t.Fatalf("renew active lease: %v", err)
	}
	expired, err := queries.ListExpiredAudioJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range expired {
		if item.ID == active.ID {
			t.Fatal("cleanup selected an actively leased job")
		}
	}
}
