package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// One occurrence of a recurring event can have its own changes or be
// skipped (This event). They are stored per occurrence, keyed by its
// recurrence id (its original start), as the fields it overrides; every
// other field follows the series.

// optionalText is an overridable nullable text field: set says the
// occurrence overrides it, value nil clears it for the occurrence.
type optionalText struct {
	set   bool
	value *string
}

// occurrenceChanges are one occurrence's own changes; nil and unset fields
// follow the series. A move sets both ends: startAt and endAt for a timed
// series, startDate and endDate for an all-day one.
type occurrenceChanges struct {
	Title               *string
	Description         optionalText
	Location            optionalText
	StartAt, EndAt      *time.Time
	StartDate, EndDate  *types.Date
	Reminders           *[]generated.Reminder
	UseDefaultReminders *bool
}

// empty reports whether the occurrence follows its series entirely.
func (c occurrenceChanges) empty() bool {
	return c.Title == nil && !c.Description.set && !c.Location.set && !c.moves() && c.Reminders == nil && c.UseDefaultReminders == nil
}

// moves reports whether the occurrence has its own start and end.
func (c occurrenceChanges) moves() bool {
	return c.StartAt != nil || c.StartDate != nil
}

// storedChanges is the persisted JSON form; pointers to json.RawMessage keep
// an explicit null apart from an absent field.
type storedChanges struct {
	Title               *string           `json:"title,omitempty"`
	Description         *json.RawMessage  `json:"description,omitempty"`
	Location            *json.RawMessage  `json:"location,omitempty"`
	StartAt             *time.Time        `json:"startAt,omitempty"`
	EndAt               *time.Time        `json:"endAt,omitempty"`
	StartDate           *string           `json:"startDate,omitempty"`
	EndDate             *string           `json:"endDate,omitempty"`
	Reminders           *[]storedReminder `json:"reminders,omitempty"`
	UseDefaultReminders *bool             `json:"useDefaultReminders,omitempty"`
}

func (c occurrenceChanges) encode() ([]byte, error) {
	var stored storedChanges
	stored.Title = c.Title
	text := func(value optionalText) (*json.RawMessage, error) {
		if !value.set {
			return nil, nil
		}
		raw, err := json.Marshal(value.value)
		if err != nil {
			return nil, err
		}
		message := json.RawMessage(raw)
		return &message, nil
	}
	var err error
	if stored.Description, err = text(c.Description); err != nil {
		return nil, err
	}
	if stored.Location, err = text(c.Location); err != nil {
		return nil, err
	}
	if c.StartAt != nil && c.EndAt != nil {
		start, end := c.StartAt.UTC(), c.EndAt.UTC()
		stored.StartAt, stored.EndAt = &start, &end
	}
	if c.StartDate != nil && c.EndDate != nil {
		first, last := c.StartDate.Format(time.DateOnly), c.EndDate.Format(time.DateOnly)
		stored.StartDate, stored.EndDate = &first, &last
	}
	if c.Reminders != nil {
		reminders := make([]storedReminder, 0, len(*c.Reminders))
		for _, reminder := range *c.Reminders {
			reminders = append(reminders, storedReminder{Kind: string(reminder.Kind), MinutesBefore: reminder.MinutesBefore, DaysBefore: reminder.DaysBefore, LocalTime: reminder.LocalTime})
		}
		stored.Reminders = &reminders
	}
	stored.UseDefaultReminders = c.UseDefaultReminders
	return json.Marshal(stored)
}

func decodeChanges(raw []byte) (occurrenceChanges, error) {
	var stored storedChanges
	if err := json.Unmarshal(raw, &stored); err != nil {
		return occurrenceChanges{}, fmt.Errorf("decode occurrence changes: %w", err)
	}
	// A null decodes to a nil pointer, so presence is read separately.
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return occurrenceChanges{}, fmt.Errorf("decode occurrence changes: %w", err)
	}
	changes := occurrenceChanges{Title: stored.Title, StartAt: stored.StartAt, EndAt: stored.EndAt, UseDefaultReminders: stored.UseDefaultReminders}
	text := func(name string) (optionalText, error) {
		value, ok := present[name]
		if !ok {
			return optionalText{}, nil
		}
		var text *string
		if err := json.Unmarshal(value, &text); err != nil {
			return optionalText{}, fmt.Errorf("decode occurrence changes: %w", err)
		}
		return optionalText{set: true, value: text}, nil
	}
	var err error
	if changes.Description, err = text("description"); err != nil {
		return occurrenceChanges{}, err
	}
	if changes.Location, err = text("location"); err != nil {
		return occurrenceChanges{}, err
	}
	if (changes.StartAt == nil) != (changes.EndAt == nil) {
		return occurrenceChanges{}, fmt.Errorf("decode occurrence changes: a move needs startAt and endAt")
	}
	if stored.StartDate != nil && stored.EndDate != nil {
		first, err := time.Parse(time.DateOnly, *stored.StartDate)
		if err != nil {
			return occurrenceChanges{}, fmt.Errorf("decode occurrence changes: %w", err)
		}
		last, err := time.Parse(time.DateOnly, *stored.EndDate)
		if err != nil {
			return occurrenceChanges{}, fmt.Errorf("decode occurrence changes: %w", err)
		}
		changes.StartDate, changes.EndDate = &types.Date{Time: first}, &types.Date{Time: last}
	}
	if stored.Reminders != nil {
		reminders := make([]generated.Reminder, 0, len(*stored.Reminders))
		for _, reminder := range *stored.Reminders {
			reminders = append(reminders, generated.Reminder{Kind: generated.ReminderKind(reminder.Kind), MinutesBefore: reminder.MinutesBefore, DaysBefore: reminder.DaysBefore, LocalTime: reminder.LocalTime})
		}
		changes.Reminders = &reminders
	}
	return changes, nil
}

