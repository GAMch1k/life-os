package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
)

type Handler struct {
	queries  *store.Queries
	activity *activity.Emitter
}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(queries *store.Queries, emitter *activity.Emitter) *Handler {
	return &Handler{queries: queries, activity: emitter}
}

func (h *Handler) ListTasks(c fiber.Ctx, params generated.ListTasksParams) error {
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			return fiber.NewError(fiber.StatusBadRequest, "limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	if params.Query != nil && (*params.Query == "" || len(*params.Query) > 200) {
		return fiber.NewError(fiber.StatusBadRequest, "query must be between 1 and 200 characters")
	}
	includeUndated := true
	if params.IncludeUndated != nil {
		includeUndated = *params.IncludeUndated
	}
	items, err := h.queries.ListTasks(c.Context(), store.ListTasksParams{CompletedFilter: params.Completed, SearchQuery: params.Query, DueBefore: timestamp(params.DueBefore), IncludeUndated: includeUndated, PageLimit: int32(limit)})
	if err != nil {
		return internal(err)
	}
	result := make([]generated.Task, 0, len(items))
	for _, item := range items {
		result = append(result, task(item))
	}
	return c.JSON(generated.TaskList{Items: result})
}
func (h *Handler) CreateTask(c fiber.Ctx) error {
	var body generated.CreateTaskJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid task request")
	}
	if body.Title == "" {
		return fiber.NewError(400, "title is required")
	}
	params := createParams(body)
	key, hash := idempotency(c)
	params.AutomationIdempotencyKey, params.AutomationPayloadHash = key, hash
	item, err := h.queries.CreateTask(c.Context(), params)
	if err != nil {
		return internal(err)
	}
	if key != nil && (item.AutomationPayloadHash == nil || *item.AutomationPayloadHash != *hash) {
		return fiber.NewError(409, "idempotency key was already used with a different payload")
	}
	// A deterministic id makes an idempotent replay record nothing new.
	h.record(c, activity.EventID("task.created", uuid(item.ID).String()), "task.created", item, "created", nil)
	return c.Status(201).JSON(task(item))
}

func idempotency(c fiber.Ctx) (*string, *string) {
	key := c.Get("Idempotency-Key")
	if key == "" {
		return nil, nil
	}
	sum := sha256.Sum256(c.Body())
	hash := fmt.Sprintf("%x", sum[:])
	return &key, &hash
}
func (h *Handler) GetTask(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.GetTask(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(task(item))
}
func (h *Handler) UpdateTask(c fiber.Ctx, id types.UUID) error {
	var body generated.UpdateTaskJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid task request")
	}
	if body.Title != nil && *body.Title == "" {
		return fiber.NewError(400, "title must not be empty")
	}
	if body.ClearDueAt != nil && *body.ClearDueAt && body.DueAt != nil {
		return fiber.NewError(400, "dueAt and clearDueAt cannot be used together")
	}
	params := updateParams(body)
	params.ID = pgUUID(id)
	item, err := h.queries.UpdateTask(c.Context(), params)
	if err != nil {
		return databaseError(err)
	}
	h.record(c, googleuuid.New(), "task.updated", item, "updated", map[string]any{"changes": changes(body)})
	return c.JSON(task(item))
}
func (h *Handler) CompleteTask(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.CompleteTask(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	h.record(c, googleuuid.New(), "task.completed", item, "completed", nil)
	return c.JSON(task(item))
}
func (h *Handler) ReopenTask(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.ReopenTask(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	h.record(c, googleuuid.New(), "task.reopened", item, "reopened", nil)
	return c.JSON(task(item))
}

// record emits a Task activity event after the mutation has persisted. The
// request's correlation/causation/actor headers tie it to the workflow that
// asked for it (for example an Inbox capture processed through MCP).
func (h *Handler) record(c fiber.Ctx, id googleuuid.UUID, eventType string, item store.Task, verb string, data map[string]any) {
	trace := activity.FromRequest(c)
	if data == nil {
		data = map[string]any{}
	}
	data["title"] = item.Title
	data["completed"] = item.CompletedAt.Valid
	if item.DueAt.Valid {
		data["dueAt"] = item.DueAt.Time
	}
	h.activity.Emit(c.Context(), activity.Event{
		ID:            id,
		Type:          eventType,
		Subject:       activity.Subject{Type: "task", ID: uuid(item.ID).String()},
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         trace.Actor,
		Outcome:       activity.Succeeded,
		Summary:       fmt.Sprintf("Task “%s” %s", activity.Preview(item.Title, 120), verb),
		Data:          data,
	})
}

func changes(body generated.UpdateTaskRequest) []string {
	result := []string{}
	if body.Title != nil {
		result = append(result, "title")
	}
	if body.Notes != nil {
		result = append(result, "notes")
	}
	if body.DueAt != nil {
		result = append(result, "dueAt")
	}
	if body.ClearDueAt != nil && *body.ClearDueAt {
		result = append(result, "clearDueAt")
	}
	if body.Recurrence != nil {
		result = append(result, "recurrence")
	}
	return result
}

func createParams(body generated.CreateTaskRequest) store.CreateTaskParams {
	frequency, interval, until := recurrence(body.Recurrence)
	return store.CreateTaskParams{Title: body.Title, Notes: body.Notes, DueAt: timestamp(body.DueAt), RecurrenceFrequency: frequency, RecurrenceInterval: interval, RecurrenceUntil: until}
}
func updateParams(body generated.UpdateTaskRequest) store.UpdateTaskParams {
	frequency, interval, until := recurrence(body.Recurrence)
	clearDueAt := body.ClearDueAt != nil && *body.ClearDueAt
	return store.UpdateTaskParams{Title: body.Title, Notes: body.Notes, DueAt: timestamp(body.DueAt), ClearDueAt: clearDueAt, RecurrenceFrequency: frequency, RecurrenceInterval: interval, RecurrenceUntil: until}
}
func recurrence(value *generated.Recurrence) (*string, *int32, pgtype.Timestamptz) {
	if value == nil {
		return nil, nil, pgtype.Timestamptz{}
	}
	frequency := string(value.Frequency)
	interval := int32(value.Interval)
	return &frequency, &interval, timestamp(value.Until)
}
func task(value store.Task) generated.Task {
	return generated.Task{Id: uuid(value.ID), Title: value.Title, Notes: value.Notes, DueAt: nullableTime(value.DueAt), Completed: value.CompletedAt.Valid, CompletedAt: nullableTime(value.CompletedAt), Recurrence: recurrenceOutput(value), CreatedAt: value.CreatedAt.Time, UpdatedAt: value.UpdatedAt.Time}
}
func recurrenceOutput(value store.Task) *generated.Recurrence {
	if value.RecurrenceFrequency == nil {
		return nil
	}
	recurrence := &generated.Recurrence{Frequency: generated.RecurrenceFrequency(*value.RecurrenceFrequency), Interval: int(*value.RecurrenceInterval), Until: nullableTime(value.RecurrenceUntil)}
	return recurrence
}
func timestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
func uuid(value pgtype.UUID) types.UUID   { return types.UUID(value.Bytes) }
func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }
func internal(err error) error {
	return fiber.NewError(500, fmt.Sprintf("tasks query failed: %v", err))
}
func databaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fiber.NewError(404, "task not found")
	}
	return internal(err)
}
