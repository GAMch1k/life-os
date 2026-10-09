package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/voice/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/storage"
)

type Handler struct {
	queries        *store.Queries
	files          storage.Files
	retention      time.Duration
	maxUploadBytes int64
	activity       *activity.Emitter
	// whisperReady probes the transcription dependency for status surfaces.
	whisperReady func(context.Context) bool
}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(queries *store.Queries, files storage.Files, retention time.Duration, maxUploadBytes int64, emitter *activity.Emitter, whisperURL string) *Handler {
	return &Handler{queries: queries, files: files, retention: retention, maxUploadBytes: maxUploadBytes, activity: emitter, whisperReady: readinessProbe(whisperURL)}
}

// readinessProbe reports whether a dependency answers its /ready endpoint
// within a short timeout.
func readinessProbe(baseURL string) func(context.Context) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(ctx context.Context) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/ready", nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode/100 == 2
	}
}

// GetAudioBacklog reports unfinished transcription work from durable job state
// and whether the Whisper dependency is ready.
func (h *Handler) GetAudioBacklog(c fiber.Ctx) error {
	backlog, err := h.queries.GetAudioBacklog(c.Context())
	if err != nil {
		return internal(err)
	}
	out := generated.AudioBacklog{
		QueuedCount:     int(backlog.QueuedCount),
		ProcessingCount: int(backlog.ProcessingCount),
		RetryableCount:  int(backlog.RetryableCount),
		OldestWaitingAt: timePtr(backlog.OldestWaitingAt),
		NextRetryAt:     timePtr(backlog.NextRetryAt),
		// A worker that holds an expired lease, or leaves due work untouched
		// while nothing is processing, is not making progress.
		WorkerStalled: backlog.ExpiredLeaseCount > 0 || (backlog.OverdueCount > 0 && backlog.ProcessingCount == 0),
		WhisperReady:  h.whisperReady(c.Context()),
	}
	failure, err := h.queries.GetLatestAudioFailure(c.Context())
	switch {
	case err == nil:
		out.LastFailureAt, out.LastFailureSummary = timePtr(failure.UpdatedAt), failure.ErrorSummary
	case !errors.Is(err, pgx.ErrNoRows):
		return internal(err)
	}
	return c.JSON(out)
}

func (h *Handler) CreateAudioJob(c fiber.Ctx) error {
	file, err := c.FormFile("audio")
	if err != nil || file.Size <= 0 {
		return fiber.NewError(400, "audio is required")
	}
	contentType, err := storage.ValidateAudio(file, h.maxUploadBytes)
	if err != nil {
		if file.Size > h.maxUploadBytes {
			return fiber.NewError(413, err.Error())
		}
		return fiber.NewError(400, err.Error())
	}
	source := c.FormValue("source", "web")
	if source != "web" && source != "watch" && source != "manual" {
		return fiber.NewError(400, "invalid source")
	}
	metadata := map[string]any{}
	if raw := c.FormValue("sourceMetadata"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return fiber.NewError(400, "sourceMetadata must be JSON")
		}
	}
	encoded, _ := json.Marshal(metadata)
	key := fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(file.Filename))
	n, err := h.files.Store(key, file)
	if err != nil {
		return internal(err)
	}
	expires := time.Now().Add(h.retention)
	job, err := h.queries.CreateAudioJob(c.Context(), store.CreateAudioJobParams{Source: source, SourceMetadata: encoded, ObjectKey: key, ContentType: contentType, ByteSize: n, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}})
	if err != nil {
		_ = h.files.Remove(key)
		return internal(err)
	}
	// A Voice job starts its own workflow: its id is the correlation id that
	// the worker hands on to Inbox.
	id := types.UUID(job.ID.Bytes).String()
	h.activity.Emit(c.Context(), activity.Event{
		ID:            activity.EventID("voice.job.accepted", id),
		Type:          "voice.job.accepted",
		Subject:       activity.Subject{Type: "voice_job", ID: id},
		CorrelationID: id,
		Actor:         activity.FromRequest(c).Actor,
		Outcome:       activity.Pending,
		Summary:       "Voice recording accepted for transcription",
		Data:          map[string]any{"source": source, "contentType": contentType, "byteSize": n},
	})
	return c.Status(202).JSON(output(job))
}
func (h *Handler) GetAudioJob(c fiber.Ctx, id types.UUID) error {
	job, err := h.queries.GetAudioJob(c.Context(), pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return fiber.NewError(404, "job not found")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(output(job))
}
func output(job store.AudioJob) generated.AudioJob {
	return generated.AudioJob{Id: types.UUID(job.ID.Bytes), Status: generated.AudioJobStatus(job.Status), Source: generated.AudioJobSource(job.Source), Transcript: job.Transcript, AttemptCount: int(job.AttemptCount), ErrorSummary: job.ErrorSummary, CreatedAt: job.CreatedAt.Time, UpdatedAt: job.UpdatedAt.Time, CompletedAt: timePtr(job.CompletedAt), ExpiresAt: timePtr(job.ExpiresAt), InboxItemId: uuidPtr(job.InboxItemID)}
}
func pgUUID(v types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(v), Valid: true} }
func timePtr(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	x := v.Time
	return &x
}
func uuidPtr(v pgtype.UUID) *types.UUID {
	if !v.Valid {
		return nil
	}
	x := types.UUID(v.Bytes)
	return &x
}
func internal(err error) error { return fiber.NewError(500, "voice job failed: "+err.Error()) }
