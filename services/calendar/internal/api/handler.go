package api

import (
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

type Handler struct{ queries *store.Queries }

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(queries *store.Queries) *Handler { return &Handler{queries: queries} }
func (h *Handler) ListEvents(c fiber.Ctx, params generated.ListEventsParams) error {
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	items, err := h.queries.ListEvents(c.Context(), store.ListEventsParams{FromDate: date(params.From), ToDate: date(params.To), FromTime: timestamp(&params.From), ToTime: timestamp(&params.To), PageLimit: int32(limit)})
	if err != nil {
		return internal(err)
	}
	result := make([]generated.CalendarEvent, 0, len(items))
	for _, item := range items {
		result = append(result, event(item))
	}
	return c.JSON(generated.EventList{Items: result})
}
func (h *Handler) CreateEvent(c fiber.Ctx) error {
	var body generated.CreateEventJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid event request")
	}
	if err := validateTimes(body.AllDay, body.StartAt, body.EndAt, body.StartDate, body.EndDate); err != nil {
		return fiber.NewError(400, err.Error())
	}
	item, err := h.queries.CreateEvent(c.Context(), createParams(body))
	if err != nil {
		return internal(err)
	}
	return c.Status(201).JSON(event(item))
}
func (h *Handler) GetEvent(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.GetEvent(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(event(item))
}
func (h *Handler) UpdateEvent(c fiber.Ctx, id types.UUID) error {
	var body generated.UpdateEventJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid event request")
	}
	params := updateParams(body)
	params.ID = pgUUID(id)
	item, err := h.queries.UpdateEvent(c.Context(), params)
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(event(item))
}
func (h *Handler) DeleteEvent(c fiber.Ctx, id types.UUID) error {
	affected, err := h.queries.DeleteEvent(c.Context(), pgUUID(id))
	if err != nil {
		return internal(err)
	}
	if affected == 0 {
		return fiber.NewError(404, "event not found")
	}
	return c.SendStatus(204)
}
func createParams(body generated.CreateEventRequest) store.CreateEventParams {
	frequency, interval, until := recurrence(body.Recurrence)
	var provider, externalID *string
	if body.ExternalReference != nil {
		provider = &body.ExternalReference.Provider
		externalID = &body.ExternalReference.ExternalId
	}
	return store.CreateEventParams{Title: body.Title, Description: body.Description, AllDay: body.AllDay, StartAt: timestamp(body.StartAt), EndAt: timestamp(body.EndAt), StartDate: datePointer(body.StartDate), EndDate: datePointer(body.EndDate), RecurrenceFrequency: frequency, RecurrenceInterval: interval, RecurrenceUntil: until, ExternalProvider: provider, ExternalID: externalID}
}
func updateParams(body generated.UpdateEventRequest) store.UpdateEventParams {
	frequency, interval, until := recurrence(body.Recurrence)
	return store.UpdateEventParams{Title: body.Title, Description: body.Description, AllDay: body.AllDay, StartAt: timestamp(body.StartAt), EndAt: timestamp(body.EndAt), StartDate: datePointer(body.StartDate), EndDate: datePointer(body.EndDate), RecurrenceFrequency: frequency, RecurrenceInterval: interval, RecurrenceUntil: until}
}
func recurrence(value *generated.Recurrence) (*string, *int32, pgtype.Timestamptz) {
	if value == nil {
		return nil, nil, pgtype.Timestamptz{}
	}
	frequency := string(value.Frequency)
	interval := int32(value.Interval)
	return &frequency, &interval, timestamp(value.Until)
}
func event(value store.Event) generated.CalendarEvent {
	var external *generated.ExternalReference
	if value.ExternalProvider != nil {
		external = &generated.ExternalReference{Provider: *value.ExternalProvider, ExternalId: *value.ExternalID}
	}
	return generated.CalendarEvent{Id: uuid(value.ID), Title: value.Title, Description: value.Description, AllDay: value.AllDay, StartAt: nullableTime(value.StartAt), EndAt: nullableTime(value.EndAt), StartDate: nullableDate(value.StartDate), EndDate: nullableDate(value.EndDate), Recurrence: recurrenceOutput(value), ExternalReference: external, CreatedAt: value.CreatedAt.Time, UpdatedAt: value.UpdatedAt.Time}
}
func recurrenceOutput(value store.Event) *generated.Recurrence {
	if value.RecurrenceFrequency == nil {
		return nil
	}
	return &generated.Recurrence{Frequency: generated.RecurrenceFrequency(*value.RecurrenceFrequency), Interval: int(*value.RecurrenceInterval), Until: nullableTime(value.RecurrenceUntil)}
}
func validateTimes(allDay bool, start, end *time.Time, startDate, endDate *types.Date) error {
	if allDay {
		if startDate == nil || endDate == nil || start != nil || end != nil {
			return errors.New("all-day events require startDate and endDate only")
		}
		if endDate.Time.Before(startDate.Time) {
			return errors.New("endDate must not precede startDate")
		}
		return nil
	}
	if start == nil || end == nil || startDate != nil || endDate != nil {
		return errors.New("timed events require startAt and endAt only")
	}
	if !end.After(*start) {
		return errors.New("endAt must be after startAt")
	}
	return nil
}
func uuid(value pgtype.UUID) types.UUID   { return types.UUID(value.Bytes) }
func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }
func timestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
func date(value time.Time) pgtype.Date { return pgtype.Date{Time: value, Valid: true} }
func datePointer(value *types.Date) pgtype.Date {
	if value == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: value.Time, Valid: true}
}
func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}
func nullableDate(value pgtype.Date) *types.Date {
	if !value.Valid {
		return nil
	}
	out := types.Date{Time: value.Time}
	return &out
}
func internal(err error) error {
	return fiber.NewError(500, fmt.Sprintf("calendar query failed: %v", err))
}
func databaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fiber.NewError(404, "event not found")
	}
	return internal(err)
}
