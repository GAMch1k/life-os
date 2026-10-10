package processor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The benchmark regression: "Move the electrician call to 2026-10-21." with
// the Task "Call the electrician", planned as a Calendar change.
const electricianAsEventPlan = `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"electrician","eventQueryAlternatives":["Elektriker","электрик"],"startDate":"2026-10-21"}}],"confidence":0.9}`

func TestARescheduleOfATaskPlannedAsAnEventUpdatesTheTask(t *testing.T) {
	tools := newCalendarTools("Move the electrician call to 2026-10-21.")
	tools.taskMatches = map[string][]string{"electrician": {"task-1"}, "Elektriker": {"task-1"}}
	runCalendarPlan(t, tools, electricianAsEventPlan)

	// No manual event matched any term, so open Tasks were searched with the
	// same terms.
	if got, want := tools.terms(), []string{"electrician", "Elektriker", "электрик"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar terms = %q, want %q", got, want)
	}
	for _, search := range tools.taskSearches {
		if search.Get("completed") != "false" {
			t.Fatalf("task search %v must look at open Tasks", search)
		}
	}
	// A date-only start is due at the owner-local midnight of that date.
	want := map[string]any{"taskId": "task-1", "dueAt": "2026-10-21T00:00:00+02:00"}
	if got := tools.inputs["task_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("task_update input = %#v, want %#v; calls = %#v", got, want, tools.calls)
	}
	if contains(tools.calls, "calendar_update") || tools.review != nil || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	processed := tools.records[len(tools.records)-1]
	if action := processed["actions"].([]map[string]any)[0]; action["tool"] != "task_update" {
		t.Fatalf("audit = %#v", processed)
	}
}

func TestATimedRescheduleOfATaskKeepsItsTimeAndTitleAndDropsEventFields(t *testing.T) {
	tools := newCalendarTools("Move the electrician call to Monday at 9, at home.")
	tools.taskMatches = map[string][]string{"electrician": {"task-1"}}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"electrician","startAt":"2026-10-12T09:00:00+02:00","endAt":"2026-10-12T10:00:00+02:00","location":"Home","title":"Call the electrician (urgent)","description":"Ask about the fuse box","reminders":[]}}],"confidence":0.9}`)

	want := map[string]any{"taskId": "task-1", "dueAt": "2026-10-12T09:00:00+02:00", "title": "Call the electrician (urgent)", "notes": "Ask about the fuse box"}
	if got := tools.inputs["task_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("task_update input = %#v, want %#v", got, want)
	}
}

func TestARescheduleOfAnEventPlannedAsATaskUpdatesTheEvent(t *testing.T) {
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-de", "Zahnarzt", "2026-10-16T13:00:00Z", "2026-10-16T14:30:00Z"))
	tools.matches["Zahnarzt"] = []string{"event-de"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"dentist","taskQueryAlternatives":["Zahnarzt"],"dueAt":"2026-10-12T09:00:00+02:00","notes":"Bring the card"}}],"confidence":0.9}`)

	if len(tools.taskSearches) != 2 || len(tools.searches) != 2 {
		t.Fatalf("task searches = %v, calendar searches = %v", tools.taskSearches, tools.searches)
	}
	for _, search := range tools.searches {
		if search.Get("editable") != "true" || search.Has("sourceKind") {
			t.Fatalf("calendar search %v", search)
		}
	}
	// The event keeps its 90 minutes.
	want := map[string]any{"eventId": "event-de", "startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T10:30:00+02:00", "description": "Bring the card"}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v; calls = %#v", got, want, tools.calls)
	}
	if contains(tools.calls, "task_update") || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
}

