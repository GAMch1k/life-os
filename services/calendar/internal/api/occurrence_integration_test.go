package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// occurrencePath is an occurrence endpoint, its recurrence id URL-encoded
// as a browser's encodeURIComponent does (":" as %3A).
func occurrencePath(id types.UUID, rid, suffix string) string {
	return "/v1/events/" + id.String() + "/occurrences/" + strings.ReplaceAll(rid, ":", "%3A") + suffix
}

// failure sends a request that must fail and returns its error code and
// message.
func (c apiClient) failure(method, target string, body any, wantStatus int) (string, string) {
	c.t.Helper()
	status, raw := c.do(method, target, body)
	if status != wantStatus {
		c.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, wantStatus)
	}
	var out struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
	}
	return out.Error.Code, out.Error.Message
}

// exceptionKeys lists the recurrence ids of a series' stored exceptions.
func (c apiClient) exceptionKeys(id types.UUID) []string {
	c.t.Helper()
	rows, err := c.queries.ListEventExceptions(context.Background(), []pgtype.UUID{pgUUID(id)})
	if err != nil {
		c.t.Fatal(err)
	}
	keys := []string{}
	for _, row := range rows {
		keys = append(keys, row.RecurrenceID)
	}
	return keys
}

// occurrenceLabels lists items as "title start recurrenceId [modified]
// [cancelled]".
func occurrenceLabels(items []generated.CalendarEvent) []string {
	var out []string
	for _, item := range items {
		label := item.Title + " "
		if item.AllDay {
			label += item.StartDate.String()
		} else {
			label += item.StartAt.Format(time.RFC3339)
		}
		if item.Occurrence != nil {
			label += " " + item.Occurrence.RecurrenceId
			if item.Occurrence.Modified {
				label += " modified"
			}
		}
		if item.Status == generated.Cancelled {
			label += " cancelled"
		}
		out = append(out, label)
	}
	return out
}

// gymSeries creates the owner's Saturday gym, weekly at 07:00 Berlin from 3
// October 2026 (05:00 UTC in summer time, 06:00 UTC from 31 October).
func gymSeries(c apiClient, extra map[string]any) generated.CalendarEvent {
	body := map[string]any{
		"title": "Gym", "description": "Bring a towel", "allDay": false,
		"startAt": "2026-10-03T07:00:00+02:00", "endAt": "2026-10-03T08:00:00+02:00", "timeZone": "Europe/Berlin",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1},
	}
	for key, value := range extra {
		body[key] = value
	}
	return c.event(http.MethodPost, "/v1/events", body, http.StatusCreated)
}

