package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/config"
	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// maxReminders bounds an event's own reminders and each default list.
const maxReminders = 10

// defaultPriority is the priority of a Life OS event without one of its own,
// and of a source's events unless the source sets another.
const defaultPriority = generated.Medium

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
	Reminders           []generated.Reminder // the event's own, never nil
	UseDefaultReminders bool                 // the defaults apply as well
	// Priority is the event's own priority; nil follows its default (see
	// withPriorityDefault).
	Priority            *generated.EventPriority
	Status              generated.EventStatus
	CancelledAt         *time.Time
	RemovedFromSourceAt *time.Time
}

func fieldsFromCreate(body generated.CreateEventRequest) eventFields {
	return eventFields{
		Title: body.Title, Description: body.Description, Location: body.Location, AllDay: body.AllDay,
		StartAt: body.StartAt, EndAt: body.EndAt, StartDate: body.StartDate, EndDate: body.EndDate,
		TimeZone: body.TimeZone, Recurrence: body.Recurrence, Reminders: remindersOrNone(body.Reminders),
		UseDefaultReminders: body.UseDefaultReminders == nil || *body.UseDefaultReminders, Priority: body.Priority, Status: generated.Active,
	}
}

// remindersOrNone is value, or no reminders when value is nil.
func remindersOrNone(value *[]generated.Reminder) []generated.Reminder {
	if value == nil {
		return []generated.Reminder{}
	}
	return *value
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
		TimeZone: value.TimeZone, Recurrence: recurrenceOutput(value), Reminders: remindersOrNone(reminders),
		UseDefaultReminders: value.UseDefaultReminders, Priority: priorityFromStore(value.Priority),
		Status: generated.EventStatus(value.Status), CancelledAt: nullableTime(value.CancelledAt),
		RemovedFromSourceAt: nullableTime(value.RemovedFromSourceAt),
	}, nil
}

// validate checks the event as a whole. owner is the owner's timezone, in
// which a timed event without its own timeZone repeats.
func (f eventFields) validate(owner *time.Location) error {
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
		if err := f.validateRecurrence(owner); err != nil {
			return err
		}
	}
	if err := validateReminders(f.Reminders, "an event takes at most %d reminders of its own"); err != nil {
		return err
	}
	if f.Priority != nil && !f.Priority.Valid() {
		return errors.New("priority must be low, medium, or high")
	}
	if !f.Status.Valid() {
		return errors.New("status must be active or cancelled")
	}
	return nil
}

// withPriorityDefault is f with its own priority dropped when it is the
// event's default (fallback): an event keeps a priority of its own only
// while it differs, so it follows later changes of its default.
func (f eventFields) withPriorityDefault(fallback generated.EventPriority) eventFields {
	if f.Priority != nil && *f.Priority == fallback {
		f.Priority = nil
	}
	return f
}

// priorityOr is value, else fallback.
func priorityOr(value *generated.EventPriority, fallback generated.EventPriority) generated.EventPriority {
	if value == nil {
		return fallback
	}
	return *value
}

func priorityFromStore(value *string) *generated.EventPriority {
	if value == nil {
		return nil
	}
	priority := generated.EventPriority(*value)
	return &priority
}

func priorityColumn(value *generated.EventPriority) *string {
	if value == nil {
		return nil
	}
	priority := string(*value)
	return &priority
}

// maxRecurrenceCount bounds a rule that ends after a number of occurrences.
const maxRecurrenceCount = 1000