func TestARescheduleOfAnAllDayEventPlannedAsATaskMovesItsDate(t *testing.T) {
	birthday := map[string]any{"id": "event-1", "title": "Anna's birthday", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16", "status": "active"}
	tools := newCalendarTools("Move Anna's birthday to Monday", birthday)
	tools.matches["birthday"] = []string{"event-1"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"birthday","dueAt":"2026-10-19T00:00:00+02:00"}}],"confidence":0.9}`)

	want := map[string]any{"eventId": "event-1", "startDate": "2026-10-19", "endDate": "2026-10-19"}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
}

func TestNoFallbackWhenThePlannedDomainMatches(t *testing.T) {
	// The event matches: Tasks are not searched, even though one would fit.
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-1", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	tools.matches["dentist"] = []string{"event-1"}
	tools.taskMatches = map[string][]string{"dentist": {"task-1"}}
	runCalendarPlan(t, tools, reschedulePlan)
	if len(tools.taskSearches) != 0 || contains(tools.calls, "task_update") || tools.inputs["calendar_update"]["eventId"] != "event-1" {
		t.Fatalf("calls = %#v, task searches = %v", tools.calls, tools.taskSearches)
	}

	// The Task matches: events are not searched, even though one would fit.
	tools = newCalendarTools("Move the electrician call to 2026-10-21.", timedEvent("event-2", "Electrician visit", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	tools.matches["electrician"] = []string{"event-2"}
	tools.taskMatches = map[string][]string{"electrician": {"task-1"}}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"electrician","dueAt":"2026-10-21T09:00:00+02:00"}}],"confidence":0.9}`)
	if len(tools.searches) != 0 || contains(tools.calls, "calendar_update") || tools.inputs["task_update"]["taskId"] != "task-1" {
		t.Fatalf("calls = %#v, calendar searches = %v", tools.calls, tools.searches)
	}
}

func TestSeveralTasksAfterTheFallbackGoToHumanReviewAndTheChoiceRuns(t *testing.T) {
	tools := newCalendarTools("Move the electrician call to 2026-10-21.")
	tools.taskMatches = map[string][]string{"electrician": {"task-1", "task-2"}}
	runCalendarPlan(t, tools, electricianAsEventPlan)

	if contains(tools.calls, "task_update") || contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("an ambiguous target was changed: %#v", tools.calls)
	}
	if tools.review["errorSummary"] != "ambiguous or missing Task selection" || !reflect.DeepEqual(tools.candidateIDs(), []string{"task-1", "task-2"}) {
		t.Fatalf("review = %#v", tools.review)
	}
	// The review proposes the Task change, so approving a Task runs it.
	proposed := tools.review["proposedAction"].(plannedAction)
	if proposed.Tool != "task_update" || proposed.Arguments["taskQuery"] != "electrician" || proposed.Arguments["dueAt"] != "2026-10-21T00:00:00+02:00" {
		t.Fatalf("proposed = %#v", proposed)
	}
	approved := newCalendarTools("Move the electrician call to 2026-10-21.")
	approved.item.ProcessingMetadata = map[string]any{"approvedAction": map[string]any{"tool": proposed.Tool, "arguments": map[string]any{"taskQueryAlternatives": proposed.Arguments["taskQueryAlternatives"], "dueAt": proposed.Arguments["dueAt"], "taskId": "task-2"}}}
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: approved, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := approved.inputs["task_update"]; !reflect.DeepEqual(got, map[string]any{"taskId": "task-2", "dueAt": "2026-10-21T00:00:00+02:00"}) {
		t.Fatalf("approved task_update = %#v, calls = %#v", got, approved.calls)
	}
}

func TestSeveralEventsAfterTheFallbackGoToHumanReview(t *testing.T) {
	tools := newCalendarTools("Move my meeting to Thursday at 14:00",
		timedEvent("event-team", "Team meeting", "2026-10-12T08:00:00Z", "2026-10-12T09:00:00Z"),
		timedEvent("event-anna", "Meeting with Anna", "2026-10-13T12:00:00Z", "2026-10-13T12:30:00Z"))
	tools.matches["meeting"] = []string{"event-team", "event-anna"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"meeting","dueAt":"2026-10-15T14:00:00+02:00"}}],"confidence":0.9}`)

	if contains(tools.calls, "calendar_update") || contains(tools.calls, "task_update") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review["errorSummary"] != "Several Calendar events match: choose the one to change" || !reflect.DeepEqual(tools.candidateIDs(), []string{"event-team", "event-anna"}) {
		t.Fatalf("review = %#v", tools.review)
	}
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Tool != "calendar_update" || proposed.Arguments["eventQuery"] != "meeting" || proposed.Arguments["startAt"] != "2026-10-15T14:00:00+02:00" {
		t.Fatalf("proposed = %#v", proposed)
	}
}

func TestNothingInEitherDomainGoesToHumanReviewWithThePlanAsGiven(t *testing.T) {
	tools := newCalendarTools("Move the electrician call to Monday at 9")
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"electrician","dueAt":"2026-10-12T09:00:00+02:00"}}],"confidence":0.9}`)
	if tools.review["errorSummary"] != "No open Task or upcoming Calendar event matches, so nothing was changed" || len(tools.candidates()) != 0 {
		t.Fatalf("review = %#v", tools.review)
	}
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Tool != "task_update" {
		t.Fatalf("proposed = %#v", proposed)
	}
}