// This event: one occurrence is skipped, moved, edited, and restored; the
// series and its other occurrences stay as they are.
func TestThisEventChangesOneOccurrence(t *testing.T) {
	c := newAPIClient(t)
	gym := gymSeries(c, nil)
	window := "/v1/events?" + q("from", "2026-10-04T22:00:00Z", "to", "2026-10-25T22:00:00Z")
	equal(t, "before", occurrenceLabels(c.list(window).Items), []string{
		"Gym 2026-10-10T05:00:00Z 2026-10-10T05:00:00Z", "Gym 2026-10-17T05:00:00Z 2026-10-17T05:00:00Z", "Gym 2026-10-24T05:00:00Z 2026-10-24T05:00:00Z",
	})

	// "Cancel gym today": today's occurrence is skipped, kept as history.
	today := c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-10T05:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)
	if today.Status != generated.Cancelled || today.CancelledAt == nil || today.Occurrence.Modified || today.Id != gym.Id || today.StartAt.Format(time.RFC3339) != "2026-10-10T05:00:00Z" {
		t.Fatalf("skipped = %+v", today)
	}
	equal(t, "skipped", occurrenceLabels(c.list(window).Items), []string{
		"Gym 2026-10-17T05:00:00Z 2026-10-17T05:00:00Z", "Gym 2026-10-24T05:00:00Z 2026-10-24T05:00:00Z",
	})
	equal(t, "skipped, with cancelled", occurrenceLabels(c.list(window+"&includeCancelled=true").Items), []string{
		"Gym 2026-10-10T05:00:00Z 2026-10-10T05:00:00Z cancelled", "Gym 2026-10-17T05:00:00Z 2026-10-17T05:00:00Z", "Gym 2026-10-24T05:00:00Z 2026-10-24T05:00:00Z",
	})
	if got := c.event(http.MethodGet, occurrencePath(gym.Id, "2026-10-10T05:00:00Z", ""), nil, http.StatusOK); got.Status != generated.Cancelled || got.Occurrence.RecurrenceId != "2026-10-10T05:00:00Z" {
		t.Fatalf("get skipped = %+v", got)
	}
	// The series itself is unchanged.
	if series := c.event(http.MethodGet, "/v1/events/"+gym.Id.String(), nil, http.StatusOK); series.Status != generated.Active || series.Occurrence != nil {
		t.Fatalf("series = %+v", series)
	}

	// Next Saturday's occurrence moves to Sunday 09:00, gets a title, no
	// notes, and a reminder of its own.
	moved := c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", ""), map[string]any{
		"title": "Gym with Anna", "description": nil, "startAt": "2026-10-18T09:00:00+02:00", "endAt": "2026-10-18T10:00:00+02:00",
		"reminders": []map[string]any{{"kind": "relative", "minutesBefore": 15}},
	}, http.StatusOK)
	if !moved.Occurrence.Modified || moved.Title != "Gym with Anna" || moved.Description != nil || moved.StartAt.Format(time.RFC3339) != "2026-10-18T07:00:00Z" ||
		len(moved.EffectiveReminders) != 1 || *moved.EffectiveReminders[0].MinutesBefore != 15 || moved.Occurrence.RecurrenceId != "2026-10-17T05:00:00Z" {
		t.Fatalf("moved = %+v", moved)
	}
	// 24 October moves out of the window; 7 November moves into it.
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-24T05:00:00Z", ""), `{"startAt":"2026-11-30T17:00:00Z","endAt":"2026-11-30T18:00:00Z"}`, http.StatusOK)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-11-07T06:00:00Z", ""), `{"startAt":"2026-10-12T16:00:00Z","endAt":"2026-10-12T17:00:00Z"}`, http.StatusOK)
	want := []string{
		"Gym 2026-10-12T16:00:00Z 2026-11-07T06:00:00Z modified",
		"Gym with Anna 2026-10-18T07:00:00Z 2026-10-17T05:00:00Z modified",
	}
	equal(t, "moved", occurrenceLabels(c.list(window).Items), want)
	equal(t, "moved, paged", c.titles(window+"&limit=1"), []string{"Gym", "Gym with Anna"})
	if other := c.list("/v1/events?" + q("from", "2026-10-30T22:00:00Z", "to", "2026-11-01T22:00:00Z")).Items; len(other) != 1 || other[0].Description == nil || *other[0].Description != "Bring a towel" || other[0].Occurrence.Modified {
		t.Fatalf("an unchanged occurrence = %+v", other)
	}

	// Search passes the skipped occurrence over: the series' next one is the
	// one moved to Monday; "today" (the owner's day) finds nothing active.
	equal(t, "search", occurrenceLabels(c.list("/v1/events/search?"+q("q", "gym")).Items), []string{"Gym 2026-10-12T16:00:00Z 2026-11-07T06:00:00Z modified"})
	todayRange := q("q", "gym", "from", "2026-10-09T22:00:00Z", "to", "2026-10-10T22:00:00Z")
	if items := c.list("/v1/events/search?" + todayRange).Items; len(items) != 0 {
		t.Fatalf("today's skipped gym found: %v", occurrenceLabels(items))
	}
	equal(t, "today, with cancelled", occurrenceLabels(c.list("/v1/events/search?"+todayRange+"&includeCancelled=true").Items), []string{"Gym 2026-10-10T05:00:00Z 2026-10-10T05:00:00Z cancelled"})

	// Restoring the skipped occurrence, and setting the edited one back to
	// the series' values, leaves no changes of their own.
	if restored := c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-10T05:00:00Z", ""), `{"status":"active"}`, http.StatusOK); restored.Status != generated.Active || restored.CancelledAt != nil {
		t.Fatalf("restored = %+v", restored)
	}
	back := c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", ""), map[string]any{
		"title": "Gym", "description": "Bring a towel", "startAt": "2026-10-17T05:00:00Z", "endAt": "2026-10-17T06:00:00Z", "reminders": []any{},
	}, http.StatusOK)
	if back.Occurrence.Modified || back.Title != "Gym" {
		t.Fatalf("set back = %+v", back)
	}
	equal(t, "exceptions left", c.exceptionKeys(gym.Id), []string{"2026-10-24T05:00:00Z", "2026-11-07T06:00:00Z"})

	// What cannot be changed on one occurrence, and what is not one.
	path := occurrencePath(gym.Id, "2026-10-31T06:00:00Z", "")
	for body, message := range map[string]string{
		`{"allDay":true,"startDate":"2026-10-31","endDate":"2026-10-31"}`: "an occurrence cannot switch between timed and all-day; change all events instead",
		`{"recurrence":{"frequency":"daily","interval":1}}`:               "recurrence belongs to the series: change all events, or this and following",
		`{"timeZone":"Europe/London"}`:                                    "timeZone belongs to the series: change all events, or this and following",
		`{"endAt":"2026-10-31T05:00:00Z"}`:                                "endAt must be after startAt",
		`{"startDate":"2026-10-31","endDate":"2026-10-31"}`:               "timed events require startAt and endAt only",
		`{"title":null}`: "title cannot be null",
	} {
		if code, got := c.failure(http.MethodPatch, path, body, http.StatusBadRequest); code != "bad_request" || got != message {
			t.Errorf("%s: %s %q, want %q", body, code, got, message)
		}
	}
	if code, _ := c.failure(http.MethodPatch, path, `{"allDay":false,"title":"Gym!"}`, http.StatusOK); code != "" {
		t.Errorf("allDay unchanged: %s", code)
	}
	for _, rid := range []string{"2026-10-11T05:00:00Z", "2026-09-26T05:00:00Z", "2026-10-17", "tomorrow"} {
		if _, message := c.failure(http.MethodGet, occurrencePath(gym.Id, rid, ""), nil, http.StatusNotFound); message != "occurrence not found" {
			t.Errorf("GET %s: %q", rid, message)
		}
		if _, message := c.failure(http.MethodPatch, occurrencePath(gym.Id, rid, ""), `{"status":"cancelled"}`, http.StatusNotFound); message != "occurrence not found" {
			t.Errorf("PATCH %s: %q", rid, message)
		}
	}
	dentist := c.event(http.MethodPost, "/v1/events", `{"title":"Dentist","allDay":false,"startAt":"2026-10-20T08:00:00Z","endAt":"2026-10-20T09:00:00Z"}`, http.StatusCreated)
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		if _, message := c.failure(method, occurrencePath(dentist.Id, "2026-10-20T08:00:00Z", ""), `{"status":"cancelled"}`, http.StatusNotFound); message != "this event does not repeat" {
			t.Errorf("%s a single event: %q", method, message)
		}
	}
	if _, message := c.failure(http.MethodGet, "/v1/events/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10/occurrences/x", nil, http.StatusNotFound); message != "event not found" {
		t.Errorf("a missing event: %q", message)
	}
	// An imported event is read-only and never a stored series.
	source := c.newSource("zaw")
	pickup := c.seed(importedParams(source, "zaw-1", "Papier", "2026-11-17T05:00:00Z"))
	importedID := types.UUID(pickup.ID.Bytes)
	for _, suffix := range []string{"", "/split", "/truncate"} {
		method := http.MethodPost
		if suffix == "" {
			method = http.MethodPatch
		}
		if code, _ := c.failure(method, occurrencePath(importedID, "2026-11-17T05:00:00Z", suffix), `{"title":"X"}`, http.StatusConflict); code != "managed_by_source" {
			t.Errorf("%s %s on an imported event: %s", method, suffix, code)
		}
	}
	if _, message := c.failure(http.MethodGet, occurrencePath(importedID, "2026-11-17T05:00:00Z", ""), nil, http.StatusNotFound); message != "this event does not repeat" {
		t.Errorf("GET an imported event's occurrence: %q", message)
	}
}

