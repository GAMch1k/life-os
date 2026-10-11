package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/inbox/internal/api/generated"
	"github.com/GAMch1k/life-os/services/inbox/internal/media"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

type Handler struct {
	queries  *store.Queries
	activity *activity.Emitter
	notifier *Notifier
	// media holds capture images, each at most maxImageBytes (see
	// WithMedia).
	media         media.Store
	maxImageBytes int64
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the Inbox API. emitter records activity events and
// notifier tells the owner about Human Review and failed captures; either may
// be nil.
func NewHandler(queries *store.Queries, emitter *activity.Emitter, notifier *Notifier) *Handler {
	return &Handler{queries: queries, activity: emitter, notifier: notifier}
}
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
	items, err := h.queries.ListInboxItems(c.Context(), store.ListInboxItemsParams{UnresolvedFilter: params.Unresolved, StateFilter: state, DueFilter: params.Due, PageLimit: int32(limit)})
	if err != nil {
		return internal(err)
	}
	result := make([]generated.InboxItem, 0, len(items))
	for _, item := range items {
		result = append(result, output(item))
	}
	return c.JSON(generated.InboxItemList{Items: result})
}

// GetInboxBacklog reports unfinished work for status surfaces. It reads only
// durable Inbox state; it never inspects the processor.
func (h *Handler) GetInboxBacklog(c fiber.Ctx) error {
	backlog, err := h.queries.GetInboxBacklog(c.Context())
	if err != nil {
		return internal(err)
	}
	out := generated.InboxBacklog{
		PendingCount:     int(backlog.PendingCount),
		RetryingCount:    int(backlog.RetryingCount),
		HumanReviewCount: int(backlog.HumanReviewCount),
		OldestPendingAt:  nullableTime(backlog.OldestPendingAt),
		FailedCount:      ptr(int(backlog.FailedCount)),
		NextRetryAt:      nullableTime(backlog.NextRetryAt),
	}
	if backlog.NextRetryReason != "" {
		out.NextRetryReason = &backlog.NextRetryReason
	}
	failure, err := h.queries.GetLatestPendingFailure(c.Context())
	switch {
	case err == nil:
		out.LastFailureAt, out.LastFailureSummary = nullableTime(failure.UpdatedAt), failure.ErrorSummary
	case !errors.Is(err, pgx.ErrNoRows):
		return internal(err)
	}
	return c.JSON(out)
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
	references, err := h.captureMedia(c.Context(), body.MediaReferences)
	if err != nil {
		return err
	}
	encodedReferences, err := json.Marshal(references)
	if err != nil {
		return fiber.NewError(400, "invalid media references")
	}
	// Only an explicit upstream correlation is stored; otherwise the item
	// starts its own workflow and its id is the correlation id.
	trace := activity.TraceFromHeaders(func(key string) string { return c.Get(key) })
	var correlation *string
	if trace.CorrelationID != "" {
		correlation = &trace.CorrelationID
	}
	item, err := h.queries.CreateInboxItem(c.Context(), store.CreateInboxItemParams{Content: body.Content, Source: string(body.Source), SourceMetadata: metadata, MediaReferences: encodedReferences, SourceDedupeKey: body.DedupeKey, CorrelationID: correlation})
	if err != nil {
		return internal(err)
	}
	id := uuid(item.ID).String()
	data := map[string]any{"source": item.Source, "content": activity.Preview(item.Content, 280)}
	if len(references) > 0 {
		data["images"] = len(references)
	}
	h.record(c, item, activity.Event{
		ID:          activity.EventID("inbox.item.captured", id),
		Type:        "inbox.item.captured",
		Subject:     activity.Subject{Type: "inbox_item", ID: id},
		CausationID: trace.CausationID,
		Outcome:     activity.Succeeded,
		Summary:     fmt.Sprintf("Captured “%s” from %s", activity.Preview(item.Content, 80), item.Source),
		Data:        data,
	})
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
	item, err := h.queries.GetInboxItem(c.Context(), pgUUID(id))
	if err != nil {
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
	attemptID := uuid(attempt.ID).String()
	captured := activity.EventID("inbox.item.captured", id.String())
	h.record(c, item, activity.Event{
		ID:          activity.EventID("inbox.processing.started", attemptID),
		Type:        "inbox.processing.started",
		Subject:     activity.Subject{Type: "processing_attempt", ID: attemptID},
		CausationID: &captured,
		Outcome:     activity.Pending,
		Summary:     fmt.Sprintf("Processing started by %s", attempt.AgentName),
		Data:        map[string]any{"inboxItemId": id.String(), "attemptId": attemptID, "agentName": attempt.AgentName, "modelProfile": attempt.ModelProfile},
	})
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
	if (body.NextRetryAt != nil || body.RetryReason != nil) && body.Status != generated.UpdateProcessingAttemptRequestStatusRetryable {
		return fiber.NewError(400, "nextRetryAt and retryReason are only valid for a retryable attempt")
	}
	var nextRetryAt pgtype.Timestamptz
	if body.NextRetryAt != nil {
		nextRetryAt = pgtype.Timestamptz{Time: *body.NextRetryAt, Valid: true}
	}
	actions, err := json.Marshal(body.Actions)
	if err != nil {
		return fiber.NewError(400, "invalid actions")
	}
	attempt, err := h.queries.UpdateProcessingAttempt(c.Context(), store.UpdateProcessingAttemptParams{ID: pgUUID(attemptID), Status: string(body.Status), Actions: actions, ErrorSummary: body.ErrorSummary, NextRetryAt: nextRetryAt})
	if err != nil {
		return internal(err)
	}
	var failed *store.InboxItem
	switch body.Status {
	case generated.UpdateProcessingAttemptRequestStatusRetryable:
		// Back to raw, eligible for the processor once nextRetryAt has passed.
		if _, err := h.queries.ScheduleInboxItemRetry(c.Context(), store.ScheduleInboxItemRetryParams{ID: pgUUID(itemID), NextRetryAt: nextRetryAt, RetryReason: body.RetryReason}); err != nil {
			return internal(err)
		}
	case generated.UpdateProcessingAttemptRequestStatusFailed:
		// Automatic recovery is unlikely: keep it visible but stop retrying.
		item, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(itemID), State: "failed"})
		if err != nil {
			return internal(err)
		}
		failed = &item
	}
	h.recordAttemptOutcome(c, itemID, attempt, body)
	if failed != nil {
		// Failed for good, so the owner hears about it; a retry is not.
		reason := ""
		if body.ErrorSummary != nil {
			reason = *body.ErrorSummary
		}
		id := attemptID.String()
		h.notifier.send(*failed, activity.EventID("inbox.processing.failed", id), failedNotification(*failed, id, reason))
	}
	return c.JSON(attemptOutput(attempt))
}