// applyTo is fields (an occurrence as its series has it) with the changes.
func (c occurrenceChanges) applyTo(fields eventFields) eventFields {
	if c.Title != nil {
		fields.Title = *c.Title
	}
	if c.Description.set {
		fields.Description = c.Description.value
	}
	if c.Location.set {
		fields.Location = c.Location.value
	}
	if c.StartAt != nil && c.EndAt != nil && !fields.AllDay {
		fields.StartAt, fields.EndAt = c.StartAt, c.EndAt
	}
	if c.StartDate != nil && c.EndDate != nil && fields.AllDay {
		fields.StartDate, fields.EndDate = c.StartDate, c.EndDate
	}
	if c.Reminders != nil {
		fields.Reminders = *c.Reminders
	}
	if c.UseDefaultReminders != nil {
		fields.UseDefaultReminders = *c.UseDefaultReminders
	}
	return fields
}

// diffOccurrence is what next (the occurrence after a change) overrides of
// natural (the occurrence as its series has it): a field set back to the
// series' value is no change of its own.
func diffOccurrence(natural, next eventFields) occurrenceChanges {
	var changes occurrenceChanges
	if next.Title != natural.Title {
		title := next.Title
		changes.Title = &title
	}
	if !sameString(next.Description, natural.Description) {
		changes.Description = optionalText{set: true, value: next.Description}
	}
	if !sameString(next.Location, natural.Location) {
		changes.Location = optionalText{set: true, value: next.Location}
	}
	if natural.AllDay {
		if !next.StartDate.Time.Equal(natural.StartDate.Time) || !next.EndDate.Time.Equal(natural.EndDate.Time) {
			changes.StartDate, changes.EndDate = next.StartDate, next.EndDate
		}
	} else if !next.StartAt.Equal(*natural.StartAt) || !next.EndAt.Equal(*natural.EndAt) {
		changes.StartAt, changes.EndAt = next.StartAt, next.EndAt
	}
	if !slices.EqualFunc(next.Reminders, natural.Reminders, sameReminder) {
		reminders := slices.Clone(next.Reminders)
		changes.Reminders = &reminders
	}
	if next.UseDefaultReminders != natural.UseDefaultReminders {
		use := next.UseDefaultReminders
		changes.UseDefaultReminders = &use
	}
	return changes
}

// without drops the changes to the fields a patch names: a series change
// made from this occurrence replaces its own changes to those fields.
func (c occurrenceChanges) without(present map[string]bool) occurrenceChanges {
	if present["title"] {
		c.Title = nil
	}
	if present["description"] {
		c.Description = optionalText{}
	}
	if present["location"] {
		c.Location = optionalText{}
	}
	for _, name := range []string{"allDay", "startAt", "endAt", "startDate", "endDate", "timeZone"} {
		if present[name] {
			c.StartAt, c.EndAt, c.StartDate, c.EndDate = nil, nil, nil, nil
		}
	}
	if present["reminders"] {
		c.Reminders = nil
	}
	if present["reminders"] || present["useDefaultReminders"] {
		c.UseDefaultReminders = nil
	}
	return c
}

// exception is one stored occurrence exception.
type exception struct {
	key         string
	changes     occurrenceChanges
	cancelledAt *time.Time
	updatedAt   time.Time
}

func exceptionFromStore(row store.EventException) (*exception, error) {
	changes, err := decodeChanges(row.Changes)
	if err != nil {
		return nil, err
	}
	return &exception{key: row.RecurrenceID, changes: changes, cancelledAt: nullableTime(row.CancelledAt), updatedAt: row.UpdatedAt.Time.UTC()}, nil
}

// exceptionsBySeries groups stored exceptions by series and recurrence id.
func exceptionsBySeries(rows []store.EventException) (map[[16]byte]map[string]*exception, error) {
	out := map[[16]byte]map[string]*exception{}
	for _, row := range rows {
		item, err := exceptionFromStore(row)
		if err != nil {
			return nil, err
		}
		id := row.EventID.Bytes
		if out[id] == nil {
			out[id] = map[string]*exception{}
		}
		out[id][item.key] = item
	}
	return out, nil
}
