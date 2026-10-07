package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/database"
	serviceconfig "github.com/GAMch1k/life-os/services/voice/internal/config"
	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/storage"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return err
	}
	defer database.ClosePool(pool)
	queries := store.New(pool)
	files := storage.Files{Root: cfg.StorageDir}
	client := &http.Client{Timeout: 31 * time.Minute}
	tick := time.NewTicker(time.Duration(cfg.WorkerPollMillis) * time.Millisecond)
	defer tick.Stop()
	for {
		if err := process(ctx, queries, files, client, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "voice worker:", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func process(ctx context.Context, q *store.Queries, files storage.Files, client *http.Client, cfg serviceconfig.Config) error {
	for _, job := range expired(ctx, q) {
		_ = files.Remove(job.ObjectKey)
	}
	job, err := q.ClaimAudioJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	transcript, err := transcribe(ctx, client, cfg.WhisperURL, files.Path(job.ObjectKey), job.ContentType)
	if err != nil {
		return fail(ctx, q, job, cfg, err)
	}
	inboxID, err := capture(ctx, client, cfg.InboxURL, job, transcript)
	if err != nil {
		return fail(ctx, q, job, cfg, err)
	}
	_, err = q.CompleteAudioJob(ctx, store.CompleteAudioJobParams{ID: job.ID, Transcript: &transcript, InboxItemID: inboxID})
	return err
}
func expired(ctx context.Context, q *store.Queries) []store.AudioJob {
	jobs, err := q.ListExpiredAudioJobs(ctx, 100)
	if err != nil {
		return nil
	}
	for _, job := range jobs {
		_, _ = q.ExpireAudioJob(ctx, job.ID)
	}
	return jobs
}
func fail(ctx context.Context, q *store.Queries, job store.AudioJob, cfg serviceconfig.Config, cause error) error {
	status := "retryable"
	if int(job.AttemptCount) >= cfg.WorkerMaxAttempts {
		status = "failed"
	}
	message := cause.Error()
	_, err := q.RetryAudioJob(ctx, store.RetryAudioJobParams{ID: job.ID, Status: status, ErrorSummary: &message, AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(time.Duration(job.AttemptCount) * time.Minute), Valid: true}})
	return err
}
func transcribe(ctx context.Context, c *http.Client, base, path, contentType string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/transcribe", file)
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", contentType)
	if info, err := file.Stat(); err == nil {
		req.ContentLength = info.Size()
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("whisper returned %s: %s", resp.Status, string(raw))
	}
	var out struct {
		Transcript string `json:"transcript"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Transcript == "" {
		return "", fmt.Errorf("invalid whisper response")
	}
	return out.Transcript, nil
}
func capture(ctx context.Context, c *http.Client, base string, job store.AudioJob, text string) (pgtype.UUID, error) {
	metadata := map[string]any{"audioJobId": uuidString(job.ID), "contentType": job.ContentType}
	body, _ := json.Marshal(map[string]any{"content": text, "source": "voice", "sourceMetadata": metadata, "mediaReferences": []string{}, "dedupeKey": "voice:" + uuidString(job.ID)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/inbox-items", bytes.NewReader(body))
	if err != nil {
		return pgtype.UUID{}, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return pgtype.UUID{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return pgtype.UUID{}, fmt.Errorf("inbox returned %s: %s", resp.Status, string(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return pgtype.UUID{}, err
	}
	var id pgtype.UUID
	if err := id.Scan(out.ID); err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}
func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", id.Bytes[0:4], id.Bytes[4:6], id.Bytes[6:8], id.Bytes[8:10], id.Bytes[10:16])
}
