package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/testdb"
)

func TestAudioBacklogReportsWaitingWorkRetriesAndStalls(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.Open(t, "")
	queries := store.New(pool)

	empty, err := queries.GetAudioBacklog(ctx)
	if err != nil || empty.QueuedCount != 0 || empty.OldestWaitingAt.Valid || empty.NextRetryAt.Valid {
		t.Fatalf("empty backlog = %+v, err = %v", empty, err)
	}

	create := func(key string) store.AudioJob {
		job, err := queries.CreateAudioJob(ctx, store.CreateAudioJobParams{Source: "web", SourceMetadata: []byte(`{}`), ObjectKey: key, ContentType: "audio/wav", ByteSize: 10, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	overdue := create("overdue.wav")
	retrying := create("retrying.wav")
	failed := create("failed.wav")
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Due for five minutes while nothing is processing: the worker is not
	// picking up work.
	exec("UPDATE audio_jobs SET available_at = now() - interval '5 minutes', created_at = now() - interval '5 minutes' WHERE id = $1", overdue.ID)
	exec("UPDATE audio_jobs SET status = 'retryable', available_at = now() + interval '1 minute', error_summary = 'whisper returned 503' WHERE id = $1", retrying.ID)
	exec("UPDATE audio_jobs SET status = 'failed', error_summary = 'invalid whisper response', updated_at = now() - interval '1 hour' WHERE id = $1", failed.ID)

	backlog, err := queries.GetAudioBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.QueuedCount != 1 || backlog.RetryableCount != 1 || backlog.ProcessingCount != 0 || backlog.OverdueCount != 1 || backlog.ExpiredLeaseCount != 0 {
		t.Fatalf("backlog = %+v", backlog)
	}
	if !backlog.NextRetryAt.Valid || time.Until(backlog.NextRetryAt.Time) <= 0 {
		t.Fatalf("next retry = %+v", backlog.NextRetryAt)
	}
	if !backlog.OldestWaitingAt.Valid || time.Since(backlog.OldestWaitingAt.Time) < 4*time.Minute {
		t.Fatalf("oldest waiting = %+v", backlog.OldestWaitingAt)
	}
	latest, err := queries.GetLatestAudioFailure(ctx)
	if err != nil || latest.ErrorSummary == nil || *latest.ErrorSummary != "whisper returned 503" {
		t.Fatalf("latest failure = %+v, err = %v", latest, err)
	}

	// A claimed job whose lease expired counts as a stall even though work is
	// "processing".
	owner := "worker-gone"
	claimed, err := queries.ClaimAudioJob(ctx, store.ClaimAudioJobParams{LeaseOwner: &owner, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE audio_jobs SET lease_expires_at = now() - interval '1 second' WHERE id = $1", claimed.ID)
	stalled, err := queries.GetAudioBacklog(ctx)
	if err != nil || stalled.ProcessingCount != 1 || stalled.ExpiredLeaseCount != 1 {
		t.Fatalf("stalled backlog = %+v, err = %v", stalled, err)
	}
}
