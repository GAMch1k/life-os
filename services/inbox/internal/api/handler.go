package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/inbox/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

type Handler struct{ queries *store.Queries }

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(queries *store.Queries) *Handler { return &Handler{queries: queries} }
func (h *Handler) ListInboxItems(c fiber.Ctx, params generated.ListInboxItemsParams) error {
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	var state *string
	if params.State != nil {
		value := string(*params.State)
		state = &value
	}
	items, err := h.queries.ListInboxItems(c.Context(), store.ListInboxItemsParams{UnresolvedFilter: params.Unresolved, StateFilter: state, PageLimit: int32(limit)})
	if err != nil {
		return internal(err)
	}
	result := make([]generated.InboxItem, 0, len(items))
	for _, item := range items {
		result = append(result, output(item))
	}
	return c.JSON(generated.InboxItemList{Items: result})
}
func (h *Handler) CreateInboxItem(c fiber.Ctx) error {
	var body generated.CreateInboxItemJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid inbox item request")
	}
	if body.Content == "" {
		return fiber.NewError(400, "content is required")
	}
	metadata, err := json.Marshal(body.SourceMetadata)
	if err != nil {
		return fiber.NewError(400, "invalid source metadata")
	}
	media, err := json.Marshal(body.MediaReferences)
	if err != nil {
		return fiber.NewError(400, "invalid media references")
	}
	item, err := h.queries.CreateInboxItem(c.Context(), store.CreateInboxItemParams{Content: body.Content, Source: string(body.Source), SourceMetadata: metadata, MediaReferences: media})
	if err != nil {
		return internal(err)
	}
	return c.Status(201).JSON(output(item))
}
func (h *Handler) GetInboxItem(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.GetInboxItem(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(output(item))
}
func (h *Handler) ListProcessingAttempts(c fiber.Ctx, id types.UUID) error {
	if _, err := h.queries.GetInboxItem(c.Context(), pgUUID(id)); err != nil {
		return databaseError(err)
	}
	attempts, err := h.queries.ListProcessingAttempts(c.Context(), pgUUID(id))
	if err != nil {
		return internal(err)
	}
	items := make([]generated.ProcessingAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		items = append(items, attemptOutput(attempt))
	}
	return c.JSON(generated.ProcessingAttemptList{Items: items})
}
func (h *Handler) CreateProcessingAttempt(c fiber.Ctx, id types.UUID) error {
	if _, err := h.queries.GetInboxItem(c.Context(), pgUUID(id)); err != nil {
		return databaseError(err)
	}
	var body generated.CreateProcessingAttemptJSONRequestBody
	if err := c.Bind().Body(&body); err != nil || body.IdempotencyKey == "" || body.AgentName == "" || body.ModelProfile == "" {
		return fiber.NewError(400, "invalid processing attempt")
	}
	attempt, err := h.queries.CreateProcessingAttempt(c.Context(), store.CreateProcessingAttemptParams{InboxItemID: pgUUID(id), IdempotencyKey: body.IdempotencyKey, AgentName: body.AgentName, ModelProfile: body.ModelProfile, Actions: []byte("[]")})
	if err != nil {
		return internal(err)
	}
	return c.Status(201).JSON(attemptOutput(attempt))
}
func attemptOutput(attempt store.InboxProcessingAttempt) generated.ProcessingAttempt {
	var actions []map[string]interface{}
	_ = json.Unmarshal(attempt.Actions, &actions)
	return generated.ProcessingAttempt{Id: uuid(attempt.ID), IdempotencyKey: &attempt.IdempotencyKey, Status: generated.ProcessingAttemptStatus(attempt.Status), AgentName: attempt.AgentName, ModelProfile: attempt.ModelProfile, Actions: actions, ErrorSummary: attempt.ErrorSummary, StartedAt: nullableTime(attempt.StartedAt), CompletedAt: nullableTime(attempt.CompletedAt), CreatedAt: attempt.CreatedAt.Time}
}
func (h *Handler) DiscardInboxItem(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(id), State: "discarded"})
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(output(item))
}
func (h *Handler) ResolveInboxItem(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(id), State: "resolved"})
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(output(item))
}
func output(item store.InboxItem) generated.InboxItem {
	var metadata map[string]interface{}
	var media []string
	_ = json.Unmarshal(item.SourceMetadata, &metadata)
	_ = json.Unmarshal(item.MediaReferences, &media)
	return generated.InboxItem{Id: uuid(item.ID), Content: item.Content, Source: generated.InboxItemSource(item.Source), SourceMetadata: &metadata, MediaReferences: &media, State: generated.InboxItemState(item.State), ResolvedAt: nullableTime(item.ResolvedAt), CreatedAt: item.CreatedAt.Time, UpdatedAt: item.UpdatedAt.Time}
}
func uuid(value pgtype.UUID) types.UUID   { return types.UUID(value.Bytes) }
func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }
func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
func internal(err error) error {
	return fiber.NewError(500, fmt.Sprintf("inbox query failed: %v", err))
}
func databaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fiber.NewError(404, "inbox item not found")
	}
	return internal(err)
}