// All events: a change of the series keeps its occurrences' own changes,
// except those of occurrences the change ends or moves.
func TestAllEventsChangesKeepOccurrenceChanges(t *testing.T) {
	c := newAPIClient(t)
	gym := gymSeries(c, nil)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-24T05:00:00Z", ""), `{"title":"Gym late"}`, http.StatusOK)
	window := "/v1/events?" + q("from", "2026-10-11T22:00:00Z", "to", "2026-11-01T22:00:00Z", "includeCancelled", "true")

	c.event(http.MethodPatch, "/v1/events/"+gym.Id.String(), `{"title":"Fitness","endAt":"2026-10-03T06:30:00Z"}`, http.StatusOK)
	equal(t, "renamed", occurrenceLabels(c.list(window).Items), []string{
		"Fitness 2026-10-17T05:00:00Z 2026-10-17T05:00:00Z cancelled", "Gym late 2026-10-24T05:00:00Z 2026-10-24T05:00:00Z modified", "Fitness 2026-10-31T06:00:00Z 2026-10-31T06:00:00Z",
	})
	// Ending the series on 20 October ends 24 October with it.
	c.event(http.MethodPatch, "/v1/events/"+gym.Id.String(), `{"recurrence":{"frequency":"weekly","interval":1,"until":"2026-10-20T00:00:00Z"}}`, http.StatusOK)
	equal(t, "ended", c.exceptionKeys(gym.Id), []string{"2026-10-17T05:00:00Z"})
	// Two days a week keeps every Saturday.
	c.event(http.MethodPatch, "/v1/events/"+gym.Id.String(), `{"recurrence":{"frequency":"weekly","interval":1,"byWeekday":["SA","WE"]}}`, http.StatusOK)
	equal(t, "Wednesdays too", c.exceptionKeys(gym.Id), []string{"2026-10-17T05:00:00Z"})
	equal(t, "Wednesdays too", occurrenceLabels(c.list(window).Items), []string{
		"Fitness 2026-10-14T05:00:00Z 2026-10-14T05:00:00Z", "Fitness 2026-10-17T05:00:00Z 2026-10-17T05:00:00Z cancelled", "Fitness 2026-10-21T05:00:00Z 2026-10-21T05:00:00Z",
		"Fitness 2026-10-24T05:00:00Z 2026-10-24T05:00:00Z", "Fitness 2026-10-28T06:00:00Z 2026-10-28T06:00:00Z", "Fitness 2026-10-31T06:00:00Z 2026-10-31T06:00:00Z",
	})
	// A new start time moves every occurrence: none keeps its changes.
	c.event(http.MethodPatch, "/v1/events/"+gym.Id.String(), `{"startAt":"2026-10-03T08:00:00+02:00","endAt":"2026-10-03T09:00:00+02:00"}`, http.StatusOK)
	equal(t, "new time", c.exceptionKeys(gym.Id), []string{})
	// Deleting the series deletes its occurrences' changes.
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T06:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)
	if status, raw := c.do(http.MethodDelete, "/v1/events/"+gym.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	equal(t, "deleted", c.exceptionKeys(gym.Id), []string{})
}

