// Package sources holds the shape of one event of a source as Calendar's
// integration API takes it (a SourceEvent), in the form the Google mapping
// produces it. It is a copy of the item types of Calendar's own sources
// package from before the Google sync moved out of Calendar (P11-07), so the
// mapping kept its code; Calendar itself is reached only through its API.
package sources

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/ics"
)

// Item is one event of a Google calendar, ready to send to Calendar: a
// single event or a series with its occurrences' own changes, with the
// reminders and priority Google gives it.
type Item struct {
	ics.Item
	// Recurrence is a series' rule; nil for a single event.
	Recurrence *Recurrence
	// Exceptions are a series' occurrence changes and skips, by recurrence
	// id: the occurrence's original start, an RFC 3339 instant in UTC for a
	// timed series, a date (YYYY-MM-DD) for an all-day one.
	Exceptions map[string]Exception
	// Reminders are the source's reminders for the event. nil leaves the
	// event's reminders to the owner.
	Reminders *Reminders
	// Priority is the event's own priority as the source gives it: low,
	// medium, high, or "" for none (the source's default). nil leaves the
	// event's priority to the owner (a Google event without Life OS's
	// priority property).
	Priority *string
	// Expanded marks one occurrence of a rule Calendar's model cannot hold
	// (or a lone changed instance), sent as an event of its own: it can only
	// be changed in Google.
	Expanded bool
}

// Recurrence is a series' rule in Calendar's model (the API's Recurrence):
// every Interval days, weeks, months, or years from the start, a weekly rule
// on each of ByWeekday (MO to SU, in week order), ending after Count
// occurrences (0: no count) or at Until (inclusive; an all-day series
// compares its dates with Until's date in the owner's timezone), or never.
type Recurrence struct {
	Frequency string
	Interval  int
	ByWeekday []string
	Count     int
	Until     *time.Time
}

// Reminder is a reminder as Calendar's API has it.
type Reminder struct {
	Kind          string  `json:"kind"`
	MinutesBefore *int    `json:"minutesBefore,omitempty"`
	DaysBefore    *int    `json:"daysBefore,omitempty"`
	LocalTime     *string `json:"localTime,omitempty"`
}

// Relative is a reminder minutes before the event starts.
func Relative(minutes int) Reminder {
	return Reminder{Kind: "relative", MinutesBefore: &minutes}
}

// Reminders are the reminders a source gives an event: whether it follows
// the source's default reminders, and its own on top.
type Reminders struct {
	UseDefault bool       `json:"useDefault"`
	Reminders  []Reminder `json:"reminders"`
}

// Equal reports whether both say the same reminders, in the same order.
func (r Reminders) Equal(other Reminders) bool {
	return r.UseDefault == other.UseDefault && slices.EqualFunc(r.Reminders, other.Reminders, SameReminder)
}

// SameReminder reports whether two reminders are the same.
func SameReminder(a, b Reminder) bool {
	sameInt := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	sameText := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return a.Kind == b.Kind && sameInt(a.MinutesBefore, b.MinutesBefore) && sameInt(a.DaysBefore, b.DaysBefore) && sameText(a.LocalTime, b.LocalTime)
}

// Exception is one occurrence's own changes, or its skip.
type Exception struct {
	Changes   Changes
	Cancelled bool
}

// Text is an occurrence's own value of a nullable text field: Value nil
// clears it for the occurrence.
type Text struct{ Value *string }

// Changes are the fields one occurrence overrides; nil fields follow the
// series. Their JSON is Calendar's OccurrenceChanges.
type Changes struct {
	Title               *string
	Description         *Text
	Location            *Text
	StartAt, EndAt      *time.Time
	StartDate, EndDate  *time.Time
	Reminders           *[]Reminder
	UseDefaultReminders *bool
	// Priority is the occurrence's own priority (low, medium, high).
	Priority *string
}

// Empty reports whether the occurrence follows its series entirely.
func (c Changes) Empty() bool {
	return c.Title == nil && c.Description == nil && c.Location == nil && c.StartAt == nil && c.StartDate == nil &&
		c.Reminders == nil && c.UseDefaultReminders == nil && c.Priority == nil
}

// JSON is Calendar's form: only the overridden fields, description or
// location null when cleared, times in UTC, dates as YYYY-MM-DD.
func (c Changes) JSON() ([]byte, error) {
	out := map[string]any{}
	if c.Title != nil {
		out["title"] = *c.Title
	}
	if c.Description != nil {
		out["description"] = c.Description.Value
	}
	if c.Location != nil {
		out["location"] = c.Location.Value
	}
	if c.StartAt != nil && c.EndAt != nil {
		out["startAt"], out["endAt"] = c.StartAt.UTC(), c.EndAt.UTC()
	}
	if c.StartDate != nil && c.EndDate != nil {
		out["startDate"], out["endDate"] = c.StartDate.Format(time.DateOnly), c.EndDate.Format(time.DateOnly)
	}
	if c.Reminders != nil {
		list := *c.Reminders
		if list == nil {
			list = []Reminder{}
		}
		out["reminders"] = list
	}
	if c.UseDefaultReminders != nil {
		out["useDefaultReminders"] = *c.UseDefaultReminders
	}
	if c.Priority != nil {
		out["priority"] = *c.Priority
	}
	return json.Marshal(out)
}

// Counts summarizes a sync of a calendar into Calendar.
type Counts struct {
	FeedEvents int `json:"feedEvents"`
	Imported   int `json:"imported"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Removed    int `json:"removed"`
	Restored   int `json:"restored"`
	Unchanged  int `json:"unchanged"`
	Skipped    int `json:"skipped"`
}

// Changed reports whether the sync changed any event in Calendar.
func (c Counts) Changed() bool {
	return c.Inserted+c.Updated+c.Removed+c.Restored > 0
}
