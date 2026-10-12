package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/testdb"
)

// An upload's Idempotency-Key names at most one job: creating a job with a
// key already on one inserts nothing and returns that job, while jobs
// without a key are all created.
func TestCreateAudioJobWithAnIdempotencyKeyReturnsTheExistingJob(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.Open(t, "")
	queries := store.New(pool)
	create := func(objectKey string, key, hash *string) store.AudioJob {
		t.Helper()
		job, err := queries.CreateAudioJob(ctx, store.CreateAudioJobParams{Source: "watch", SourceMetadata: []byte(`{}`), ObjectKey: objectKey, ContentType: "audio/wave", ByteSize: 10, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, IdempotencyKey: key, RequestHash: hash})
		if err != nil {
			t.Fatalf("create %s: %v", objectKey, err)
		}
		return job
	}
	key, hash, otherHash := "capture-1", "hash-1", "hash-2"
	first := create("first.wav", &key, &hash)
	again := create("again.wav", &key, &otherHash)
	if again.ID != first.ID || again.ObjectKey != "first.wav" || again.RequestHash == nil || *again.RequestHash != hash {
		t.Fatalf("second create with the key = %+v, want the first job %+v", again, first)
	}
	found, err := queries.GetAudioJobByIdempotencyKey(ctx, key)
	if err != nil || found.ID != first.ID {
		t.Fatalf("job by key = %+v, %v; want %v", found, err, first.ID)
	}
	if a, b := create("plain-a.wav", nil, nil), create("plain-b.wav", nil, nil); a.ID == b.ID {
		t.Fatal("two jobs without a key were one")
	}
	if count := countJobs(t, pool); count != 3 {
		t.Fatalf("jobs = %d, want 3", count)
	}
}

// Migration 000003 keeps the jobs it finds (without a key), refuses a
// second job with one key, a key without its request hash and the other way
// round, and a key over 200 characters, and goes down and up keeping the
// jobs.
func TestUploadIdempotencyMigration(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.Open(t, "000002")
	// Rows are written and read with SQL: the generated queries know the
	// new columns.
	if _, err := pool.Exec(ctx, `INSERT INTO audio_jobs (source, object_key, content_type, byte_size) VALUES ('web', 'before.wav', 'audio/wave', 10)`); err != nil {
		t.Fatal(err)
	}
	up, down := testdb.Migration(t, "000003", "up"), testdb.Migration(t, "000003", "down")
	testdb.Apply(t, pool, up)
	var key, hash *string
	if err := pool.QueryRow(ctx, `SELECT idempotency_key, request_hash FROM audio_jobs WHERE object_key = 'before.wav'`).Scan(&key, &hash); err != nil || key != nil || hash != nil {
		t.Fatalf("existing job: key %v, hash %v, err %v; want neither", key, hash, err)
	}
	refusals := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, idempotency_key, request_hash) VALUES ('watch', 'keyed.wav', 'audio/wave', 10, 'capture-1', 'hash-1')`); err != nil {
			t.Fatalf("keyed job: %v", err)
		}
		for name, tc := range map[string]struct{ constraint, statement string }{
			"the same key again": {"audio_jobs_idempotency_key_idx", `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, idempotency_key, request_hash) VALUES ('watch', 'again.wav', 'audio/wave', 10, 'capture-1', 'hash-1')`},
			"a key without hash": {"audio_jobs_idempotency_key_check", `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, idempotency_key) VALUES ('watch', 'nohash.wav', 'audio/wave', 10, 'capture-2')`},
			"a hash without key": {"audio_jobs_idempotency_key_check", `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, request_hash) VALUES ('watch', 'nokey.wav', 'audio/wave', 10, 'hash-3')`},
			"an empty key":       {"audio_jobs_idempotency_key_check", `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, idempotency_key, request_hash) VALUES ('watch', 'empty.wav', 'audio/wave', 10, '', 'hash-4')`},
			"a long key":         {"audio_jobs_idempotency_key_check", `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, idempotency_key, request_hash) VALUES ('watch', 'long.wav', 'audio/wave', 10, '` + strings.Repeat("k", 201) + `', 'hash-5')`},
		} {
			_, err := pool.Exec(ctx, tc.statement)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.ConstraintName != tc.constraint {
				t.Errorf("%s: err = %v, want a %s violation", name, err, tc.constraint)
			}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO audio_jobs (source, object_key, content_type, byte_size, idempotency_key, request_hash) VALUES ('watch', 'longest.wav', 'audio/wave', 10, '`+strings.Repeat("k", 200)+`', 'hash-6')`); err != nil {
			t.Errorf("a 200-character key: %v", err)
		}
	}
	refusals()
	if count := countJobs(t, pool); count != 3 {
		t.Fatalf("jobs after up = %d, want 3", count)
	}

	testdb.Apply(t, pool, down)
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'audio_jobs' AND column_name IN ('idempotency_key', 'request_hash')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("columns after down = %d, %v; want none", columns, err)
	}
	if count := countJobs(t, pool); count != 3 {
		t.Fatalf("jobs after down = %d, want 3", count)
	}

	testdb.Apply(t, pool, up)
	if _, err := pool.Exec(ctx, `DELETE FROM audio_jobs WHERE object_key IN ('keyed.wav', 'longest.wav')`); err != nil {
		t.Fatal(err)
	}
	refusals()

	// The whole chain goes down and up again.
	downs := testdb.Migrations(t, "down")
	for index := len(downs) - 1; index >= 0; index-- {
		testdb.Apply(t, pool, downs[index])
	}
	for _, path := range testdb.Migrations(t, "up") {
		testdb.Apply(t, pool, path)
	}
	if count := countJobs(t, pool); count != 0 {
		t.Fatalf("jobs after the chain down and up = %d, want 0", count)
	}
}

func countJobs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audio_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