// validateRecurrence checks the rule against the event it repeats: weekdays
// only for a weekly rule and including the start's weekday (in the event's
// timezone, else owner; an all-day event's start date), a count or an until
// but not both, and an until not before the start.
func (f eventFields) validateRecurrence(owner *time.Location) error {
	rule := f.Recurrence
	if !rule.Frequency.Valid() {
		return errors.New("recurrence frequency must be daily, weekly, monthly, or yearly")
	}
	if rule.Interval < 1 {
		return errors.New("recurrence interval must be at least 1")
	}
	if rule.ByWeekday != nil && len(*rule.ByWeekday) > 0 {
		if rule.Frequency != generated.Weekly {
			return errors.New("recurrence byWeekday is only for weekly recurrence")
		}
		codes := make([]string, 0, len(*rule.ByWeekday))
		for _, day := range *rule.ByWeekday {
			if !day.Valid() {
				return errors.New("recurrence byWeekday days must be MO, TU, WE, TH, FR, SA, or SU")
			}
			codes = append(codes, string(day))
		}
		start := f.startWeekday(owner)
		if !slices.Contains(codes, recurrence.WeekdayCode(start)) {
			return fmt.Errorf("recurrence byWeekday must include the weekday the event starts on (%s)", start)
		}
	}
	if rule.Count != nil {
		if *rule.Count < 1 || *rule.Count > maxRecurrenceCount {
			return fmt.Errorf("recurrence count must be between 1 and %d", maxRecurrenceCount)
		}
		if rule.Until != nil {
			return errors.New("recurrence takes until or count, not both")
		}
	}
	if rule.Until != nil {
		before := !f.AllDay && rule.Until.Before(*f.StartAt)
		if f.AllDay {
			// An all-day series compares its dates with until's date in the
			// owner's timezone.
			before = untilDate(*rule.Until, owner).Before(f.StartDate.Time)
		}
		if before {
			return errors.New("recurrence until must not be before the event starts")
		}
	}
	return nil
}

// startWeekday is the weekday the event starts on: a timed event's in its
// timeZone (else owner), an all-day event's start date's.
func (f eventFields) startWeekday(owner *time.Location) time.Weekday {
	if f.AllDay {
		return f.StartDate.Time.Weekday()
	}
	return f.StartAt.In(f.clock(owner)).Weekday()
}

// clock is the timezone a timed event repeats in: its own, else owner.
func (f eventFields) clock(owner *time.Location) *time.Location {
	if f.TimeZone != nil {
		if loc, err := time.LoadLocation(*f.TimeZone); err == nil {
			return loc
		}
	}
	if owner == nil {
		return time.UTC
	}
	return owner
}

// untilDate is the date of an all-day series' until in the owner's
// timezone, at midnight UTC like stored dates.
func untilDate(until time.Time, owner *time.Location) time.Time {
	if owner == nil {
		owner = time.UTC
	}
	local := until.In(owner)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
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
	reminders, err := encodeReminders(&f.Reminders)
	if err != nil {
		return store.CreateEventParams{}, err
	}
	rule := recurrenceColumns(f.Recurrence)
	return store.CreateEventParams{
		Title: f.Title, Description: f.Description, Location: f.Location, AllDay: f.AllDay,
		StartAt: timestamp(f.StartAt), EndAt: timestamp(f.EndAt), StartDate: datePointer(f.StartDate), EndDate: datePointer(f.EndDate),
		TimeZone: f.TimeZone, RecurrenceFrequency: rule.frequency, RecurrenceInterval: rule.interval, RecurrenceUntil: rule.until,
		RecurrenceByWeekday: rule.byWeekday, RecurrenceCount: rule.count,
		Reminders: reminders, UseDefaultReminders: f.UseDefaultReminders, Status: string(f.Status), CancelledAt: timestamp(f.CancelledAt),
		SourceKind: string(generated.EventSourceKindManual), RemovedFromSourceAt: timestamp(f.RemovedFromSourceAt), Priority: priorityColumn(f.Priority),
	}, nil
}

