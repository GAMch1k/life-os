package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/config"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// maxReminders bounds an event's reminder list.
const maxReminders = 10

// eventFields is an event's editable state. Create builds it from the request
// and update merges the request into the stored state; either way the whole
// result is validated before it is written, so a timed event never keeps an
// all-day date and the other way round. Source identity is not editable and
// so is not part of it.
type eventFields struct {
	Title               string
	Description         *string
	Location            *string
	AllDay              bool
	StartAt, EndAt      *time.Time
	StartDate, EndDate  *types.Date
	TimeZone            *string
	Recurrence          *generated.Recurrence
	Reminders           *[]generated.Reminder // nil follows the defaults
	Status              generated.EventStatus
	CancelledAt         *time.Time
	RemovedFromSourceAt *time.Time
}

func fieldsFromCreate(body generated.CreateEventRequest) eventFields {
	return eventFields{
		Title: body.Title, Description: body.Description, Location: body.Location, AllDay: body.AllDay,
		StartAt: body.StartAt, EndAt: body.EndAt, StartDate: body.StartDate, EndDate: body.EndDate,
		TimeZone: body.TimeZone, Recurrence: body.Recurrence, Reminders: body.Reminders, Status: generated.Active,
	}
}

func fieldsFromStore(value store.Event) (eventFields, error) {
	reminders, err := decodeReminders(value.Reminders)
	if err != nil {
		return eventFields{}, err
	}
	return eventFields{
		Title: value.Title, Description: value.Description, Location: value.Location, AllDay: value.AllDay,
		StartAt: nullableTime(value.StartAt), EndAt: nullableTime(value.EndAt),
		StartDate: nullableDate(value.StartDate), EndDate: nullableDate(value.EndDate),
		TimeZone: value.TimeZone, Recurrence: recurrenceOutput(value), Reminders: reminders,
		Status: generated.EventStatus(value.Status), CancelledAt: nullableTime(value.CancelledAt),
		RemovedFromSourceAt: nullableTime(value.RemovedFromSourceAt),
	}, nil
}

