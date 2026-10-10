package sources

import (
	"encoding/json"
	"reflect"
	"slices"
	"time"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Event is the part of an item every source gives: one event with its
// identity in the source, as its integration sends it.
type Event struct {
	// ExternalUID is the event's identity in its source.
	ExternalUID string
	Title       string
	Description *string
	Location    *string
	AllDay      bool
	// StartAt/EndAt are a timed event's instants (UTC).
	StartAt, EndAt time.Time
	// StartDate/EndDate are an all-day event's first and last (inclusive)
	// days, at UTC midnight.
	StartDate, EndDate time.Time
	// TimeZone is the IANA zone a timed event is scheduled in, when known.
	TimeZone *string
	// Cancelled: the source cancelled the event.
	Cancelled  bool
	Categories []string
}

// Start is the event's start instant; an all-day event starts at midnight
// of its first day in loc.
func (e Event) Start(loc *time.Location) time.Time {
	if e.AllDay {
		return time.Date(e.StartDate.Year(), e.StartDate.Month(), e.StartDate.Day(), 0, 0, 0, 0, loc)
	}
	return e.StartAt
}

// Item is one event of a source, ready to reconcile, as an integration
// syncs it: a single event or a series with its occurrences' own changes,
// with the reminders and priority the source gives it, if any.
type Item struct {
	Event
	// Recurrence is a series' rule; nil for a single event.
	Recurrence *Recurrence
	// Exceptions are a series' occurrence changes and skips, by recurrence
	// id: the occurrence's original start, an RFC 3339 instant in UTC for a
	// timed series, a date (YYYY-MM-DD) for an all-day one.
	Exceptions map[string]Exception
	// Reminders are the source's reminders for the event. nil leaves the
	// event's reminders to the owner (a feed carries none).
	Reminders *Reminders
	// Priority is the event's own priority as the source gives it: low,
	// medium, high, or "" for none (the source's default). nil leaves the
	// event's priority to the owner (a source that gives none, such as a
	// feed).
	Priority *string
	// ReadOnlyReason, set by an integration for an event of a source that
	// accepts changes, makes it read-only in Life OS and says why (such as
	// one occurrence of a rule Calendar cannot hold).
	ReadOnlyReason *string
}

// ownPriority is the priority to store for item: none when the source
// gives none or its default (defaultPriority).
func ownPriority(item Item, defaultPriority string) *string {
	if item.Priority == nil || *item.Priority == "" || *item.Priority == defaultPriority {
		return nil
	}
	switch *item.Priority {
	case "low", "medium", "high":
		value := *item.Priority
		return &value
	}
	return nil
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

// Reminder is a reminder as Calendar stores it (the API's Reminder).
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

func (r Reminders) encode() ([]byte, []byte, error) {
	list := r.Reminders
	if list == nil {
		list = []Reminder{}
	}
	own, err := json.Marshal(list)
	if err != nil {
		return nil, nil, err
	}
	whole, err := json.Marshal(Reminders{UseDefault: r.UseDefault, Reminders: list})
	return own, whole, err
}

// Equal reports whether both say the same reminders, in the same order.
func (r Reminders) Equal(other Reminders) bool {
	return r.UseDefault == other.UseDefault && slices.EqualFunc(r.Reminders, other.Reminders, sameReminder)
}

func sameReminder(a, b Reminder) bool {
	sameInt := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	sameText := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return a.Kind == b.Kind && sameInt(a.MinutesBefore, b.MinutesBefore) && sameInt(a.DaysBefore, b.DaysBefore) && sameText(a.LocalTime, b.LocalTime)
}

// decodeReminders reads a stored reminder list.
func decodeReminders(raw []byte) []Reminder {
	var list []Reminder
	_ = json.Unmarshal(raw, &list)
	return list
}

// sourceReminders is what the source last gave the event, if anything.
func sourceReminders(event store.Event) (Reminders, bool) {
	if event.SourceReminders == nil {
		return Reminders{}, false
	}
	var stored Reminders
	if err := json.Unmarshal(event.SourceReminders, &stored); err != nil {
		return Reminders{}, false
	}
	return stored, true
}

// followsSource reports whether the event's reminders are still the ones
// its source gave it: the owner has not changed them.
func followsSource(event store.Event) bool {
	stored, ok := sourceReminders(event)
	return ok && stored.Equal(Reminders{UseDefault: event.UseDefaultReminders, Reminders: decodeReminders(event.Reminders)})
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
// series. They are stored as event_exceptions.changes, in the API's form
// (see the api package's occurrence exceptions).
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

// JSON is the stored form: only the overridden fields, description or
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

// sameChanges compares two stored change objects by content.
func sameChanges(a, b []byte) bool {
	var left, right map[string]any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return reflect.DeepEqual(left, right)
}
