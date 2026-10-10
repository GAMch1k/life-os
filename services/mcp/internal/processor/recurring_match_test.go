package processor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// gymOccurrence is one occurrence of the owner's weekly gym (07:00–08:00
// Berlin on Saturdays), as Calendar search returns it.
func gymOccurrence(recurrenceID, startAt, endAt string) map[string]any {
	event := timedEvent("series-gym", "Gym", startAt, endAt)
	event["recurrence"] = map[string]any{"frequency": "weekly", "interval": 1.0}
	event["occurrence"] = map[string]any{"seriesId": "series-gym", "recurrenceId": recurrenceID, "modified": false}
	return event
}

// todaysGym is Saturday 10 October's occurrence (over by the capture at
// 10:00); nextGym is the series' next one, which an open search returns.
var (
	todaysGym = func() map[string]any {
		return gymOccurrence("2026-10-10T05:00:00Z", "2026-10-10T05:00:00Z", "2026-10-10T06:00:00Z")
	}
	nextGym = func() map[string]any {
		return gymOccurrence("2026-10-17T05:00:00Z", "2026-10-17T05:00:00Z", "2026-10-17T06:00:00Z")
	}
)

const cancelGymToday = `{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"gym","eventQueryAlternatives":["Fitness"],"onDate":"2026-10-10"}}],"confidence":0.95}`

// "Cancel gym today": the candidate is today's occurrence, and the proposed
// action remembers it, so the approval skips that occurrence only.
func TestCancelGymTodayProposesTodaysOccurrence(t *testing.T) {
	tools := newCalendarTools("Cancel gym today", nextGym())
	tools.matches["gym"] = []string{"series-gym"}
	tools.dayMatches = map[string][]map[string]any{"gym": {todaysGym()}, "Fitness": {todaysGym()}}
	runCalendarPlan(t, tools, cancelGymToday)

	if contains(tools.calls, "calendar_cancel") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("a cancellation ran without approval: %#v", tools.calls)
	}
	// Today's searches (bounded to the owner's day) come first, then the
	// usual ones from the start of today.
	if len(tools.searches) != 4 {
		t.Fatalf("searches = %v", tools.searches)
	}
	for index, search := range tools.searches {
		bounded := index < 2
		if search.Get("from") != "2026-10-09T22:00:00Z" || search.Has("to") != bounded || (bounded && search.Get("to") != "2026-10-10T22:00:00Z") || search.Get("editable") != "true" || search.Has("sourceKind") {
			t.Fatalf("search %d = %v", index, search)
		}
	}
	if got := tools.candidateIDs(); !reflect.DeepEqual(got, []string{"series-gym"}) {
		t.Fatalf("candidates = %q", got)
	}
	candidate := tools.candidates()[0]
	if candidate["title"] != "Gym · Sat 10 Oct 2026 07:00–08:00" || candidate["recurrenceId"] != "2026-10-10T05:00:00Z" {
		t.Fatalf("candidate = %#v", candidate)
	}
	proposed := tools.review["proposedAction"].(plannedAction)
	if proposed.Tool != "calendar_cancel" || !reflect.DeepEqual(proposed.Arguments["occurrences"], map[string]any{"series-gym": "2026-10-10T05:00:00Z"}) || proposed.Arguments["onDate"] != "2026-10-10" {
		t.Fatalf("proposed = %#v", proposed)
	}
	if tools.review["errorSummary"] != "Cancelling a Calendar event needs your approval: choose the event to cancel it" {
		t.Fatalf("review = %#v", tools.review)
	}
}

// Without a day, or when the series has no occurrence that day, the
// candidate is its next occurrence; a single event that day is never taken
// from the day's search.
func TestCancelOfARecurringEventProposesItsNextOccurrence(t *testing.T) {
	for name, plan := range map[string]string{
		"no day":                   `{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"gym"}}],"confidence":0.95}`,
		"no occurrence that day":   cancelGymToday,
		"a planned map is ignored": `{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"gym","occurrences":{"series-gym":"2026-12-26T06:00:00Z","other":"x"}}}],"confidence":0.95}`,
	} {
		t.Run(name, func(t *testing.T) {
			tools := newCalendarTools("Cancel gym", nextGym())
			tools.matches["gym"] = []string{"series-gym"}
			tools.dayMatches = map[string][]map[string]any{"gym": {timedEvent("event-gym-shop", "Gym shop visit", "2026-10-10T14:00:00Z", "2026-10-10T15:00:00Z")}}
			runCalendarPlan(t, tools, plan)
			if got := tools.candidateIDs(); !reflect.DeepEqual(got, []string{"series-gym"}) {
				t.Fatalf("candidates = %q", got)
			}
			proposed := tools.review["proposedAction"].(plannedAction)
			if !reflect.DeepEqual(proposed.Arguments["occurrences"], map[string]any{"series-gym": "2026-10-17T05:00:00Z"}) || tools.candidates()[0]["title"] != "Gym · Sat 17 Oct 2026 07:00–08:00" {
				t.Fatalf("proposed = %#v, candidates = %#v", proposed, tools.candidates())
			}
		})
	}
}