// This and following: the series ends before the occurrence and a new one
// starts there with the changes; occurrence changes move along while their
// occurrence still exists, and a count is shared out.
func TestThisAndFollowingSplitsTheSeries(t *testing.T) {
	c := newAPIClient(t)
	// Ten Saturdays: 3 October to 5 December.
	gym := gymSeries(c, map[string]any{"recurrence": map[string]any{"frequency": "weekly", "interval": 1, "count": 10}})
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-10T05:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", ""), `{"title":"Gym with Anna","location":"Park"}`, http.StatusOK)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-24T05:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)

	swim := c.event(http.MethodPost, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", "/split"), `{"title":"Swim"}`, http.StatusCreated)
	if swim.Id == gym.Id || swim.Title != "Swim" || swim.StartAt.Format(time.RFC3339) != "2026-10-17T05:00:00Z" || swim.Recurrence == nil || *swim.Recurrence.Count != 8 || swim.Recurrence.Until != nil || *swim.TimeZone != "Europe/Berlin" {
		t.Fatalf("new series = %+v", swim)
	}
	ended := c.event(http.MethodGet, "/v1/events/"+gym.Id.String(), nil, http.StatusOK)
	if ended.Recurrence.Count != nil || ended.Recurrence.Until == nil || ended.Recurrence.Until.Format(time.RFC3339) != "2026-10-10T05:00:00Z" || ended.Title != "Gym" {
		t.Fatalf("ended series = %+v", ended.Recurrence)
	}
	window := "/v1/events?" + q("from", "2026-10-01T00:00:00Z", "to", "2027-01-01T00:00:00Z", "includeCancelled", "true") + "&limit=100"
	equal(t, "split", occurrenceLabels(c.list(window).Items), []string{
		"Gym 2026-10-03T05:00:00Z 2026-10-03T05:00:00Z", "Gym 2026-10-10T05:00:00Z 2026-10-10T05:00:00Z cancelled",
		// The split occurrence keeps its own location; the title is the new series'.
		"Swim 2026-10-17T05:00:00Z 2026-10-17T05:00:00Z modified", "Swim 2026-10-24T05:00:00Z 2026-10-24T05:00:00Z cancelled",
		"Swim 2026-10-31T06:00:00Z 2026-10-31T06:00:00Z", "Swim 2026-11-07T06:00:00Z 2026-11-07T06:00:00Z", "Swim 2026-11-14T06:00:00Z 2026-11-14T06:00:00Z",
		"Swim 2026-11-21T06:00:00Z 2026-11-21T06:00:00Z", "Swim 2026-11-28T06:00:00Z 2026-11-28T06:00:00Z", "Swim 2026-12-05T06:00:00Z 2026-12-05T06:00:00Z",
	})
	equal(t, "old exceptions", c.exceptionKeys(gym.Id), []string{"2026-10-10T05:00:00Z"})
	equal(t, "new exceptions", c.exceptionKeys(swim.Id), []string{"2026-10-17T05:00:00Z", "2026-10-24T05:00:00Z"})

	// A later split with a new time moves every following occurrence: their
	// changes are dropped, and the count left is shared out again.
	c.event(http.MethodPatch, occurrencePath(swim.Id, "2026-11-21T06:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)
	evening := c.event(http.MethodPost, occurrencePath(swim.Id, "2026-11-14T06:00:00Z", "/split"), `{"startAt":"2026-11-14T18:00:00+01:00","endAt":"2026-11-14T19:00:00+01:00"}`, http.StatusCreated)
	if *evening.Recurrence.Count != 4 || evening.Title != "Swim" {
		t.Fatalf("evening series = %+v", evening)
	}
	equal(t, "evening", occurrenceLabels(c.list("/v1/events?"+q("from", "2026-11-10T00:00:00Z", "to", "2027-01-01T00:00:00Z", "includeCancelled", "true")).Items), []string{
		"Swim 2026-11-14T17:00:00Z 2026-11-14T17:00:00Z", "Swim 2026-11-21T17:00:00Z 2026-11-21T17:00:00Z",
		"Swim 2026-11-28T17:00:00Z 2026-11-28T17:00:00Z", "Swim 2026-12-05T17:00:00Z 2026-12-05T17:00:00Z",
	})
	equal(t, "evening exceptions", append(c.exceptionKeys(swim.Id), c.exceptionKeys(evening.Id)...), []string{"2026-10-17T05:00:00Z", "2026-10-24T05:00:00Z"})

	// On the first occurrence nothing is split: the series changes.
	first := c.event(http.MethodPost, occurrencePath(gym.Id, "2026-10-03T05:00:00Z", "/split"), `{"location":"Gym hall"}`, http.StatusOK)
	if first.Id != gym.Id || *first.Location != "Gym hall" || first.Recurrence.Until == nil {
		t.Fatalf("first-occurrence split = %+v", first)
	}

	// A birthday, annually on 17 September, gets a new title from 2027 on.
	anna := c.event(http.MethodPost, "/v1/events", `{"title":"Anna's birthday","allDay":true,"startDate":"2020-09-17","endDate":"2020-09-17","recurrence":{"frequency":"yearly","interval":1}}`, http.StatusCreated)
	renamed := c.event(http.MethodPost, occurrencePath(anna.Id, "2027-09-17", "/split"), `{"title":"Anna's birthday 🎂"}`, http.StatusCreated)
	if renamed.StartDate.String() != "2027-09-17" {
		t.Fatalf("renamed = %+v", renamed)
	}
	birthdays := c.list("/v1/events/search?" + q("q", "birthday", "from", "2026-01-01T00:00:00Z", "to", "2029-01-01T00:00:00Z"))
	var labels []string
	for _, item := range birthdays.Items {
		labels = append(labels, item.Title+" "+item.StartDate.String())
	}
	slices.Sort(labels)
	equal(t, "birthdays", labels, []string{"Anna's birthday 2026-09-17", "Anna's birthday 🎂 2027-09-17", "Anna's birthday 🎂 2028-09-17"})

	// The new series is validated like any event.
	if _, message := c.failure(http.MethodPost, occurrencePath(swim.Id, "2026-10-31T06:00:00Z", "/split"), `{"recurrence":{"frequency":"weekly","interval":1,"byWeekday":["MO"]}}`, http.StatusBadRequest); message != "recurrence byWeekday must include the weekday the event starts on (Saturday)" {
		t.Errorf("invalid split: %q", message)
	}
}

// Deleting this and following ends the series before the occurrence; on
// the first occurrence it deletes the series.
func TestThisAndFollowingTruncatesTheSeries(t *testing.T) {
	c := newAPIClient(t)
	gym := gymSeries(c, nil)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-10T05:00:00Z", ""), `{"title":"Gym early"}`, http.StatusOK)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-24T05:00:00Z", ""), `{"status":"cancelled"}`, http.StatusOK)
	if status, raw := c.do(http.MethodPost, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", "/truncate"), nil); status != http.StatusNoContent {
		t.Fatalf("truncate: %d %s", status, raw)
	}
	equal(t, "truncated", occurrenceLabels(c.list("/v1/events?"+q("from", "2026-10-01T00:00:00Z", "to", "2027-01-01T00:00:00Z", "includeCancelled", "true")).Items), []string{
		"Gym 2026-10-03T05:00:00Z 2026-10-03T05:00:00Z", "Gym early 2026-10-10T05:00:00Z 2026-10-10T05:00:00Z modified",
	})
	equal(t, "exceptions", c.exceptionKeys(gym.Id), []string{"2026-10-10T05:00:00Z"})
	if _, message := c.failure(http.MethodPost, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", "/truncate"), nil, http.StatusNotFound); message != "occurrence not found" {
		t.Errorf("truncate past the end: %q", message)
	}
	if status, _ := c.do(http.MethodPost, occurrencePath(gym.Id, "2026-10-03T05:00:00Z", "/truncate"), nil); status != http.StatusNoContent {
		t.Fatalf("truncate at the first occurrence: %d", status)
	}
	c.failure(http.MethodGet, "/v1/events/"+gym.Id.String(), nil, http.StatusNotFound)
	equal(t, "deleted", c.exceptionKeys(gym.Id), []string{})
}

// Weekly rules on several days and rules that end after a number of
// occurrences.
func TestWeekdayAndCountRules(t *testing.T) {
	c := newAPIClient(t)
	course := c.event(http.MethodPost, "/v1/events", `{"title":"Course","allDay":false,"startAt":"2026-10-12T18:00:00+02:00","endAt":"2026-10-12T19:30:00+02:00","timeZone":"Europe/Berlin",
		"recurrence":{"frequency":"weekly","interval":1,"byWeekday":["TH","MO","TH"],"count":5}}`, http.StatusCreated)
	if days := *course.Recurrence.ByWeekday; len(days) != 2 || days[0] != generated.MO || days[1] != generated.TH || *course.Recurrence.Count != 5 {
		t.Fatalf("rule = %+v", course.Recurrence)
	}
	equal(t, "course", occurrenceStarts(c.list("/v1/events?"+q("from", "2026-10-01T00:00:00Z", "to", "2027-01-01T00:00:00Z")).Items), []string{
		"Course 2026-10-12T16:00:00Z *", "Course 2026-10-15T16:00:00Z *", "Course 2026-10-19T16:00:00Z *", "Course 2026-10-22T16:00:00Z *", "Course 2026-10-26T17:00:00Z *",
	})
	for body, message := range map[string]string{
		`{"recurrence":{"frequency":"weekly","interval":1,"byWeekday":["TU"]}}`:                       "recurrence byWeekday must include the weekday the event starts on (Monday)",
		`{"recurrence":{"frequency":"monthly","interval":1,"byWeekday":["MO"]}}`:                      "recurrence byWeekday is only for weekly recurrence",
		`{"recurrence":{"frequency":"weekly","interval":1,"byWeekday":["MON"]}}`:                      "recurrence byWeekday days must be MO, TU, WE, TH, FR, SA, or SU",
		`{"recurrence":{"frequency":"weekly","interval":1,"count":0}}`:                                "recurrence count must be between 1 and 1000",
		`{"recurrence":{"frequency":"weekly","interval":1,"count":2,"until":"2027-01-01T00:00:00Z"}}`: "recurrence takes until or count, not both",
		`{"recurrence":{"frequency":"weekly","interval":1,"until":"2026-10-12T15:59:59Z"}}`:           "recurrence until must not be before the event starts",
		`{"startAt":"2026-10-13T16:00:00Z","endAt":"2026-10-13T17:00:00Z"}`:                           "recurrence byWeekday must include the weekday the event starts on (Tuesday)",
	} {
		if _, got := c.failure(http.MethodPatch, "/v1/events/"+course.Id.String(), body, http.StatusBadRequest); got != message {
			t.Errorf("%s: %q, want %q", body, got, message)
		}
	}
}

// Occurrence changes, splits, and truncations are recorded in Activity with
// the occurrence they concern.
func TestOccurrenceChangesAreRecordedInActivity(t *testing.T) {
	c, recorder := newActivityClient(t)
	seen := 0
	gym := gymSeries(c, nil)
	expectRecorded(t, recorder, &seen, eventCreated)

	path := occurrencePath(gym.Id, "2026-10-10T05:00:00Z", "")
	status, raw := c.do(http.MethodPatch, path, `{"status":"cancelled"}`, activity.ActorHeader, "agent:codex")
	if status != http.StatusOK {
		t.Fatalf("cancel: %d %s", status, raw)
	}
	cancelled := expectRecorded(t, recorder, &seen, eventCancelled)[0]
	if cancelled.Summary != "Event “Gym” on Sat 10 Oct 2026, 07:00 CEST cancelled" || cancelled.Data["recurrenceId"] != "2026-10-10T05:00:00Z" ||
		cancelled.Data["startAt"] != "2026-10-10T05:00:00Z" || cancelled.Subject.ID != gym.Id.String() || cancelled.Actor.Kind != "agent" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	c.event(http.MethodPatch, path, `{"status":"cancelled"}`, http.StatusOK)
	expectRecorded(t, recorder, &seen)
	c.event(http.MethodPatch, path, `{"status":"active"}`, http.StatusOK)
	expectRecorded(t, recorder, &seen, eventRestored)
	c.event(http.MethodPatch, path, `{"startAt":"2026-10-11T07:00:00Z","endAt":"2026-10-11T08:00:00Z"}`, http.StatusOK)
	moved := expectRecorded(t, recorder, &seen, eventRescheduled)[0]
	if previous, _ := moved.Data["previous"].(map[string]any); previous["startAt"] != "2026-10-10T05:00:00Z" || moved.Data["startAt"] != "2026-10-11T07:00:00Z" {
		t.Fatalf("moved = %+v", moved.Data)
	}
	c.event(http.MethodPatch, path, `{"title":"Gym with Anna"}`, http.StatusOK)
	expectRecorded(t, recorder, &seen, eventUpdated)

	swim := c.event(http.MethodPost, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", "/split"), `{"title":"Swim"}`, http.StatusCreated)
	split := expectRecorded(t, recorder, &seen, eventUpdated, eventCreated)
	if changes, _ := split[0].Data["changes"].([]any); len(changes) != 1 || changes[0] != "recurrence" || split[0].Subject.ID != gym.Id.String() {
		t.Fatalf("ended = %+v", split[0])
	}
	if split[1].Subject.ID != swim.Id.String() || split[1].Data["splitFrom"] != gym.Id.String() {
		t.Fatalf("created = %+v", split[1])
	}
	if status, _ := c.do(http.MethodPost, occurrencePath(swim.Id, "2026-10-24T05:00:00Z", "/truncate"), nil); status != http.StatusNoContent {
		t.Fatal("truncate")
	}
	expectRecorded(t, recorder, &seen, eventUpdated)
	if status, _ := c.do(http.MethodPost, occurrencePath(swim.Id, "2026-10-17T05:00:00Z", "/truncate"), nil); status != http.StatusNoContent {
		t.Fatal("truncate at the first occurrence")
	}
	expectRecorded(t, recorder, &seen, eventDeleted)
}
