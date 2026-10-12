package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/voice/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/storage"
	"github.com/GAMch1k/life-os/services/voice/internal/testdb"
)

// voiceAPI is the Voice API on a private schema, storing audio in its own
// directory.
type voiceAPI struct {
	app    *httpserver.App
	pool   *pgxpool.Pool
	schema string
	dir    string
}

func newVoiceAPI(t *testing.T) *voiceAPI {
	t.Helper()
	pool, schema := testdb.Open(t, "")
	dir := t.TempDir()
	app := httpserver.New("voice", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, NewHandler(store.New(pool), storage.Files{Root: dir}, time.Hour, 1<<20, nil, ""))
	return &voiceAPI{app: app, pool: pool, schema: schema, dir: dir}
}

type uploadRequest struct {
	key, source, metadata string
	audio                 []byte
}

type uploadResponse struct {
	status int
	code   string
	job    generated.AudioJob
}

// upload posts a recording as an app does, with the key in Idempotency-Key
// when it is set.
func (v *voiceAPI) upload(t *testing.T, r uploadRequest) uploadResponse {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", "capture.wav")
	if err == nil {
		_, err = part.Write(r.audio)
	}
	if err == nil && r.source != "" {
		err = writer.WriteField("source", r.source)
	}
	if err == nil && r.metadata != "" {
		err = writer.WriteField("sourceMetadata", r.metadata)
	}
	if err == nil {
		err = writer.Close()
	}
	if err != nil {
		t.Error(err)
		return uploadResponse{}
	}
	req, err := http.NewRequest(http.MethodPost, "/v1/audio-jobs", &body)
	if err != nil {
		t.Error(err)
		return uploadResponse{}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(activity.ActorHeader, "dev:test")
	if r.key != "" {
		req.Header.Set("Idempotency-Key", r.key)
	}
	resp, err := v.app.Test(req, fiber.TestConfig{Timeout: 20 * time.Second})
	if err != nil {
		// t.Error, not t.Fatal: concurrent uploads run in goroutines.
		t.Error(err)
		return uploadResponse{}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := uploadResponse{status: resp.StatusCode}
	if resp.StatusCode == http.StatusAccepted {
		if err := json.Unmarshal(raw, &out.job); err != nil {
			t.Errorf("job %s: %v", raw, err)
		}
		return out
	}
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	out.code = envelope.Error.Code
	return out
}

// stored counts the audio files in the storage directory and the jobs.
func (v *voiceAPI) stored(t *testing.T) (files, jobs int) {
	t.Helper()
	entries, err := os.ReadDir(v.dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := v.pool.QueryRow(context.Background(), `SELECT count(*) FROM audio_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	return len(entries), jobs
}

func recording(fill byte) []byte {
	return append(wav(0), bytes.Repeat([]byte{fill}, 64)...)
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// A retried upload (same key, audio, source, and metadata) returns the job
// the first created, in its current state, and stores no second copy.
func TestRetriedUploadReturnsTheSameJob(t *testing.T) {
	v := newVoiceAPI(t)
	first := v.upload(t, uploadRequest{key: "capture-7", source: "watch", audio: recording(1), metadata: `{"captureId":"capture-7","timeZone":"Europe/Berlin"}`})
	if first.status != http.StatusAccepted || first.job.Status != generated.Queued {
		t.Fatalf("first upload = %+v, want 202 queued", first)
	}
	// The metadata's key order and spacing do not make it another upload.
	retry := uploadRequest{key: "capture-7", source: "watch", audio: recording(1), metadata: `{ "timeZone": "Europe/Berlin", "captureId": "capture-7" }`}
	again := v.upload(t, retry)
	if again.status != http.StatusAccepted || again.job.Id != first.job.Id || again.job.CreatedAt != first.job.CreatedAt {
		t.Fatalf("retried upload = %+v, want 202 with job %v", again, first.job.Id)
	}
	if files, jobs := v.stored(t); files != 1 || jobs != 1 {
		t.Fatalf("after the retry: %d files, %d jobs; want 1 and 1", files, jobs)
	}
	// A retry answers with the job's current state.
	if _, err := v.pool.Exec(context.Background(), `UPDATE audio_jobs SET status = 'processing', attempt_count = 1 WHERE id = $1`, first.job.Id); err != nil {
		t.Fatal(err)
	}
	if later := v.upload(t, retry); later.status != http.StatusAccepted || later.job.Id != first.job.Id || later.job.Status != generated.Processing || later.job.AttemptCount != 1 {
		t.Fatalf("later retry = %+v, want the processing job", later)
	}
}

// The same key with another upload is refused and stores nothing.
func TestReusedIdempotencyKeyWithAnotherUploadConflicts(t *testing.T) {
	v := newVoiceAPI(t)
	original := uploadRequest{key: "capture-8", source: "watch", audio: recording(1), metadata: `{"captureId":"capture-8"}`}
	first := v.upload(t, original)
	if first.status != http.StatusAccepted {
		t.Fatalf("first upload = %+v", first)
	}
	for name, r := range map[string]uploadRequest{
		"other audio":    {key: "capture-8", source: "watch", audio: recording(2), metadata: `{"captureId":"capture-8"}`},
		"other metadata": {key: "capture-8", source: "watch", audio: recording(1), metadata: `{"captureId":"capture-9"}`},
		"other source":   {key: "capture-8", source: "web", audio: recording(1), metadata: `{"captureId":"capture-8"}`},
	} {
		if got := v.upload(t, r); got.status != http.StatusConflict || got.code != "idempotency_conflict" {
			t.Errorf("%s: %d %q, want 409 idempotency_conflict", name, got.status, got.code)
		}
	}
	if files, jobs := v.stored(t); files != 1 || jobs != 1 {
		t.Fatalf("after the conflicts: %d files, %d jobs; want 1 and 1", files, jobs)
	}
	if again := v.upload(t, original); again.status != http.StatusAccepted || again.job.Id != first.job.Id {
		t.Fatalf("the original retried after the conflicts = %+v", again)
	}
}

// Without a key every upload is a new job, as before; another key is
// another upload.
func TestUploadsWithoutAKeyCreateJobs(t *testing.T) {
	v := newVoiceAPI(t)
	ids := map[string]bool{}
	for _, r := range []uploadRequest{
		{audio: recording(1)},
		{audio: recording(1)},
		{key: "capture-a", audio: recording(1)},
		{key: "capture-b", audio: recording(1)},
	} {
		got := v.upload(t, r)
		if got.status != http.StatusAccepted {
			t.Fatalf("upload %+v = %+v", r, got)
		}
		ids[got.job.Id.String()] = true
	}
	if files, jobs := v.stored(t); len(ids) != 4 || files != 4 || jobs != 4 {
		t.Fatalf("%d ids, %d files, %d jobs; want 4 of each", len(ids), files, jobs)
	}
}

// Uploads with one key arriving together create one job and keep one copy
// of the audio.
func TestConcurrentUploadsWithOneKeyCreateOneJob(t *testing.T) {
	v := newVoiceAPI(t)
	const uploads = 8
	start := make(chan struct{})
	results := make([]uploadResponse, uploads)
	var wg sync.WaitGroup
	for index := range uploads {
		wg.Go(func() {
			<-start
			results[index] = v.upload(t, uploadRequest{key: "capture-concurrent", source: "watch", audio: recording(3), metadata: `{"captureId":"capture-concurrent"}`})
		})
	}
	close(start)
	wg.Wait()
	for index, got := range results {
		if got.status != http.StatusAccepted || got.job.Id != results[0].job.Id {
			t.Errorf("upload %d = %+v, want 202 with job %v", index, got, results[0].job.Id)
		}
	}
	if files, jobs := v.stored(t); files != 1 || jobs != 1 {
		t.Fatalf("%d files, %d jobs; want 1 and 1", files, jobs)
	}
}

// An upload that finds its key taken only when inserting (another upload
// with the key committed after this one looked) removes the audio it stored
// and answers as a replay or a conflict.
func TestUploadLosingTheRaceRemovesItsAudio(t *testing.T) {
	for name, tc := range map[string]struct {
		winnerSource string
		wantStatus   int
		wantCode     string
	}{
		"the same upload": {winnerSource: "watch", wantStatus: http.StatusAccepted},
		"another upload":  {winnerSource: "web", wantStatus: http.StatusConflict, wantCode: "idempotency_conflict"},
	} {
		t.Run(name, func(t *testing.T) {
			v := newVoiceAPI(t)
			ctx := context.Background()
			audio := recording(4)
			digest := sha256Hex(audio)
			// The winner's transaction holds the key, uncommitted, so the
			// upload does not see it before storing its audio and then
			// waits for it on insert.
			tx, err := v.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			var winner string
			if err := tx.QueryRow(ctx, `INSERT INTO audio_jobs (source, source_metadata, object_key, content_type, byte_size, idempotency_key, request_hash)
				VALUES ($1, '{}', 'winner.wav', 'audio/wave', 10, 'capture-race', $2) RETURNING id::text`, tc.winnerSource, uploadHash(digest, tc.winnerSource, []byte(`{}`))).Scan(&winner); err != nil {
				t.Fatal(err)
			}
			done := make(chan uploadResponse, 1)
			go func() { done <- v.upload(t, uploadRequest{key: "capture-race", source: "watch", audio: audio}) }()
			waitForBlockedInsert(t, v)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var got uploadResponse
			select {
			case got = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("the upload did not finish")
			}
			if got.status != tc.wantStatus || got.code != tc.wantCode || (tc.wantStatus == http.StatusAccepted && got.job.Id.String() != winner) {
				t.Fatalf("upload = %+v, want %d %q (job %s)", got, tc.wantStatus, tc.wantCode, winner)
			}
			// The winner's audio is not in this directory: any file here
			// is the loser's copy.
			if files, jobs := v.stored(t); files != 0 || jobs != 1 {
				t.Fatalf("%d files, %d jobs; want 0 and 1", files, jobs)
			}
		})
	}
}

// waitForBlockedInsert waits, at most ten seconds, until one of the test's
// sessions waits for a lock while creating a job.
func waitForBlockedInsert(t *testing.T, v *voiceAPI) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := v.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE application_name = $1 AND wait_event_type = 'Lock' AND query LIKE '%CreateAudioJob%'`, v.schema).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the upload never waited for the winner's insert")
}
