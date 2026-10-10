package api

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

func storedTimed(title, start string, minutes int) store.Event {
	begin, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	return store.Event{
		Title: title, Status: "active", SourceKind: "manual", Reminders: []byte(`[]`), UseDefaultReminders: true,
		StartAt: pgtype.Timestamptz{Time: begin, Valid: true},
		EndAt:   pgtype.Timestamptz{Time: begin.Add(time.Duration(minutes) * time.Minute), Valid: true},
	}
}

func TestClassifyUpdate(t *testing.T) {
	base := storedTimed("Dentist", "2026-11-17T08:30:00Z", 45)
	reminders := base
	reminders.Reminders = []byte(`[{"kind":"relative","minutesBefore":30}]`)
	location := "Main St 1"
	frequency := "weekly"
	for _, test := range []struct {
		name        string
		change      func(*store.Event)
		wantType    string
		wantChanges string
	}{
		{"nothing changed", func(*store.Event) {}, "", ""},
		{"same instant in another zone", func(e *store.Event) {
			berlin, _ := time.LoadLocation("Europe/Berlin")
			e.StartAt.Time = e.StartAt.Time.In(berlin)
		}, "", ""},
		{"renamed", func(e *store.Event) { e.Title = "Dentist (Dr. Weber)" }, eventUpdated, "title"},
		{"location and reminders", func(e *store.Event) {
			e.Location = &location
			e.Reminders = []byte(`[{"kind":"relative","minutesBefore":180}]`)
		}, eventUpdated, "location reminders"},
		{"default reminders off", func(e *store.Event) { e.UseDefaultReminders = false }, eventUpdated, "reminders"},
		{"priority", func(e *store.Event) { e.Priority = ptr("high") }, eventUpdated, "priority"},
		{"repeats", func(e *store.Event) { e.RecurrenceFrequency = &frequency }, eventUpdated, "recurrence"},
		{"moved", func(e *store.Event) {
			e.StartAt.Time = e.StartAt.Time.Add(24 * time.Hour)
			e.EndAt.Time = e.EndAt.Time.Add(24 * time.Hour)
		}, eventRescheduled, "startAt endAt"},
		{"longer", func(e *store.Event) { e.EndAt.Time = e.EndAt.Time.Add(time.Hour) }, eventRescheduled, "endAt"},
		{"renamed and moved", func(e *store.Event) {
			e.Title = "Zahnarzt"
			e.StartAt.Time = e.StartAt.Time.Add(time.Hour)
		}, eventRescheduled, "title startAt"},
		{"cancelled", func(e *store.Event) { e.Status = "cancelled" }, eventCancelled, "status"},
		{"cancelled and moved", func(e *store.Event) {
			e.Status = "cancelled"
			e.StartAt.Time = e.StartAt.Time.Add(time.Hour)
		}, eventCancelled, "startAt status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := base
			test.change(&after)
			gotType, changes := classifyUpdate(base, after)
			if gotType != test.wantType || strings.Join(changes, " ") != test.wantChanges {
				t.Fatalf("classifyUpdate = %q %q, want %q %q", gotType, changes, test.wantType, test.wantChanges)
			}
		})
	}
	cancelled := base
	cancelled.Status = "cancelled"
	if gotType, _ := classifyUpdate(cancelled, base); gotType != eventRestored {
		t.Fatalf("restoring = %q", gotType)
	}
	if gotType, changes := classifyUpdate(reminders, base); gotType != eventUpdated || strings.Join(changes, " ") != "reminders" {
		t.Fatalf("back to default reminders = %q %q", gotType, changes)
	}
}

func TestEventSummary(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	timed := storedTimed("Dentist", "2026-11-17T08:30:00Z", 45)
	allDay := store.Event{
		Title: "Holiday", AllDay: true, Status: "active",
		StartDate: pgtype.Date{Time: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), Valid: true},
		EndDate:   pgtype.Date{Time: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	oneDay := allDay
	oneDay.Title, oneDay.EndDate = "Bioabfall", oneDay.StartDate
	weekly, every := "weekly", int32(2)
	recurring := timed
	recurring.Title, recurring.RecurrenceFrequency, recurring.RecurrenceInterval = "Standup", &weekly, &every
	for _, test := range []struct {
		eventType string
		item      store.Event
		changes   []string
		want      string
	}{
		{eventCreated, timed, nil, "Event “Dentist” created for Tue 17 Nov 2026, 09:30 CET"},
		{eventRescheduled, allDay, []string{"startDate"}, "Event “Holiday” moved to Sat 17 Oct 2026 – Mon 19 Oct 2026, all day"},
		{eventCancelled, oneDay, []string{"status"}, "Event “Bioabfall” on Sat 17 Oct 2026, all day cancelled"},
		{eventRestored, timed, []string{"status"}, "Event “Dentist” on Tue 17 Nov 2026, 09:30 CET restored"},
		{eventDeleted, recurring, nil, "Event “Standup” on Tue 17 Nov 2026, 09:30 CET, repeating every 2 weeks deleted"},
		{eventUpdated, timed, []string{"title", "location", "timeZone", "reminders"}, "Event “Dentist” updated: title, location, time zone, reminders"},
		{eventUpdated, timed, []string{"status"}, "Event “Dentist” updated"},
	} {
		if got := eventSummary(test.eventType, test.item, test.changes, berlin); got != test.want {
			t.Errorf("%s summary = %q, want %q", test.eventType, got, test.want)
		}
	}
	// Times are the owner's: the same instant in UTC.
	if got := eventSummary(eventCreated, timed, nil, nil); got != "Event “Dentist” created for Tue 17 Nov 2026, 08:30 UTC" {
		t.Errorf("UTC summary = %q", got)
	}
}

func TestEventDataKeepsNoPrivateDetails(t *testing.T) {
	item := storedTimed("Dentist", "2026-11-17T08:30:00Z", 45)
	description, location := "Bring the insurance card", "Main St 1"
	item.Description, item.Location = &description, &location
	item.Reminders = []byte(`[{"kind":"relative","minutesBefore":30}]`)
	data := eventData(item)
	if data["startAt"] != "2026-11-17T08:30:00Z" || data["endAt"] != "2026-11-17T09:15:00Z" || data["title"] != "Dentist" || data["sourceKind"] != "manual" {
		t.Fatalf("data = %v", data)
	}
	for _, key := range []string{"description", "location", "reminders"} {
		if _, ok := data[key]; ok {
			t.Errorf("data keeps %s: %v", key, data)
		}
	}
}
