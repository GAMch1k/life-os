package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/voice/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/storage"
)

type Handler struct {
	queries        *store.Queries
	files          storage.Files
	retention      time.Duration
	maxUploadBytes int64
}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(queries *store.Queries, files storage.Files, retention time.Duration, maxUploadBytes int64) *Handler {
	return &Handler{queries: queries, files: files, retention: retention, maxUploadBytes: maxUploadBytes}
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