// RecordAttemptRuntime stores the runtime state of an unfinished attempt and
// refreshes its heartbeat.
func (h *Handler) RecordAttemptRuntime(c fiber.Ctx, itemID types.UUID, attemptID types.UUID) error {
	var body generated.RecordAttemptRuntimeJSONRequestBody
	if err := c.Bind().Body(&body); err != nil || body.State == "" || body.Owner == "" || len(body.Owner) > 200 || (body.SessionId != nil && len(*body.SessionId) > 200) {
		return fiber.NewError(400, "invalid attempt runtime")
	}
	state := string(body.State)
	attempt, err := h.queries.RecordAttemptRuntime(c.Context(), store.RecordAttemptRuntimeParams{ID: pgUUID(attemptID), InboxItemID: pgUUID(itemID), RuntimeState: &state, RuntimeOwner: &body.Owner, RuntimeSessionID: body.SessionId})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := h.queries.GetProcessingAttemptForItem(c.Context(), store.GetProcessingAttemptForItemParams{ID: pgUUID(attemptID), InboxItemID: pgUUID(itemID)}); err != nil {
			return databaseError(err)
		}
		return fiber.NewError(409, "processing attempt already finished")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(attemptOutput(attempt))
}

// ListStaleProcessingAttempts lists unfinished attempts whose runtime stopped
// reporting, for reconciliation after a processor crash or restart.
func (h *Handler) ListStaleProcessingAttempts(c fiber.Ctx, params generated.ListStaleProcessingAttemptsParams) error {
	if params.StaleAfterSeconds < 30 || params.StaleAfterSeconds > 86400 {
		return fiber.NewError(400, "staleAfterSeconds must be between 30 and 86400")
	}
	attempts, err := h.queries.ListStaleProcessingAttempts(c.Context(), int32(params.StaleAfterSeconds))
	if err != nil {
		return internal(err)
	}
	items := make([]generated.ProcessingAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		items = append(items, attemptOutput(attempt))
	}
	return c.JSON(generated.ProcessingAttemptList{Items: items})
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
	metadata, err := json.Marshal(map[string]interface{}{"humanReview": map[string]interface{}{"attemptId": body.AttemptId.String(), "errorSummary": body.ErrorSummary, "proposedAction": body.ProposedAction, "candidates": body.Candidates}})
	if err != nil {
		return fiber.NewError(400, "invalid human review metadata")
	}
	item, err := h.queries.SetInboxItemHumanReview(c.Context(), store.SetInboxItemHumanReviewParams{ID: pgUUID(itemID), ProcessingMetadata: metadata})
	if err != nil {
		return databaseError(err)
	}
	attemptID := body.AttemptId.String()
	started := activity.EventID("inbox.processing.started", attemptID)
	tool, _ := body.ProposedAction["tool"].(string)
	candidates := len(body.Candidates)
	h.record(c, item, activity.Event{
		ID:          activity.EventID("inbox.human_review.requested", attemptID),
		Type:        "inbox.human_review.requested",
		Subject:     activity.Subject{Type: "inbox_item", ID: itemID.String()},
		CausationID: &started,
		Outcome:     activity.Pending,
		Summary:     fmt.Sprintf("Needs your decision: %s", activity.Preview(body.ErrorSummary, 120)),
		Data:        map[string]any{"attemptId": attemptID, "reason": activity.Preview(body.ErrorSummary, 2000), "proposedTool": tool, "candidateCount": candidates},
	})
	h.notifier.send(item, activity.EventID("inbox.human_review.requested", attemptID), humanReviewNotification(item, attemptID, body.ErrorSummary))
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
	event := activity.Event{
		ID:      googleuuid.New(),
		Type:    "inbox.human_review.resolved",
		Subject: activity.Subject{Type: "inbox_item", ID: itemID.String()},
		Outcome: activity.Succeeded,
		Summary: "Review decision recorded; processing will resume",
		Data:    map[string]any{"candidateId": body.CandidateId.String(), "approvedTool": approved["tool"]},
	}
	if attemptID, ok := review["attemptId"].(string); ok {
		requested := activity.EventID("inbox.human_review.requested", attemptID)
		event.CausationID = &requested
	}
	h.record(c, queued, event)
	return c.JSON(output(queued))
}
func attemptOutput(attempt store.InboxProcessingAttempt) generated.ProcessingAttempt {
	var actions []map[string]interface{}
	_ = json.Unmarshal(attempt.Actions, &actions)
	var runtimeState *generated.ProcessingAttemptRuntimeState
	if attempt.RuntimeState != nil {
		state := generated.ProcessingAttemptRuntimeState(*attempt.RuntimeState)
		runtimeState = &state
	}
	return generated.ProcessingAttempt{Id: uuid(attempt.ID), InboxItemId: uuid(attempt.InboxItemID), RuntimeState: runtimeState, RuntimeOwner: attempt.RuntimeOwner, RuntimeSessionId: attempt.RuntimeSessionID, RuntimeHeartbeatAt: nullableTime(attempt.RuntimeHeartbeatAt), IdempotencyKey: &attempt.IdempotencyKey, Status: generated.ProcessingAttemptStatus(attempt.Status), AgentName: attempt.AgentName, ModelProfile: attempt.ModelProfile, Actions: actions, ErrorSummary: attempt.ErrorSummary, NextRetryAt: nullableTime(attempt.NextRetryAt), StartedAt: nullableTime(attempt.StartedAt), CompletedAt: nullableTime(attempt.CompletedAt), CreatedAt: attempt.CreatedAt.Time}
}
func (h *Handler) DiscardInboxItem(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(id), State: "discarded"})
	if err != nil {
		return databaseError(err)
	}
	h.record(c, item, activity.Event{
		ID:      activity.EventID("inbox.item.discarded", id.String()),
		Type:    "inbox.item.discarded",
		Subject: activity.Subject{Type: "inbox_item", ID: id.String()},
		Outcome: activity.Succeeded,
		Summary: fmt.Sprintf("Discarded “%s”", activity.Preview(item.Content, 80)),
	})
	return c.JSON(output(item))
}
func (h *Handler) ResolveInboxItem(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.SetInboxItemState(c.Context(), store.SetInboxItemStateParams{ID: pgUUID(id), State: "resolved"})
	if err != nil {
		return databaseError(err)
	}
	h.record(c, item, activity.Event{
		ID:          activity.EventID("inbox.item.resolved", id.String()),
		Type:        "inbox.item.resolved",
		Subject:     activity.Subject{Type: "inbox_item", ID: id.String()},
		CausationID: activity.FromRequest(c).CausationID,
		Outcome:     activity.Succeeded,
		Summary:     fmt.Sprintf("Resolved “%s”", activity.Preview(item.Content, 80)),
	})
	return c.JSON(output(item))
}