// Approving the occurrence cancels that occurrence alone.
func TestApprovedOccurrenceCancelSkipsOnlyThatOccurrence(t *testing.T) {
	tools := newCalendarTools("Cancel gym today", nextGym())
	approvedItem(tools, map[string]any{"tool": "calendar_cancel", "arguments": map[string]any{
		"eventQuery": "gym", "onDate": "2026-10-10", "occurrences": map[string]any{"series-gym": "2026-10-10T05:00:00Z"}, "taskId": "series-gym",
	}})
	runner := Runner{Planner: fakePlanner{err: errors.New("an approved item is not planned again")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tools.inputs["calendar_cancel"]; !reflect.DeepEqual(got, map[string]any{"eventId": "series-gym", "recurrenceId": "2026-10-10T05:00:00Z"}) {
		t.Fatalf("calendar_cancel input = %#v, calls = %#v", got, tools.calls)
	}
	if len(tools.searches) != 0 || len(tools.gets) != 0 || !contains(tools.calls, "inbox_resolve") || tools.review != nil {
		t.Fatalf("searches = %v, gets = %v, calls = %#v", tools.searches, tools.gets, tools.calls)
	}
}

// A recurring event chosen without an occurrence (such as in a review made
// before occurrences were recorded) is never cancelled as a whole.
func TestApprovedCancelOfAWholeSeriesIsRefused(t *testing.T) {
	tools := newCalendarTools("Cancel gym", nextGym())
	approvedItem(tools, map[string]any{"tool": "calendar_cancel", "arguments": map[string]any{"eventQuery": "gym", "taskId": "series-gym"}})
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if contains(tools.calls, "calendar_cancel") || contains(tools.calls, "inbox_resolve") || tools.review == nil || tools.review["errorSummary"] != recurringChangeReason || len(tools.candidates()) != 0 {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

// Changing a recurring event is for the Calendar: the capture changes
// nothing and the review offers nothing to approve.
func TestCalendarUpdateOfARecurringEventGoesToHumanReview(t *testing.T) {
	for name, confidence := range map[string]string{"confident": "0.95", "uncertain": "0.6"} {
		t.Run(name, func(t *testing.T) {
			tools := newCalendarTools("Move gym to 9", nextGym())
			tools.matches["gym"] = []string{"series-gym"}
			runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"gym","startAt":"2026-10-17T09:00:00+02:00"}}],"confidence":`+confidence+`}`)
			if contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_resolve") {
				t.Fatalf("a recurring event was changed: %#v", tools.calls)
			}
			if tools.review == nil || tools.review["errorSummary"] != "Recurring events are changed in the Calendar." || len(tools.candidates()) != 0 {
				t.Fatalf("review = %#v", tools.review)
			}
		})
	}
}

// An approved change of an event that turns out to recur is refused too.
func TestApprovedUpdateOfARecurringEventIsRefused(t *testing.T) {
	series := nextGym()
	delete(series, "occurrence") // calendar_get returns the series itself
	tools := newCalendarTools("Move my workout to 9", series)
	approvedItem(tools, map[string]any{"tool": "calendar_update", "arguments": map[string]any{"eventQuery": "workout", "title": "Workout", "taskId": "series-gym"}})
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if contains(tools.calls, "calendar_update") || tools.review == nil || tools.review["errorSummary"] != recurringChangeReason || !reflect.DeepEqual(tools.gets, []string{"series-gym"}) {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

// A plan that sets up a repeating event goes to the owner: repeating events
// are created and changed only in the Calendar.
func TestPlannedRecurrenceIsRefused(t *testing.T) {
	for plan, reason := range map[string]string{
		`{"actions":[{"tool":"calendar_create","arguments":{"title":"Gym","allDay":false,"startAt":"2026-10-17T07:00:00+02:00","recurrence":{"frequency":"weekly","interval":1}}}],"confidence":0.95}`: recurringCreateReason,
		`{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"gym","recurrence":{"frequency":"daily","interval":1}}}],"confidence":0.95}`:                                                  recurringChangeReason,
	} {
		tools := newCalendarTools("Gym every Saturday at 7")
		runCalendarPlan(t, tools, plan)
		if contains(tools.calls, "calendar_create") || contains(tools.calls, "calendar_update") || len(tools.searches) != 0 || contains(tools.calls, "inbox_resolve") {
			t.Fatalf("plan %s: calls = %#v", plan, tools.calls)
		}
		if tools.review == nil || tools.review["errorSummary"] != reason || len(tools.candidates()) != 0 {
			t.Fatalf("plan %s: review = %#v", plan, tools.review)
		}
	}
}

func TestCalendarArgumentsCancelTheApprovedOccurrence(t *testing.T) {
	planned := map[string]any{"eventQuery": "gym", "onDate": "2026-10-10", "occurrences": map[string]any{"series-gym": "2026-10-10T05:00:00Z"}}
	if got := calendarArguments("calendar_cancel", planned, "series-gym"); !reflect.DeepEqual(got, map[string]any{"eventId": "series-gym", "recurrenceId": "2026-10-10T05:00:00Z"}) {
		t.Fatalf("cancel arguments = %#v", got)
	}
	if got := calendarArguments("calendar_cancel", planned, "event-dentist"); !reflect.DeepEqual(got, map[string]any{"eventId": "event-dentist"}) {
		t.Fatalf("another event's arguments = %#v", got)
	}
}
