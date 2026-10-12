package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/voice/internal/api"
	"github.com/GAMch1k/life-os/services/voice/internal/api/generated"
	serviceconfig "github.com/GAMch1k/life-os/services/voice/internal/config"
	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/storage"
	"github.com/GAMch1k/life-os/services/voice/internal/testdb"
)

// An app that retries an upload whose reply was lost, before and after the
// worker has transcribed it, gets the one job back each time, and the
// recording becomes one Inbox item.
func TestRetriedUploadBecomesOneInboxItem(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.Open(t, "")
	queries := store.New(pool)
	files := storage.Files{Root: t.TempDir()}
	app := httpserver.New("voice", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(app, api.NewHandler(queries, files, time.Hour, 1<<20, nil, ""))

	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"transcript":"I already bought the chicken."}`))
	}))
	defer whisper.Close()
	var mu sync.Mutex
	var captures []map[string]any
	inbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		captures = append(captures, body)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"7a1c2d3e-4f50-4617-8a9b-0c1d2e3f4a5b"}`))
	}))
	defer inbox.Close()
	cfg := serviceconfig.Config{WhisperURL: whisper.URL, InboxURL: inbox.URL, WorkerLeaseSeconds: 60, WorkerMaxAttempts: 3, WorkerRetryBackoffSeconds: 60}

	audio := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00"), make([]byte, 64)...)
	upload := func() generated.AudioJob {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("audio", "capture.wav")
		if err == nil {
			_, err = part.Write(audio)
		}
		if err == nil {
			err = writer.WriteField("source", "watch")
		}
		if err == nil {
			err = writer.WriteField("sourceMetadata", `{"captureId":"wear-capture-1","timeZone":"Europe/Berlin"}`)
		}
		if err == nil {
			err = writer.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, "/v1/audio-jobs", &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set(activity.ActorHeader, "user")
		req.Header.Set("Idempotency-Key", "wear-capture-1")
		resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var job generated.AudioJob
		if err := json.NewDecoder(resp.Body).Decode(&job); err != nil || resp.StatusCode != http.StatusAccepted {
			t.Fatalf("upload: %d, %v", resp.StatusCode, err)
		}
		return job
	}
	work := func() {
		t.Helper()
		if err := process(ctx, queries, files, whisper.Client(), cfg, "worker-1", nil); err != nil {
			t.Fatalf("process: %v", err)
		}
	}

	first := upload()
	if again := upload(); again.Id != first.Id || again.Status != generated.Queued {
		t.Fatalf("retry before transcription = %+v, want queued job %v", again, first.Id)
	}
	work()
	work() // nothing left to claim
	done := upload()
	if done.Id != first.Id || done.Status != generated.Succeeded || done.InboxItemId == nil || done.Transcript == nil || *done.Transcript != "I already bought the chicken." {
		t.Fatalf("retry after transcription = %+v, want succeeded job %v with its Inbox item", done, first.Id)
	}
	work()

	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audio_jobs`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs = %d, %v; want 1", jobs, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(captures) != 1 {
		t.Fatalf("Inbox captures = %d (%v), want 1", len(captures), captures)
	}
	metadata, _ := captures[0]["sourceMetadata"].(map[string]any)
	if captures[0]["dedupeKey"] != "voice:"+first.Id.String() || metadata["captureId"] != "wear-capture-1" {
		t.Fatalf("Inbox capture = %v", captures[0])
	}
}