func (f eventFields) updateParams(id pgtype.UUID, expectedUpdatedAt pgtype.Timestamptz) (store.UpdateEventParams, error) {
	reminders, err := encodeReminders(&f.Reminders)
	if err != nil {
		return store.UpdateEventParams{}, err
	}
	rule := recurrenceColumns(f.Recurrence)
	return store.UpdateEventParams{
		Title: f.Title, Description: f.Description, Location: f.Location, AllDay: f.AllDay,
		StartAt: timestamp(f.StartAt), EndAt: timestamp(f.EndAt), StartDate: datePointer(f.StartDate), EndDate: datePointer(f.EndDate),
		TimeZone: f.TimeZone, RecurrenceFrequency: rule.frequency, RecurrenceInterval: rule.interval, RecurrenceUntil: rule.until,
		RecurrenceByWeekday: rule.byWeekday, RecurrenceCount: rule.count,
		Reminders: reminders, UseDefaultReminders: f.UseDefaultReminders, Status: string(f.Status), CancelledAt: timestamp(f.CancelledAt),
		RemovedFromSourceAt: timestamp(f.RemovedFromSourceAt), Priority: priorityColumn(f.Priority), ID: id, ExpectedUpdatedAt: expectedUpdatedAt,
	}, nil
}

// event renders a stored event. Instants are returned in UTC. Its source
// name, default and effective reminders, and default priority are left to
// resolveSources, and so is its priority when it has none of its own (empty
// until then).
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
		UseDefaultReminders: fields.UseDefaultReminders, DefaultReminders: []generated.Reminder{}, EffectiveReminders: []generated.Reminder{},
		Priority: priorityOr(fields.Priority, ""), Status: fields.Status, CancelledAt: fields.CancelledAt, RemovedFromSourceAt: fields.RemovedFromSourceAt,
		Source: source, ExternalReference: external, ReadOnlyReason: value.ReadOnlyReason,
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

// validateReminders checks a reminder list: at most maxReminders (tooMany
// formats the error with the limit), each one valid.
func validateReminders(list []generated.Reminder, tooMany string) error {
	if len(list) > maxReminders {
		return fmt.Errorf(tooMany, maxReminders)
	}
	for _, reminder := range list {
		if err := validateReminder(reminder); err != nil {
			return err
		}
	}
	return nil
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

// ruleColumns are a Recurrence's stored columns, all null for none.
type ruleColumns struct {
	frequency *string
	interval  *int32
	until     pgtype.Timestamptz
	byWeekday []string // in week order, each day once
	count     *int32
}

func recurrenceColumns(value *generated.Recurrence) ruleColumns {
	if value == nil {
		return ruleColumns{}
	}
	frequency := string(value.Frequency)
	interval := int32(value.Interval)
	columns := ruleColumns{frequency: &frequency, interval: &interval, until: timestamp(value.Until)}
	if value.ByWeekday != nil && len(*value.ByWeekday) > 0 {
		codes := make([]string, 0, len(*value.ByWeekday))
		for _, day := range *value.ByWeekday {
			codes = append(codes, string(day))
		}
		if normalized, ok := recurrence.NormalizeWeekdays(codes); ok {
			codes = normalized
		}
		columns.byWeekday = codes
	}
	if value.Count != nil {
		count := int32(*value.Count)
		columns.count = &count
	}
	return columns
}

func recurrenceOutput(value store.Event) *generated.Recurrence {
	if value.RecurrenceFrequency == nil || value.RecurrenceInterval == nil {
		return nil
	}
	rule := &generated.Recurrence{Frequency: generated.RecurrenceFrequency(*value.RecurrenceFrequency), Interval: int(*value.RecurrenceInterval), Until: nullableTime(value.RecurrenceUntil)}
	if len(value.RecurrenceByWeekday) > 0 {
		days := make([]generated.Weekday, 0, len(value.RecurrenceByWeekday))
		for _, code := range value.RecurrenceByWeekday {
			days = append(days, generated.Weekday(code))
		}
		rule.ByWeekday = &days
	}
	if value.RecurrenceCount != nil {
		count := int(*value.RecurrenceCount)
		rule.Count = &count
	}
	return rule
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
