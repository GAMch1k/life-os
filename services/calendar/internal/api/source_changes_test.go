package api

import (
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// A source writes occurrence exceptions (a synced Google series' changed
// instances) in the form the API reads as an occurrence's own changes.
func TestSourceOccurrenceChangesReadAsTheAPIs(t *testing.T) {
	title := "Standup (planning)"
	start, end := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	first, last := time.Date(2027, 11, 6, 0, 0, 0, 0, time.UTC), time.Date(2027, 11, 7, 0, 0, 0, 0, time.UTC)
	useDefaults := false
	reminders := []sources.Reminder{sources.Relative(5)}
	raw, err := sources.Changes{
		Title: &title, Description: &sources.Text{}, StartAt: &start, EndAt: &end, StartDate: &first, EndDate: &last,
		Reminders: &reminders, UseDefaultReminders: &useDefaults,
	}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := decodeChanges(raw)
	if err != nil {
		t.Fatal(err)
	}
	if *changes.Title != title || !changes.Description.set || changes.Description.value != nil || changes.Location.set ||
		!changes.StartAt.Equal(start) || !changes.EndAt.Equal(end) || changes.StartDate.Format(time.DateOnly) != "2027-11-06" ||
		changes.EndDate.Format(time.DateOnly) != "2027-11-07" || len(*changes.Reminders) != 1 || *(*changes.Reminders)[0].MinutesBefore != 5 ||
		*changes.UseDefaultReminders {
		t.Fatalf("decoded = %+v from %s", changes, raw)
	}
}