func TestOnlyReschedulesCrossDomains(t *testing.T) {
	for _, tc := range []struct{ name, plan string }{
		{"cancel", `{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"electrician"}}],"confidence":0.95}`},
		{"event title only", `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"electrician","title":"Electrician"}}],"confidence":0.95}`},
		{"event location only", `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"electrician","location":"Home"}}],"confidence":0.95}`},
		{"complete", `{"actions":[{"tool":"task_complete","arguments":{"taskQuery":"electrician"}}],"confidence":0.95}`},
		{"reopen", `{"actions":[{"tool":"task_reopen","arguments":{"taskQuery":"electrician"}}],"confidence":0.95}`},
		{"task title only", `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"electrician","title":"Call the electrician today"}}],"confidence":0.95}`},
		{"unschedule", `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"electrician","clearDueAt":true}}],"confidence":0.95}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Both domains would match; only the planned one is searched.
			tools := newCalendarTools("electrician", timedEvent("event-1", "Electrician visit", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
			if tc.name == "complete" || tc.name == "reopen" || tc.name == "task title only" || tc.name == "unschedule" {
				tools.matches["electrician"] = []string{"event-1"}
			} else {
				tools.taskMatches = map[string][]string{"electrician": {"task-1"}}
			}
			runCalendarPlan(t, tools, tc.plan)
			for _, mutation := range []string{"task_update", "task_complete", "task_reopen", "calendar_update", "calendar_cancel"} {
				if contains(tools.calls, mutation) {
					t.Fatalf("%s crossed domains: %#v", tc.name, tools.calls)
				}
			}
			if tools.review == nil || len(tools.candidates()) != 0 {
				t.Fatalf("review = %#v", tools.review)
			}
			if len(tools.searches) > 0 && len(tools.taskSearches) > 0 {
				t.Fatalf("%s searched both domains: events %v, tasks %v", tc.name, tools.searches, tools.taskSearches)
			}
		})
	}
}

func TestLowConfidenceRescheduleOffersTheOtherDomainsMatches(t *testing.T) {
	tools := newCalendarTools("Move the electrician call to 2026-10-21.")
	tools.taskMatches = map[string][]string{"electrician": {"task-1"}}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"electrician","startDate":"2026-10-21"}}],"confidence":0.6}`)
	if contains(tools.calls, "task_update") || tools.review["errorSummary"] != "ambiguous or low-confidence Task selection" || !reflect.DeepEqual(tools.candidateIDs(), []string{"task-1"}) {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

func TestPlanPromptTellsTasksFromEvents(t *testing.T) {
	for _, want := range []string{
		"Tasks are to-dos (call, buy, renew, pay, …) with an optional due date",
		"Calendar events are appointments and meetings with a time and maybe a place",
		`"Move the electrician call to Friday" is task_update`,
	} {
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}

func TestADateOnlyRescheduleOfATimedEventKeepsItsTimeOfDay(t *testing.T) {
	// The Task plan is due at Berlin midnight, a date-only due date; the
	// matched event is Friday 15:00-16:30 summer time.
	for _, tc := range []struct{ name, dueAt, startAt, endAt string }{
		{"same offset", "2026-10-19T00:00:00+02:00", "2026-10-19T15:00:00+02:00", "2026-10-19T16:30:00+02:00"},
		// After summer time ends on 25 October: still 15:00 Berlin, still 90 minutes.
		{"across daylight saving", "2026-10-26T00:00:00+01:00", "2026-10-26T15:00:00+01:00", "2026-10-26T16:30:00+01:00"},
		{"midnight written in UTC", "2026-10-25T23:00:00Z", "2026-10-26T15:00:00+01:00", "2026-10-26T16:30:00+01:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := newCalendarTools("Move the dentist appointment to Monday", timedEvent("event-de", "Zahnarzt", "2026-10-16T13:00:00Z", "2026-10-16T14:30:00Z"))
			tools.matches["Zahnarzt"] = []string{"event-de"}
			runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"dentist","taskQueryAlternatives":["Zahnarzt"],"dueAt":"`+tc.dueAt+`"}}],"confidence":0.9}`)
			want := map[string]any{"eventId": "event-de", "startAt": tc.startAt, "endAt": tc.endAt}
			if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("calendar_update input = %#v, want %#v", got, want)
			}
		})
	}
	// A due time that is not midnight is a time: the event starts then.
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-de", "Zahnarzt", "2026-10-16T13:00:00Z", "2026-10-16T14:30:00Z"))
	tools.matches["Zahnarzt"] = []string{"event-de"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"Zahnarzt","dueAt":"2026-10-26T00:30:00+01:00"}}],"confidence":0.9}`)
	if got := tools.inputs["calendar_update"]["startAt"]; got != "2026-10-26T00:30:00+01:00" {
		t.Fatalf("startAt = %#v", got)
	}
}

func TestAnApprovedDateOnlyRescheduleKeepsTheChosenEventsTimeOfDay(t *testing.T) {
	// Several events matched after the fallback, so the review proposed the
	// new date; the owner chose one.
	tools := newCalendarTools("Move my meeting to Monday",
		timedEvent("event-team", "Team meeting", "2026-10-12T08:00:00Z", "2026-10-12T09:00:00Z"),
		timedEvent("event-anna", "Meeting with Anna", "2026-10-13T12:00:00Z", "2026-10-13T12:30:00Z"))
	tools.matches["meeting"] = []string{"event-team", "event-anna"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"meeting","dueAt":"2026-10-26T00:00:00+01:00"}}],"confidence":0.9}`)
	proposed := tools.review["proposedAction"].(plannedAction)
	if proposed.Arguments["startDate"] != "2026-10-26" {
		t.Fatalf("proposed = %#v", proposed)
	}
	approved := newCalendarTools("Move my meeting to Monday", timedEvent("event-anna", "Meeting with Anna", "2026-10-13T12:00:00Z", "2026-10-13T12:30:00Z"))
	approvedItem(approved, map[string]any{"tool": "calendar_update", "arguments": map[string]any{"eventQuery": "meeting", "startDate": "2026-10-26", "taskId": "event-anna"}})
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: approved, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"eventId": "event-anna", "startAt": "2026-10-26T14:00:00+01:00", "endAt": "2026-10-26T14:30:00+01:00"}
	if got := approved.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
}

func TestKeepTimeOfDayLeavesOtherChangesAlone(t *testing.T) {
	timed := timedEvent("e", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z")
	allDay := map[string]any{"allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16"}
	for _, tc := range []struct {
		name      string
		event     map[string]any
		arguments map[string]any
	}{
		{"all-day event", allDay, map[string]any{"startDate": "2026-10-19"}},
		{"switching to all day", timed, map[string]any{"allDay": true, "startDate": "2026-10-19"}},
		{"a time is given", timed, map[string]any{"startAt": "2026-10-19T09:00:00+02:00", "startDate": "2026-10-19"}},
		{"an end date is given", timed, map[string]any{"startDate": "2026-10-19", "endDate": "2026-10-20"}},
	} {
		before := map[string]any{}
		for field, value := range tc.arguments {
			before[field] = value
		}
		keepTimeOfDay(tc.arguments, tc.event, berlin(t))
		if !reflect.DeepEqual(tc.arguments, before) {
			t.Errorf("%s: %#v, want unchanged %#v", tc.name, tc.arguments, before)
		}
	}
}
