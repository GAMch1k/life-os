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
	item, err := h.queries.CreateInboxItem(c.Context(), store.CreateInboxItemParams{Content: body.Content, Source: string(body.Source), SourceMetadata: metadata, MediaReferences: media, SourceDedupeKey: body.DedupeKey})
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
	// The Inbox item exposes the active lifecycle to clients while each attempt
	// retains its own immutable identity for retry/audit history.
	if _, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(id), State: "processing"}); err != nil {
		return internal(err)
	}
	return c.Status(201).JSON(attemptOutput(attempt))
}
func (h *Handler) UpdateProcessingAttempt(c fiber.Ctx, itemID types.UUID, attemptID types.UUID) error {
	if _, err := h.queries.GetProcessingAttemptForItem(c.Context(), store.GetProcessingAttemptForItemParams{ID: pgUUID(attemptID), InboxItemID: pgUUID(itemID)}); err != nil {
		return databaseError(err)
	}
	var body generated.UpdateProcessingAttemptJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid processing-attempt update")
	}
	if body.Status == "" || body.Actions == nil {
		return fiber.NewError(400, "status and actions are required")
	}
	actions, err := json.Marshal(body.Actions)
	if err != nil {
		return fiber.NewError(400, "invalid actions")
	}
	attempt, err := h.queries.UpdateProcessingAttempt(c.Context(), store.UpdateProcessingAttemptParams{ID: pgUUID(attemptID), Status: string(body.Status), Actions: actions, ErrorSummary: body.ErrorSummary})
	if err != nil {
		return internal(err)
	}
	if body.Status == generated.UpdateProcessingAttemptRequestStatusRetryable || body.Status == generated.UpdateProcessingAttemptRequestStatusFailed {
		if _, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(itemID), State: "raw"}); err != nil {
			return internal(err)
		}
	}
	return c.JSON(attemptOutput(attempt))
}
func (h *Handler) CreateHumanReview(c fiber.Ctx, itemID types.UUID) error {
	var body generated.CreateHumanReviewJSONRequestBody
	if err := c.Bind().Body(&body); err != nil || body.ErrorSummary == "" || len(body.ProposedAction) == 0 {
		return fiber.NewError(400, "invalid human review request")
	}
	if _, err := h.queries.GetProcessingAttemptForItem(c.Context(), store.GetProcessingAttemptForItemParams{ID: pgUUID(body.AttemptId), InboxItemID: pgUUID(itemID)}); err != nil {
		return databaseError(err)
	}
	actions, err := json.Marshal([]map[string]interface{}{body.ProposedAction})
	if err != nil {
		return fiber.NewError(400, "invalid proposed action")
	}
	if _, err := h.queries.UpdateProcessingAttempt(c.Context(), store.UpdateProcessingAttemptParams{ID: pgUUID(body.AttemptId), Status: "human_review", Actions: actions, ErrorSummary: &body.ErrorSummary}); err != nil {
		return internal(err)
	}
	metadata, err := json.Marshal(map[string]interface{}{"humanReview": map[string]interface{}{"errorSummary": body.ErrorSummary, "proposedAction": body.ProposedAction, "candidates": body.Candidates}})
	if err != nil {
		return fiber.NewError(400, "invalid human review metadata")
	}
	item, err := h.queries.SetInboxItemHumanReview(c.Context(), store.SetInboxItemHumanReviewParams{ID: pgUUID(itemID), ProcessingMetadata: metadata})
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(output(item))
}
func (h *Handler) ResolveHumanReview(c fiber.Ctx, itemID types.UUID) error {
	var body generated.ResolveHumanReviewJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid human review selection")
	}
	item, err := h.queries.GetInboxItem(c.Context(), pgUUID(itemID))
	if err != nil {
		return databaseError(err)
	}
	if item.State != "human_review" {
		return fiber.NewError(400, "inbox item is not awaiting human review")
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(item.ProcessingMetadata, &metadata); err != nil {
		return internal(err)
	}
	review, ok := metadata["humanReview"].(map[string]interface{})
	if !ok {
		return fiber.NewError(400, "human review details are missing")
	}
	if !containsCandidate(review["candidates"], body.CandidateId.String()) {
		return fiber.NewError(400, "selected candidate is not part of this review")
	}
	proposed, ok := review["proposedAction"].(map[string]interface{})
	if !ok {
		return fiber.NewError(400, "proposed action is missing")
	}
	approved, err := approvedAction(proposed, body.CandidateId.String())
	if err != nil {
		return fiber.NewError(400, "invalid proposed action")
	}
	review["resolution"] = map[string]interface{}{"candidateId": body.CandidateId.String()}
	metadata["humanReview"] = review
	metadata["approvedAction"] = approved
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return internal(err)
	}
	queued, err := h.queries.ResolveInboxItemHumanReview(c.Context(), store.ResolveInboxItemHumanReviewParams{ID: pgUUID(itemID), ProcessingMetadata: encoded})
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(output(queued))
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
	var processingMetadata map[string]interface{}
	var media []string
	_ = json.Unmarshal(item.SourceMetadata, &metadata)
	_ = json.Unmarshal(item.ProcessingMetadata, &processingMetadata)
	_ = json.Unmarshal(item.MediaReferences, &media)
	return generated.InboxItem{Id: uuid(item.ID), Content: item.Content, Source: generated.InboxItemSource(item.Source), SourceMetadata: &metadata, MediaReferences: &media, State: generated.InboxItemState(item.State), ProcessingMetadata: processingMetadata, ResolvedAt: nullableTime(item.ResolvedAt), CreatedAt: item.CreatedAt.Time, UpdatedAt: item.UpdatedAt.Time}
}
func containsCandidate(raw interface{}, candidateID string) bool {
	candidates, ok := raw.([]interface{})
	if !ok {
		return false
	}
	for _, rawCandidate := range candidates {
		candidate, ok := rawCandidate.(map[string]interface{})
		if ok && candidate["id"] == candidateID {
			return true
		}
	}
	return false
}
func approvedAction(proposed map[string]interface{}, candidateID string) (map[string]interface{}, error) {
	raw, err := json.Marshal(proposed)
	if err != nil {
		return nil, err
	}
	var approved map[string]interface{}
	if err := json.Unmarshal(raw, &approved); err != nil {
		return nil, err
	}
	arguments, ok := approved["arguments"].(map[string]interface{})
	if !ok || approved["tool"] == nil {
		return nil, errors.New("missing action arguments")
	}
	delete(arguments, "taskQuery")
	arguments["taskId"] = candidateID
	approved["arguments"] = arguments
	return approved, nil
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
