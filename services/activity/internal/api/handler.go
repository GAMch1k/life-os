// Package api contains the Activity service's HTTP transport adapters.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/activity/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/activity/internal/repository/generated"
)

// Store is the persistence the handler needs; *store.Queries satisfies it.
type Store interface {
	RecordActivityEvent(context.Context, store.RecordActivityEventParams) (store.ActivityEvent, error)
	GetActivityEvent(context.Context, pgtype.UUID) (store.ActivityEvent, error)
	ListActivityEvents(context.Context, store.ListActivityEventsParams) ([]store.ActivityEvent, error)
}

type Handler struct{ store Store }

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(s Store) *Handler { return &Handler{store: s} }

const maxDataBytes = 16 << 10

var eventType = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)+$`)

func (h *Handler) RecordActivityEvent(c fiber.Ctx) error {
	var body generated.RecordActivityEventJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid activity event")
	}
	params, err := recordParams(body)
	if err != nil {
		return fiber.NewError(400, err.Error())
	}
	event, err := h.store.RecordActivityEvent(c.Context(), params)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING: the id was already recorded, so the
		// emitter is retrying. Return the stored event unchanged.
		existing, getErr := h.store.GetActivityEvent(c.Context(), params.ID)
		if getErr != nil {
			return internal(getErr)
		}
		return c.JSON(output(existing))
	}
	if err != nil {
		return internal(err)
	}
	return c.Status(201).JSON(output(event))
}

func (h *Handler) GetActivityEvent(c fiber.Ctx, id types.UUID) error {
	event, err := h.store.GetActivityEvent(c.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return fiber.NewError(404, "activity event not found")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(output(event))
}

func (h *Handler) ListActivityEvents(c fiber.Ctx, params generated.ListActivityEventsParams) error {
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			return fiber.NewError(400, "limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	query := store.ListActivityEventsParams{
		CorrelationID: params.CorrelationId,
		SubjectType:   params.SubjectType,
		SubjectID:     params.SubjectId,
		EventType:     params.Type,
		Source:        params.Source,
		OccurredFrom:  timestamp(params.From),
		OccurredTo:    timestamp(params.To),
		PageLimit:     int32(limit + 1),
	}
	if params.Outcome != nil {
		if !params.Outcome.Valid() {
			return fiber.NewError(400, "invalid outcome")
		}
		outcome := string(*params.Outcome)
		query.Outcome = &outcome
	}
	if params.Cursor != nil {
		at, id, err := decodeCursor(*params.Cursor)
		if err != nil {
			return fiber.NewError(400, "invalid cursor")
		}
		query.CursorAt, query.CursorID = pgtype.Timestamptz{Time: at, Valid: true}, pgtype.UUID{Bytes: id, Valid: true}
	}
	events, err := h.store.ListActivityEvents(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	result := generated.ActivityEventList{Items: make([]generated.ActivityEvent, 0, min(len(events), limit))}
	for index, event := range events {
		if index == limit {
			cursor := encodeCursor(events[index-1])
			result.NextCursor = &cursor
			break
		}
		result.Items = append(result.Items, output(event))
	}
	return c.JSON(result)
}

func recordParams(body generated.ActivityEventCreateRequest) (store.RecordActivityEventParams, error) {
	switch {
	case !eventType.MatchString(body.Type) || len(body.Type) > 128:
		return store.RecordActivityEventParams{}, errors.New("type must be a dotted lowercase name such as task.completed")
	case blank(body.Source, 64):
		return store.RecordActivityEventParams{}, errors.New("source is required")
	case blank(body.Subject.Type, 64) || blank(body.Subject.Id, 128):
		return store.RecordActivityEventParams{}, errors.New("subject type and id are required")
	case blank(body.CorrelationId, 128):
		return store.RecordActivityEventParams{}, errors.New("correlationId is required")
	case !body.Actor.Kind.Valid():
		return store.RecordActivityEventParams{}, errors.New("actor kind must be user, agent, or system")
	case body.Actor.Name != nil && len(*body.Actor.Name) > 128:
		return store.RecordActivityEventParams{}, errors.New("actor name is too long")
	case !body.Outcome.Valid():
		return store.RecordActivityEventParams{}, errors.New("outcome must be succeeded, pending, or failed")
	case blank(body.Summary, 500):
		return store.RecordActivityEventParams{}, errors.New("summary must be 1-500 characters")
	case body.OccurredAt.IsZero():
		return store.RecordActivityEventParams{}, errors.New("occurredAt is required")
	case body.Id == types.UUID{}:
		return store.RecordActivityEventParams{}, errors.New("id is required")
	}
	data := []byte("{}")
	if body.Data != nil {
		encoded, err := json.Marshal(body.Data)
		if err != nil || len(encoded) > maxDataBytes {
			return store.RecordActivityEventParams{}, fmt.Errorf("data must be a JSON object of at most %d bytes", maxDataBytes)
		}
		data = encoded
	}
	params := store.RecordActivityEventParams{
		ID:            pgtype.UUID{Bytes: body.Id, Valid: true},
		Type:          body.Type,
		Source:        body.Source,
		SubjectType:   body.Subject.Type,
		SubjectID:     body.Subject.Id,
		CorrelationID: body.CorrelationId,
		ActorKind:     string(body.Actor.Kind),
		ActorName:     body.Actor.Name,
		Outcome:       string(body.Outcome),
		Summary:       body.Summary,
		Data:          data,
		OccurredAt:    pgtype.Timestamptz{Time: body.OccurredAt.UTC(), Valid: true},
	}
	if body.CausationId != nil {
		params.CausationID = pgtype.UUID{Bytes: *body.CausationId, Valid: true}
	}
	return params, nil
}

func output(event store.ActivityEvent) generated.ActivityEvent {
	data := map[string]interface{}{}
	_ = json.Unmarshal(event.Data, &data)
	result := generated.ActivityEvent{
		Id:            types.UUID(event.ID.Bytes),
		Type:          event.Type,
		Source:        event.Source,
		Subject:       generated.ActivitySubject{Type: event.SubjectType, Id: event.SubjectID},
		CorrelationId: event.CorrelationID,
		Actor:         generated.ActivityActor{Kind: generated.ActivityActorKind(event.ActorKind), Name: event.ActorName},
		Outcome:       generated.ActivityOutcome(event.Outcome),
		Summary:       event.Summary,
		Data:          data,
		OccurredAt:    event.OccurredAt.Time,
		RecordedAt:    event.RecordedAt.Time,
	}
	if event.CausationID.Valid {
		causation := types.UUID(event.CausationID.Bytes)
		result.CausationId = &causation
	}
	return result
}

// Cursors encode the last row's (occurredAt, id) keyset position.
func encodeCursor(event store.ActivityEvent) string {
	raw := event.OccurredAt.Time.UTC().Format(time.RFC3339Nano) + "|" + types.UUID(event.ID.Bytes).String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, types.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, types.UUID{}, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, types.UUID{}, errors.New("malformed cursor")
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, types.UUID{}, err
	}
	var parsedID types.UUID
	if err := parsedID.UnmarshalText([]byte(id)); err != nil {
		return time.Time{}, types.UUID{}, err
	}
	return parsedAt, parsedID, nil
}

func blank(value string, max int) bool {
	return strings.TrimSpace(value) == "" || len(value) > max
}

func timestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func internal(err error) error {
	return fiber.NewError(500, fmt.Sprintf("activity query failed: %v", err))
}