// validate checks the event as a whole.
func (f eventFields) validate() error {
	if strings.TrimSpace(f.Title) == "" {
		return errors.New("title must not be empty")
	}
	if err := validateTimes(f.AllDay, f.StartAt, f.EndAt, f.StartDate, f.EndDate); err != nil {
		return err
	}
	if f.TimeZone != nil {
		if f.AllDay {
			return errors.New("all-day events take no timeZone: their dates are the owner's days")
		}
		if !config.ValidTimeZone(*f.TimeZone) {
			return errors.New("timeZone must be an IANA timezone such as Europe/Berlin")
		}
	}
	if f.Recurrence != nil {
		if !f.Recurrence.Frequency.Valid() {
			return errors.New("recurrence frequency must be daily, weekly, monthly, or yearly")
		}
		if f.Recurrence.Interval < 1 {
			return errors.New("recurrence interval must be at least 1")
		}
	}
	if f.Reminders != nil {
		if len(*f.Reminders) > maxReminders {
			return fmt.Errorf("an event takes at most %d reminders", maxReminders)
		}
		for _, reminder := range *f.Reminders {
			if err := validateReminder(reminder); err != nil {
				return err
			}
		}
	}
	if !f.Status.Valid() {
		return errors.New("status must be active or cancelled")
	}
	return nil
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

func (f eventFields) createParams() (store.CreateEventParams, error) {
	reminders, err := encodeReminders(f.Reminders)
	if err != nil {
		return store.CreateEventParams{}, err
	}
	frequency, interval, until := recurrenceColumns(f.Recurrence)
	return store.CreateEventParams{
		Title: f.Title, Description: f.Description, Location: f.Location, AllDay: f.AllDay,
		StartAt: timestamp(f.StartAt), EndAt: timestamp(f.EndAt), StartDate: datePointer(f.StartDate), EndDate: datePointer(f.EndDate),
		TimeZone: f.TimeZone, RecurrenceFrequency: frequency, RecurrenceInterval: interval, RecurrenceUntil: until,
		Reminders: reminders, Status: string(f.Status), CancelledAt: timestamp(f.CancelledAt),
		SourceKind: string(generated.EventSourceKindManual), RemovedFromSourceAt: timestamp(f.RemovedFromSourceAt),
	}, nil
}

func (f eventFields) updateParams(id pgtype.UUID, expectedUpdatedAt pgtype.Timestamptz) (store.UpdateEventParams, error) {
	reminders, err := encodeReminders(f.Reminders)
	if err != nil {
		return store.UpdateEventParams{}, err
	}
	frequency, interval, until := recurrenceColumns(f.Recurrence)
	return store.UpdateEventParams{
		Title: f.Title, Description: f.Description, Location: f.Location, AllDay: f.AllDay,
		StartAt: timestamp(f.StartAt), EndAt: timestamp(f.EndAt), StartDate: datePointer(f.StartDate), EndDate: datePointer(f.EndDate),
		TimeZone: f.TimeZone, RecurrenceFrequency: frequency, RecurrenceInterval: interval, RecurrenceUntil: until,
		Reminders: reminders, Status: string(f.Status), CancelledAt: timestamp(f.CancelledAt),
		RemovedFromSourceAt: timestamp(f.RemovedFromSourceAt), ID: id, ExpectedUpdatedAt: expectedUpdatedAt,
	}, nil
}

// event renders a stored event. Instants are returned in UTC.
func event(value store.Event) (generated.CalendarEvent, error) {
	fields, err := fieldsFromStore(value)
	if err != nil {
		return generated.CalendarEvent{}, err
	}
	var external *generated.ExternalReference
	if value.ExternalProvider != nil && value.ExternalID != nil {
		external = &generated.ExternalReference{Provider: *value.ExternalProvider, ExternalId: *value.ExternalID}
	}
	source := generated.EventSource{Kind: generated.EventSourceKind(value.SourceKind), ExternalUid: value.ExternalUid}
	if value.SourceID.Valid {
		id := uuid(value.SourceID)
		source.SourceId = &id
	}
	return generated.CalendarEvent{
		Id: uuid(value.ID), Title: fields.Title, Description: fields.Description, Location: fields.Location,
		AllDay: fields.AllDay, StartAt: fields.StartAt, EndAt: fields.EndAt, StartDate: fields.StartDate, EndDate: fields.EndDate,
		TimeZone: fields.TimeZone, Recurrence: fields.Recurrence, Reminders: fields.Reminders,
		Status: fields.Status, CancelledAt: fields.CancelledAt, RemovedFromSourceAt: fields.RemovedFromSourceAt,
		Source: source, ExternalReference: external,
		CreatedAt: value.CreatedAt.Time.UTC(), UpdatedAt: value.UpdatedAt.Time.UTC(),
	}, nil
}

// storedReminder is the persisted form of a Reminder, independent of the
// generated API type so the stored JSON stays stable.
type storedReminder struct {
	Kind          string  `json:"kind"`
	MinutesBefore *int    `json:"minutesBefore,omitempty"`
	DaysBefore    *int    `json:"daysBefore,omitempty"`
	LocalTime     *string `json:"localTime,omitempty"`
}

func validateReminder(value generated.Reminder) error {
	switch value.Kind {
	case generated.Relative:
		if value.MinutesBefore == nil || value.DaysBefore != nil || value.LocalTime != nil {
			return errors.New("a relative reminder takes minutesBefore only")
		}
		if *value.MinutesBefore < 0 || *value.MinutesBefore > 40320 {
			return errors.New("minutesBefore must be between 0 and 40320 (28 days)")
		}
	case generated.LocalTime:
		if value.DaysBefore == nil || value.LocalTime == nil || value.MinutesBefore != nil {
			return errors.New("a localTime reminder takes daysBefore and localTime only")
		}
		if *value.DaysBefore < 0 || *value.DaysBefore > 28 {
			return errors.New("daysBefore must be between 0 and 28")
		}
		if !validWallClock(*value.LocalTime) {
			return errors.New("localTime must be a 24-hour HH:MM time such as 19:00")
		}
	default:
		return errors.New("reminder kind must be relative or localTime")
	}
	return nil
}

// validWallClock accepts exactly HH:MM, 00:00 to 23:59.
func validWallClock(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	parsed, err := time.Parse("15:04", value)
	return err == nil && parsed.Format("15:04") == value
}

func encodeReminders(value *[]generated.Reminder) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	stored := make([]storedReminder, 0, len(*value))
	for _, reminder := range *value {
		stored = append(stored, storedReminder{Kind: string(reminder.Kind), MinutesBefore: reminder.MinutesBefore, DaysBefore: reminder.DaysBefore, LocalTime: reminder.LocalTime})
	}
	return json.Marshal(stored)
}

func decodeReminders(raw []byte) (*[]generated.Reminder, error) {
	if raw == nil {
		return nil, nil
	}
	var stored []storedReminder
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode stored reminders: %w", err)
	}
	reminders := make([]generated.Reminder, 0, len(stored))
	for _, reminder := range stored {
		reminders = append(reminders, generated.Reminder{Kind: generated.ReminderKind(reminder.Kind), MinutesBefore: reminder.MinutesBefore, DaysBefore: reminder.DaysBefore, LocalTime: reminder.LocalTime})
	}
	return &reminders, nil
}

func recurrenceColumns(value *generated.Recurrence) (*string, *int32, pgtype.Timestamptz) {
	if value == nil {
		return nil, nil, pgtype.Timestamptz{}
	}
	frequency := string(value.Frequency)
	interval := int32(value.Interval)
	return &frequency, &interval, timestamp(value.Until)
}

func recurrenceOutput(value store.Event) *generated.Recurrence {
	if value.RecurrenceFrequency == nil || value.RecurrenceInterval == nil {
		return nil
	}
	return &generated.Recurrence{Frequency: generated.RecurrenceFrequency(*value.RecurrenceFrequency), Interval: int(*value.RecurrenceInterval), Until: nullableTime(value.RecurrenceUntil)}
}

func uuid(value pgtype.UUID) types.UUID   { return types.UUID(value.Bytes) }
func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }

func timestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

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
	out := value.Time.UTC()
	return &out
}

func nullableDate(value pgtype.Date) *types.Date {
	if !value.Valid {
		return nil
	}
	out := types.Date{Time: value.Time}
	return &out
}