// record emits an Inbox activity event in the item's workflow. Inbox owns the
// item's correlation id, so it is used even when the caller sent another one;
// causation and actor still come from the caller unless the event sets them.
func (h *Handler) record(c fiber.Ctx, item store.InboxItem, event activity.Event) {
	trace := activity.FromRequest(c)
	event.CorrelationID = correlationID(item)
	if event.CausationID == nil {
		event.CausationID = trace.CausationID
	}
	event.Actor = trace.Actor
	h.activity.Emit(c.Context(), event)
}

func (h *Handler) recordAttemptOutcome(c fiber.Ctx, itemID types.UUID, attempt store.InboxProcessingAttempt, body generated.UpdateProcessingAttemptRequest) {
	var eventType, outcome, summary string
	reason := ""
	if body.ErrorSummary != nil {
		reason = *body.ErrorSummary
	}
	switch body.Status {
	case generated.UpdateProcessingAttemptRequestStatusProcessed:
		eventType, outcome, summary = "inbox.processing.completed", activity.Succeeded, "Processing completed"
	case generated.UpdateProcessingAttemptRequestStatusRetryable:
		eventType, outcome, summary = "inbox.processing.retry_scheduled", activity.Pending, "Processing will be retried: "+activity.Preview(reason, 160)
	case generated.UpdateProcessingAttemptRequestStatusFailed:
		eventType, outcome, summary = "inbox.processing.failed", activity.Failed, "Processing failed: "+activity.Preview(reason, 160)
	default:
		return // "processing" is already covered by inbox.processing.started
	}
	item, err := h.queries.GetInboxItem(c.Context(), pgUUID(itemID))
	if err != nil {
		return
	}
	attemptID := uuid(attempt.ID).String()
	started := activity.EventID("inbox.processing.started", attemptID)
	tools := []string{}
	for _, action := range body.Actions {
		if tool, ok := action["tool"].(string); ok {
			tools = append(tools, tool)
		}
	}
	h.record(c, item, activity.Event{
		ID:          activity.EventID(eventType, attemptID),
		Type:        eventType,
		Subject:     activity.Subject{Type: "processing_attempt", ID: attemptID},
		CausationID: &started,
		Outcome:     outcome,
		Summary:     summary,
		Data:        retryData(map[string]any{"inboxItemId": itemID.String(), "attemptId": attemptID, "tools": tools, "errorSummary": activity.Preview(reason, 2000)}, body),
	})
}

