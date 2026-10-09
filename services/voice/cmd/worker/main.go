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

	"github.com/GAMch1k/life-os/pkg/activity"
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
	workerID, err := os.Hostname()
	if err != nil {
		return err
	}
	workerID = fmt.Sprintf("%s-%d", workerID, os.Getpid())
	emitter := activity.NewEmitter(cfg.ActivityURL, "voice", nil)
	tick := time.NewTicker(time.Duration(cfg.WorkerPollMillis) * time.Millisecond)
	defer tick.Stop()
	for {
		if err := process(ctx, queries, files, client, cfg, workerID, emitter); err != nil {
			fmt.Fprintln(os.Stderr, "voice worker:", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func process(ctx context.Context, q *store.Queries, files storage.Files, client *http.Client, cfg serviceconfig.Config, workerID string, emitter *activity.Emitter) error {
	if err := reclaim(ctx, q, cfg); err != nil {
		return err
	}
	for _, job := range expired(ctx, q) {
		_ = files.Remove(job.ObjectKey)
	}
	job, err := q.ClaimAudioJob(ctx, store.ClaimAudioJobParams{LeaseOwner: &workerID, LeaseSeconds: int32(cfg.WorkerLeaseSeconds)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	workCtx, stopLease := maintainLease(ctx, q, job.ID, cfg, workerID)
	defer stopLease()
	transcript, err := transcribe(workCtx, client, cfg.WhisperURL, files.Path(job.ObjectKey), job.ContentType)
	if err != nil {
		return fail(ctx, q, job, cfg, workerID, err, emitter)
	}
	transcribedAt := time.Now().UTC()
	inboxID, err := capture(workCtx, client, cfg.InboxURL, job, transcript)
	if err != nil {
		return fail(ctx, q, job, cfg, workerID, err, emitter)
	}
	if _, err = q.CompleteAudioJob(ctx, store.CompleteAudioJobParams{ID: job.ID, Transcript: &transcript, InboxItemID: inboxID, LeaseOwner: &workerID}); err != nil {
		return err
	}
	jobID := uuidString(job.ID)
	accepted := activity.EventID("voice.job.accepted", jobID)
	emitter.Emit(ctx, activity.Event{
		ID:            activity.EventID("voice.job.transcribed", jobID),
		Type:          "voice.job.transcribed",
		Subject:       activity.Subject{Type: "voice_job", ID: jobID},
		CorrelationID: jobID,
		CausationID:   &accepted,
		Actor:         activity.System("voice-worker"),
		Outcome:       activity.Succeeded,
		Summary:       fmt.Sprintf("Transcribed “%s”", activity.Preview(transcript, 80)),
		Data:          map[string]any{"transcript": activity.Preview(transcript, 280), "inboxItemId": uuidString(inboxID), "attempt": job.AttemptCount},
		OccurredAt:    transcribedAt,
	})
	return nil
}
func maintainLease(ctx context.Context, q *store.Queries, jobID pgtype.UUID, cfg serviceconfig.Config, workerID string) (context.Context, func()) {
	workCtx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	interval := time.Duration(cfg.WorkerLeaseSeconds) * time.Second / 3
	if interval < time.Second {
		interval = time.Second
	}
	go func() {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-workCtx.Done():
				return
			case <-tick.C:
				if _, err := q.RenewAudioJobLease(ctx, store.RenewAudioJobLeaseParams{ID: jobID, LeaseOwner: &workerID, LeaseSeconds: int32(cfg.WorkerLeaseSeconds)}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return workCtx, func() { close(stop); cancel() }
}
func reclaim(ctx context.Context, q *store.Queries, cfg serviceconfig.Config) error {
	message := "worker lease expired before completion"
	_, err := q.ReclaimExpiredAudioJobs(ctx, store.ReclaimExpiredAudioJobsParams{
		AttemptCount: int32(cfg.WorkerMaxAttempts), ErrorSummary: &message,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(time.Duration(cfg.WorkerRetryBackoffSeconds) * time.Second), Valid: true},
	})
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
func fail(ctx context.Context, q *store.Queries, job store.AudioJob, cfg serviceconfig.Config, workerID string, cause error, emitter *activity.Emitter) error {
	status := "retryable"
	if int(job.AttemptCount) >= cfg.WorkerMaxAttempts {
		status = "failed"
	}
	message := cause.Error()
	backoff := time.Duration(cfg.WorkerRetryBackoffSeconds) * time.Second * time.Duration(job.AttemptCount)
	retryAt := time.Now().Add(backoff)
	if _, err := q.RetryAudioJob(ctx, store.RetryAudioJobParams{ID: job.ID, Status: status, ErrorSummary: &message, AvailableAt: pgtype.Timestamptz{Time: retryAt, Valid: true}, LeaseOwner: &workerID}); err != nil {
		return err
	}
	jobID := uuidString(job.ID)
	accepted := activity.EventID("voice.job.accepted", jobID)
	event := activity.Event{
		Type:          "voice.job.retry_scheduled",
		Subject:       activity.Subject{Type: "voice_job", ID: jobID},
		CorrelationID: jobID,
		CausationID:   &accepted,
		Actor:         activity.System("voice-worker"),
		Outcome:       activity.Pending,
		Summary:       "Transcription will be retried: " + activity.Preview(message, 160),
		Data:          map[string]any{"attempt": job.AttemptCount, "error": activity.Preview(message, 500), "retryAt": retryAt.UTC()},
	}
	if status == "failed" {
		event.Type, event.Outcome, event.Summary = "voice.job.failed", activity.Failed, "Transcription failed: "+activity.Preview(message, 160)
		delete(event.Data, "retryAt")
	}
	event.ID = activity.EventID(event.Type, jobID, fmt.Sprint(job.AttemptCount))
	emitter.Emit(ctx, event)
	return nil
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
	// The capture belongs to this job's workflow and was caused by its
	// transcription.
	transcribed := activity.EventID("voice.job.transcribed", uuidString(job.ID))
	activity.Trace{CorrelationID: uuidString(job.ID), CausationID: &transcribed, Actor: activity.System("voice-worker")}.Apply(req.Header)
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
