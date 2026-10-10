package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

func occurrenceStarts(items []generated.CalendarEvent) []string {
	var out []string
	for _, item := range items {
		label := item.Title + " "
		if item.AllDay {
			label += item.StartDate.String()
		} else {
			label += item.StartAt.Format(time.RFC3339)
		}
		if item.Occurrence != nil {
			label += " *"
		}
		out = append(out, label)
	}
	return out
}

// Recurring events are listed as their occurrences in the range, in order
// among other events, each with the series' id and its own start and end.
func TestRecurringEventsAreListedAsOccurrences(t *testing.T) {
	c := newAPIClient(t)
	// Weekly at 18:00 Berlin: 16:00 UTC in summer time, 17:00 UTC after 25 October.
	yoga := c.event(http.MethodPost, "/v1/events", `{"title":"Yoga","allDay":false,"startAt":"2026-10-12T18:00:00+02:00","endAt":"2026-10-12T19:00:00+02:00","timeZone":"Europe/Berlin","recurrence":{"frequency":"weekly","interval":1}}`, http.StatusCreated)
	c.event(http.MethodPost, "/v1/events", `{"title":"Dentist","allDay":false,"startAt":"2026-10-20T08:00:00Z","endAt":"2026-10-20T09:00:00Z"}`, http.StatusCreated)
	c.event(http.MethodPost, "/v1/events", `{"title":"Anna","allDay":true,"startDate":"2020-10-24","endDate":"2020-10-24","recurrence":{"frequency":"yearly","interval":1}}`, http.StatusCreated)
	c.event(http.MethodPost, "/v1/events", `{"title":"Course","allDay":false,"startAt":"2026-10-13T07:00:00Z","endAt":"2026-10-13T08:00:00Z","recurrence":{"frequency":"daily","interval":7,"until":"2026-10-20T07:00:00Z"}}`, http.StatusCreated)
	window := "/v1/events?" + q("from", "2026-10-11T22:00:00Z", "to", "2026-11-02T23:00:00Z")
	equal(t, "occurrences", occurrenceStarts(c.list(window).Items), []string{
		"Yoga 2026-10-12T16:00:00Z *",
		"Course 2026-10-13T07:00:00Z *",
		"Yoga 2026-10-19T16:00:00Z *",
		"Course 2026-10-20T07:00:00Z *",
		"Dentist 2026-10-20T08:00:00Z",
		"Anna 2026-10-24 *",
		"Yoga 2026-10-26T17:00:00Z *",
		"Yoga 2026-11-02T17:00:00Z *",
	})
	items := c.list(window).Items
	first := items[0]
	if first.Id != yoga.Id || first.Occurrence.SeriesId != yoga.Id || first.Occurrence.RecurrenceId != "2026-10-12T16:00:00Z" || first.EndAt.Format(time.RFC3339) != "2026-10-12T17:00:00Z" || first.Recurrence == nil {
		t.Fatalf("first occurrence = %+v", first)
	}
	if anna := items[5]; anna.Occurrence.RecurrenceId != "2026-10-24" || anna.EndDate.String() != "2026-10-24" {
		t.Fatalf("all-day occurrence = %+v", anna)
	}
	// Paging one at a time returns the same sequence.
	paged := c.titles(window + "&limit=1")
	equal(t, "paged", paged, []string{"Yoga", "Course", "Yoga", "Course", "Dentist", "Anna", "Yoga", "Yoga"})
	paged3 := c.titles(window + "&limit=3")
	equal(t, "paged by 3", paged3, paged)

	// The stored event is unchanged and editable through its id.
	stored := c.event(http.MethodGet, "/v1/events/"+yoga.Id.String(), nil, http.StatusOK)
	if stored.Occurrence != nil || stored.StartAt.Format(time.RFC3339) != "2026-10-12T16:00:00Z" {
		t.Fatalf("stored = %+v", stored)
	}
	c.event(http.MethodPatch, "/v1/events/"+yoga.Id.String(), `{"recurrence":{"frequency":"weekly","interval":2}}`, http.StatusOK)
	equal(t, "every other week", c.titles("/v1/events?"+q("from", "2026-10-11T22:00:00Z", "to", "2026-11-02T23:00:00Z", "sourceKind", "manual")+"&limit=100"),
		[]string{"Yoga", "Course", "Course", "Dentist", "Anna", "Yoga"})
	// A cancelled series leaves listings with all its occurrences.
	c.event(http.MethodPatch, "/v1/events/"+yoga.Id.String(), `{"status":"cancelled"}`, http.StatusOK)
	equal(t, "cancelled series", c.titles(window), []string{"Course", "Course", "Dentist", "Anna"})
	equal(t, "with cancelled", c.titles(window+"&includeCancelled=true"), []string{"Yoga", "Course", "Course", "Dentist", "Anna", "Yoga"})
}

// Search returns a recurring event's next occurrence (or its last one once
// it has ended), or every occurrence in a bounded range.
func TestSearchFindsRecurringEvents(t *testing.T) {
	c := newAPIClient(t)
	c.event(http.MethodPost, "/v1/events", `{"title":"Yoga class","allDay":false,"startAt":"2026-09-07T16:00:00Z","endAt":"2026-09-07T17:00:00Z","timeZone":"Europe/Berlin","recurrence":{"frequency":"weekly","interval":1}}`, http.StatusCreated)
	c.event(http.MethodPost, "/v1/events", `{"title":"Yoga retreat","allDay":true,"startDate":"2026-08-01","endDate":"2026-08-02","recurrence":{"frequency":"monthly","interval":1,"until":"2026-09-01T00:00:00Z"}}`, http.StatusCreated)
	c.event(http.MethodPost, "/v1/events", `{"title":"Yoga mat delivery","allDay":false,"startAt":"2026-10-30T10:00:00Z","endAt":"2026-10-30T11:00:00Z"}`, http.StatusCreated)

	search := func(values ...string) []string {
		return occurrenceStarts(c.list("/v1/events/search?" + q(values...)).Items)
	}
	// Now is Saturday 10 October: the class's next occurrence is Monday 12
	// October; the retreat ended with its September occurrence.
	equal(t, "open search", search("q", "yoga"), []string{
		"Yoga class 2026-10-12T16:00:00Z *", "Yoga mat delivery 2026-10-30T10:00:00Z", "Yoga retreat 2026-09-01 *",
	})
	equal(t, "bounded search", search("q", "yoga class", "from", "2026-10-01T00:00:00Z", "to", "2026-10-20T00:00:00Z"), []string{
		"Yoga class 2026-10-12T16:00:00Z *", "Yoga class 2026-10-19T16:00:00Z *", "Yoga class 2026-10-05T16:00:00Z *",
	})
	equal(t, "from only", search("q", "class", "from", "2026-11-01T00:00:00Z"), []string{"Yoga class 2026-11-02T17:00:00Z *"})
	equal(t, "to only", search("q", "retreat", "to", "2026-08-15T00:00:00Z"), []string{"Yoga retreat 2026-08-01 *"})
	// Paging through a bounded search keeps the order.
	target := "/v1/events/search?" + q("q", "yoga", "from", "2026-08-01T00:00:00Z", "to", "2026-10-20T00:00:00Z")
	all := c.titles(target + "&limit=100")
	if len(all) != 9 || strings.Join(c.titles(target+"&limit=2"), "|") != strings.Join(all, "|") {
		t.Fatalf("bounded search pages: %v vs %v", c.titles(target+"&limit=2"), all)
	}
}