// retryData adds the retry schedule to a retry_scheduled event.
func retryData(data map[string]any, body generated.UpdateProcessingAttemptRequest) map[string]any {
	if body.NextRetryAt != nil {
		data["nextRetryAt"] = body.NextRetryAt.UTC()
	}
	if body.RetryReason != nil {
		data["retryReason"] = *body.RetryReason
	}
	return data
}

func correlationID(item store.InboxItem) string {
	if item.CorrelationID != nil && *item.CorrelationID != "" {
		return *item.CorrelationID
	}
	return uuid(item.ID).String()
}
func output(item store.InboxItem) generated.InboxItem {
	var metadata map[string]interface{}
	var processingMetadata map[string]interface{}
	var references []string
	_ = json.Unmarshal(item.SourceMetadata, &metadata)
	_ = json.Unmarshal(item.ProcessingMetadata, &processingMetadata)
	_ = json.Unmarshal(item.MediaReferences, &references)
	if references == nil {
		// Older captures without images stored JSON null.
		references = []string{}
	}
	return generated.InboxItem{Id: uuid(item.ID), CorrelationId: correlationID(item), Content: item.Content, Source: generated.InboxItemSource(item.Source), SourceMetadata: &metadata, MediaReferences: &references, State: generated.InboxItemState(item.State), ProcessingMetadata: processingMetadata, NextRetryAt: nullableTime(item.NextRetryAt), RetryReason: item.RetryReason, ResolvedAt: nullableTime(item.ResolvedAt), CreatedAt: item.CreatedAt.Time, UpdatedAt: item.UpdatedAt.Time}
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
func ptr[T any](value T) *T               { return &value }
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
