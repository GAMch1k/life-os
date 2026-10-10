package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// calendarTools serves one Inbox item and answers calendar_search per term,
// the way the Calendar search API matches words, recording each search, the
// Calendar mutations, and the Human Review they lead to.
type calendarTools struct {
	*fakeTools
	item inboxItem
	// events are the active events by id; matches maps a search term to ids.
	events  map[string]map[string]any
	matches map[string][]string
	// dayMatches answers searches bounded by to (one day's): a search term
	// to the items found, such as one occurrence of a recurring event.
	dayMatches map[string][]map[string]any
	// failSearch makes every calendar_search fail.
	failSearch bool
	// failTool makes a Calendar mutation fail with that error.
	failTool map[string]error
	// taskMatches maps a task_search term to the ids of open Tasks it finds.
	taskMatches  map[string][]string
	taskSearches []url.Values
	searches     []url.Values
	gets         []string
	review       map[string]any
	inputs       map[string]map[string]any
}

func (c *calendarTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	switch name {
	case "inbox_list":
		c.calls = append(c.calls, name)
		raw, _ := json.Marshal(inboxList{Items: []inboxItem{c.item}})
		return raw, nil
	case "calendar_search":
		c.calls = append(c.calls, name)
		if c.failSearch {
			return nil, errors.New("service unavailable: connection refused")
		}
		query, _ := url.ParseQuery(args["query"].(string))
		c.searches = append(c.searches, query)
		items := []map[string]any{}
		if query.Has("to") {
			items = append(items, c.dayMatches[query.Get("q")]...)
		} else {
			for _, id := range c.matches[query.Get("q")] {
				items = append(items, c.events[id])
			}
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return raw, nil
	case "task_search":
		c.calls = append(c.calls, name)
		query, _ := url.ParseQuery(args["query"].(string))
		c.taskSearches = append(c.taskSearches, query)
		items := []map[string]any{}
		for _, id := range c.taskMatches[query.Get("query")] {
			items = append(items, map[string]any{"id": id, "title": "Task " + id})
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return raw, nil
	case "calendar_get":
		c.calls = append(c.calls, name)
		id := args["eventId"].(string)
		c.gets = append(c.gets, id)
		raw, _ := json.Marshal(c.events[id])
		return raw, nil
	case "inbox_set_human_review":
		c.review = args
	case "calendar_update", "calendar_cancel", "calendar_create", "task_update", "task_create":
		if c.inputs == nil {
			c.inputs = map[string]map[string]any{}
		}
		c.inputs[name] = args
		if err := c.failTool[name]; err != nil {
			c.calls = append(c.calls, name)
			return nil, err
		}
	}
	return c.fakeTools.Call(ctx, name, args)
}

func (c *calendarTools) terms() []string {
	terms := []string{}
	for _, search := range c.searches {
		terms = append(terms, search.Get("q"))
	}
	return terms
}

func (c *calendarTools) candidates() []map[string]any {
	return c.review["candidates"].([]map[string]any)
}

func (c *calendarTools) candidateIDs() []string {
	ids := []string{}
	for _, candidate := range c.candidates() {
		ids = append(ids, candidate["id"].(string))
	}
	return ids
}

// The capture: Saturday 2026-10-10 10:00 in Berlin.
var capturedAt = time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)

func newCalendarTools(content string, events ...map[string]any) *calendarTools {
	tools := &calendarTools{fakeTools: &fakeTools{}, item: inboxItem{ID: "item-1", Content: content, Source: "web", SourceMetadata: map[string]any{}, CreatedAt: capturedAt}, events: map[string]map[string]any{}, matches: map[string][]string{}}
	for _, event := range events {
		tools.events[event["id"].(string)] = event
	}
	return tools
}

func timedEvent(id, title, startAt, endAt string) map[string]any {
	return map[string]any{"id": id, "title": title, "allDay": false, "startAt": startAt, "endAt": endAt, "status": "active", "source": map[string]any{"kind": "manual"}}
}

func runCalendarPlan(t *testing.T, tools *calendarTools, plan string) {
	t.Helper()
	runner := Runner{Planner: fakePlanner{output: plan}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

const reschedulePlan = `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"dentist","eventQueryAlternatives":["Zahnarzt","стоматолог"],"startAt":"2026-10-12T09:00:00+02:00"}}],"confidence":0.95}`

func TestCalendarUpdateFindsTheOneEventAcrossLanguagesAndKeepsItsLength(t *testing.T) {
	// "Move the dentist appointment to Monday at 9"; the only dentist event
	// is titled "Zahnarzt", Friday 15:00-16:30 Berlin.
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-de", "Zahnarzt", "2026-10-16T13:00:00Z", "2026-10-16T14:30:00Z"))
	tools.matches["Zahnarzt"] = []string{"event-de"}
	runCalendarPlan(t, tools, reschedulePlan)

	if got, want := tools.terms(), []string{"dentist", "Zahnarzt", "стоматолог"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("searched %q, want %q", got, want)
	}
	// From the start of the capture's day in Berlin, in Berlin, active manual
	// events only: imported ones are managed by their source.
	for _, search := range tools.searches {
		if search.Get("from") != "2026-10-09T22:00:00Z" || search.Get("timeZone") != "Europe/Berlin" || search.Get("limit") != "10" || search.Has("includeCancelled") || search.Get("sourceKind") != "manual" {
			t.Fatalf("search %v", search)
		}
	}
	want := map[string]any{"eventId": "event-de", "startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T10:30:00+02:00"}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
	if !contains(tools.calls, "inbox_resolve") || tools.review != nil || contains(tools.calls, "calendar_create") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	// The audit names the event that was changed.
	processed := tools.records[len(tools.records)-1]
	if processed["status"] != "processed" || processed["actions"].([]map[string]any)[0]["arguments"].(map[string]any)["eventId"] != "event-de" {
		t.Fatalf("audit = %#v", processed)
	}
}

func TestCalendarUpdateSetsThePriorityOfTheOneEvent(t *testing.T) {
	// "Das Visum-Gespräch ist wichtig": only the priority changes.
	tools := newCalendarTools("Das Visum-Gespräch ist wichtig", timedEvent("event-visa", "Visa appointment", "2026-10-16T07:00:00Z", "2026-10-16T08:00:00Z"))
	tools.matches["Visa"] = []string{"event-visa"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"Visum","eventQueryAlternatives":["Visa","виз"],"priority":"high"}}],"confidence":0.95}`)
	want := map[string]any{"eventId": "event-visa", "priority": "high"}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
	if !contains(tools.calls, "inbox_resolve") || tools.review != nil {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

func TestCalendarUpdateWithSeveralMatchesGoesToHumanReviewWithCandidates(t *testing.T) {
	tools := newCalendarTools("Move my meeting to Thursday at 14:00",
		timedEvent("event-team", "Team meeting", "2026-10-12T08:00:00Z", "2026-10-12T09:00:00Z"),
		timedEvent("event-anna", "Meeting with Anna", "2026-10-13T12:00:00Z", "2026-10-13T12:30:00Z"))
	tools.events["event-anna"]["location"] = "Café Rosa"
	tools.matches["meeting"] = []string{"event-team", "event-anna"}
	tools.matches["Besprechung"] = []string{"event-team"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"meeting","eventQueryAlternatives":["Besprechung"],"startAt":"2026-10-15T14:00:00+02:00"}}],"confidence":0.9}`)

	if contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("an ambiguous event was changed: %#v", tools.calls)
	}
	if tools.review == nil || tools.review["errorSummary"] != "Several Calendar events match: choose the one to change" {
		t.Fatalf("review = %#v", tools.review)
	}
	if got, want := tools.candidateIDs(), []string{"event-team", "event-anna"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
	// The Dashboard shows a candidate's title: it says when and where.
	labels := []string{tools.candidates()[0]["title"].(string), tools.candidates()[1]["title"].(string)}
	if want := []string{"Team meeting · Mon 12 Oct 2026 10:00–11:00", "Meeting with Anna · Tue 13 Oct 2026 14:00–14:30 · Café Rosa"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %q, want %q", labels, want)
	}
	if tools.candidates()[1]["eventTitle"] != "Meeting with Anna" || tools.candidates()[1]["kind"] != "calendar_event" {
		t.Fatalf("candidate = %#v", tools.candidates()[1])
	}
	// The proposed action keeps the search terms and the change.
	proposed := tools.review["proposedAction"].(plannedAction)
	if proposed.Tool != "calendar_update" || proposed.Arguments["eventQuery"] != "meeting" || proposed.Arguments["startAt"] != "2026-10-15T14:00:00+02:00" {
		t.Fatalf("proposed = %#v", proposed)
	}
}

func TestCalendarUpdateWithNoMatchGoesToHumanReviewAndCreatesNothing(t *testing.T) {
	tools := newCalendarTools("Move the dentist appointment to Monday at 9")
	runCalendarPlan(t, tools, reschedulePlan)

	if contains(tools.calls, "calendar_update") || contains(tools.calls, "calendar_create") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review == nil || len(tools.candidates()) != 0 || tools.review["errorSummary"] != "No upcoming Calendar event or open Task matches, so nothing was changed" {
		t.Fatalf("review = %#v", tools.review)
	}
}

func TestCalendarCancelAlwaysWaitsForTheOwnersApproval(t *testing.T) {
	// Exactly one event matches, and still nothing is cancelled.
	tools := newCalendarTools("Cancel the dentist appointment", timedEvent("event-1", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	tools.matches["dentist"] = []string{"event-1"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"dentist","eventQueryAlternatives":["Zahnarzt"]}}],"confidence":0.97}`)

	if contains(tools.calls, "calendar_cancel") || contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("an AI-proposed cancellation ran without approval: %#v", tools.calls)
	}
	if tools.review == nil || !reflect.DeepEqual(tools.candidateIDs(), []string{"event-1"}) {
		t.Fatalf("review = %#v", tools.review)
	}
	// Imported events are cancelled only by their source.
	for _, search := range tools.searches {
		if search.Get("sourceKind") != "manual" {
			t.Fatalf("cancel searched %v", search)
		}
	}
	if summary := tools.review["errorSummary"].(string); !strings.Contains(summary, "needs your approval") {
		t.Fatalf("errorSummary = %q", summary)
	}
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Tool != "calendar_cancel" {
		t.Fatalf("proposed = %#v", proposed)
	}
}

func TestCalendarCancelWithSeveralOrNoMatchesAlsoGoesToHumanReview(t *testing.T) {
	for name, matches := range map[string][]string{"several": {"event-1", "event-2"}, "none": nil} {
		t.Run(name, func(t *testing.T) {
			tools := newCalendarTools("Cancel my meeting", timedEvent("event-1", "Team meeting", "2026-10-12T08:00:00Z", "2026-10-12T09:00:00Z"), timedEvent("event-2", "Meeting with Anna", "2026-10-13T12:00:00Z", "2026-10-13T13:00:00Z"))
			tools.matches["meeting"] = matches
			runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"meeting"}}],"confidence":0.9}`)
			if contains(tools.calls, "calendar_cancel") || tools.review == nil || len(tools.candidates()) != len(matches) {
				t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
			}
		})
	}
}

// approvedItem is an item the owner approved in Human Review: Inbox keeps
// the proposed action, drops taskQuery, and records the chosen candidate as
// taskId.
func approvedItem(tools *calendarTools, action map[string]any) {
	tools.item.ProcessingMetadata = map[string]any{"approvedAction": action}
}

func TestApprovedCalendarCancelRunsOnTheChosenEventOnly(t *testing.T) {
	tools := newCalendarTools("Cancel the dentist appointment", timedEvent("event-1", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	approvedItem(tools, map[string]any{"tool": "calendar_cancel", "arguments": map[string]any{"eventQuery": "dentist", "eventQueryAlternatives": []any{"Zahnarzt"}, "taskId": "event-1"}})
	runner := Runner{Planner: fakePlanner{err: errors.New("an approved item is not planned again")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tools.searches) != 0 {
		t.Fatalf("an approved event was searched again: %v", tools.searches)
	}
	if got := tools.inputs["calendar_cancel"]; !reflect.DeepEqual(got, map[string]any{"eventId": "event-1"}) {
		t.Fatalf("calendar_cancel input = %#v, calls = %#v", got, tools.calls)
	}
	if !contains(tools.calls, "inbox_resolve") || tools.review != nil {
		t.Fatalf("calls = %#v", tools.calls)
	}
}

func TestApprovedCalendarUpdateReadsTheChosenEventToKeepItsLength(t *testing.T) {
	tools := newCalendarTools("Move my meeting to Thursday at 14:00", timedEvent("event-anna", "Meeting with Anna", "2026-10-13T12:00:00Z", "2026-10-13T12:30:00Z"))
	approvedItem(tools, map[string]any{"tool": "calendar_update", "arguments": map[string]any{"eventQuery": "meeting", "startAt": "2026-10-15T14:00:00+02:00", "taskId": "event-anna"}})
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tools.searches) != 0 || !reflect.DeepEqual(tools.gets, []string{"event-anna"}) {
		t.Fatalf("searches = %v, gets = %v", tools.searches, tools.gets)
	}
	want := map[string]any{"eventId": "event-anna", "startAt": "2026-10-15T14:00:00+02:00", "endAt": "2026-10-15T14:30:00+02:00"}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
	if !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
}

func TestAPlannedCalendarChangeCannotNameItsEvent(t *testing.T) {
	// Only the owner's Human Review choice may name an event; a capture
	// that carries an id (for example a prompt injection) is rejected.
	for _, plan := range []string{
		`{"actions":[{"tool":"calendar_cancel","arguments":{"eventQuery":"dentist","eventId":"event-1"}}],"confidence":0.95}`,
		`{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"dentist","taskId":"event-1","title":"x"}}],"confidence":0.95}`,
		`{"actions":[{"tool":"calendar_cancel","arguments":{"eventId":"event-1"}}],"confidence":0.95}`,
	} {
		tools := newCalendarTools("Cancel the dentist appointment", timedEvent("event-1", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
		tools.matches["dentist"] = []string{"event-1"}
		runCalendarPlan(t, tools, plan)
		if contains(tools.calls, "calendar_cancel") || contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_resolve") {
			t.Fatalf("plan %s mutated: %#v", plan, tools.calls)
		}
		if last := tools.records[len(tools.records)-1]; last["status"] != "retryable" {
			t.Fatalf("plan %s: audit = %#v", plan, tools.records)
		}
	}
}

func TestLowConfidenceCalendarChangeOffersTheMatchingEvents(t *testing.T) {
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-de", "Zahnarzt", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	tools.matches["Zahnarzt"] = []string{"event-de"}
	runCalendarPlan(t, tools, strings.Replace(reschedulePlan, `"confidence":0.95`, `"confidence":0.6`, 1))

	if contains(tools.calls, "calendar_update") || tools.review == nil || !reflect.DeepEqual(tools.candidateIDs(), []string{"event-de"}) {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if tools.review["errorSummary"] != "ambiguous or low-confidence Calendar event selection" {
		t.Fatalf("review = %#v", tools.review)
	}
}

func TestCalendarSearchFailureIsRetriedAndChangesNothing(t *testing.T) {
	tools := newCalendarTools("Move the dentist appointment to Monday at 9")
	tools.failSearch = true
	runCalendarPlan(t, tools, reschedulePlan)
	if contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_set_human_review") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if last := tools.records[len(tools.records)-1]; last["status"] != "retryable" || !strings.HasPrefix(last["errorSummary"].(string), "calendar search failed") {
		t.Fatalf("audit = %#v", tools.records)
	}
}

func TestCalendarSearchUsesTheCapturesOwnTimeZone(t *testing.T) {
	// Captured at 08:00 UTC from New York: 04:00 there, still Saturday.
	tools := newCalendarTools("Move the dentist appointment to Monday at 9")
	tools.item.SourceMetadata["timeZone"] = "America/New_York"
	runCalendarPlan(t, tools, reschedulePlan)
	if search := tools.searches[0]; search.Get("from") != "2026-10-10T04:00:00Z" || search.Get("timeZone") != "America/New_York" {
		t.Fatalf("search = %v", search)
	}
}

func TestExistingCalendarCreatePlansStillWork(t *testing.T) {
	// A plan in the shape used before Calendar changes existed, and one with
	// the new optional fields.
	for _, raw := range []string{
		`{"actions":[{"tool":"calendar_create","arguments":{"title":"Dentist","allDay":false,"startAt":"2026-10-16T15:00:00+02:00","endAt":"2026-10-16T16:00:00+02:00"}}],"confidence":0.95}`,
		`{"actions":[{"tool":"calendar_create","arguments":{"title":"Anna's birthday","description":"Bring flowers","allDay":true,"startDate":"2026-10-12","endDate":"2026-10-12"}}],"confidence":0.95}`,
		`{"actions":[{"tool":"calendar_create","arguments":{"title":"Dentist","location":"Dr. Weber","allDay":false,"startAt":"2026-10-16T15:00:00+02:00","endAt":"2026-10-16T16:00:00+02:00","reminders":[{"kind":"relative","minutesBefore":30}]}}],"confidence":0.95}`,
		`{"actions":[{"tool":"calendar_create","arguments":{"title":"Visa appointment","allDay":false,"startAt":"2026-10-16T09:00:00+02:00","endAt":"2026-10-16T10:00:00+02:00","priority":"high"}}],"confidence":0.95}`,
	} {
		tools := newCalendarTools("Add dentist")
		runCalendarPlan(t, tools, raw)
		input := tools.inputs["calendar_create"]
		if input == nil || input["idempotencyKey"] == "" || !contains(tools.calls, "inbox_resolve") || len(tools.searches) != 0 {
			t.Fatalf("plan %s: calls = %#v, input = %#v", raw, tools.calls, input)
		}
		var parsed plan
		_ = json.Unmarshal([]byte(raw), &parsed)
		for field, value := range parsed.Actions[0].Arguments {
			if !reflect.DeepEqual(input[field], value) {
				t.Fatalf("plan %s: calendar_create %s = %#v, want %#v", raw, field, input[field], value)
			}
		}
	}
}

func TestValidActionChecksCalendarChanges(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		arguments map[string]any
		valid     bool
	}{
		{"reschedule", "calendar_update", map[string]any{"eventQuery": "dentist", "startAt": "2026-10-12T09:00:00+02:00"}, true},
		{"alternatives", "calendar_update", map[string]any{"eventQuery": "dentist", "eventQueryAlternatives": []any{"Zahnarzt"}, "location": "Praxis Weber"}, true},
		{"clear", "calendar_update", map[string]any{"eventQuery": "dentist", "clear": []any{"location", "reminders"}}, true},
		{"reminders", "calendar_update", map[string]any{"eventQuery": "dentist", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 30.0}}}, true},
		{"no change", "calendar_update", map[string]any{"eventQuery": "dentist"}, false},
		{"no query", "calendar_update", map[string]any{"startAt": "2026-10-12T09:00:00+02:00"}, false},
		{"blank query", "calendar_update", map[string]any{"eventQuery": " ", "title": "x"}, false},
		{"blank title", "calendar_update", map[string]any{"eventQuery": "dentist", "title": ""}, false},
		{"clear title", "calendar_update", map[string]any{"eventQuery": "dentist", "clear": []any{"title"}}, false},
		{"set and clear", "calendar_update", map[string]any{"eventQuery": "dentist", "location": "x", "clear": []any{"location"}}, false},
		{"status", "calendar_update", map[string]any{"eventQuery": "dentist", "status": "cancelled"}, false},
		{"bad alternatives", "calendar_update", map[string]any{"eventQuery": "dentist", "eventQueryAlternatives": "Zahnarzt", "title": "x"}, false},
		{"bad reminders", "calendar_update", map[string]any{"eventQuery": "dentist", "reminders": "30 minutes"}, false},
		{"defaults off", "calendar_update", map[string]any{"eventQuery": "dentist", "useDefaultReminders": false}, true},
		{"bad defaults flag", "calendar_update", map[string]any{"eventQuery": "dentist", "useDefaultReminders": "no"}, false},
		{"create with own reminders only", "calendar_create", map[string]any{"title": "Dentist", "allDay": true, "useDefaultReminders": false, "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 10.0}}}, true},
		{"create with a bad defaults flag", "calendar_create", map[string]any{"title": "Dentist", "allDay": true, "useDefaultReminders": "yes"}, false},
		{"create important", "calendar_create", map[string]any{"title": "Visa", "allDay": true, "priority": "high"}, true},
		{"create unimportant", "calendar_create", map[string]any{"title": "Visa", "allDay": true, "priority": "low"}, true},
		{"create with an unknown priority", "calendar_create", map[string]any{"title": "Visa", "allDay": true, "priority": "urgent"}, false},
		{"create with a numeric priority", "calendar_create", map[string]any{"title": "Visa", "allDay": true, "priority": 3.0}, false},
		{"priority alone", "calendar_update", map[string]any{"eventQuery": "dentist", "priority": "high"}, true},
		{"update with an unknown priority", "calendar_update", map[string]any{"eventQuery": "dentist", "priority": "wichtig"}, false},
		{"clear priority", "calendar_update", map[string]any{"eventQuery": "dentist", "clear": []any{"priority"}}, false},
		{"cancel", "calendar_cancel", map[string]any{"eventQuery": "dentist", "eventQueryAlternatives": []any{"Zahnarzt"}}, true},
		{"cancel with change", "calendar_cancel", map[string]any{"eventQuery": "dentist", "title": "x"}, false},
		{"cancel on a day", "calendar_cancel", map[string]any{"eventQuery": "gym", "onDate": "2026-10-10"}, true},
		{"cancel on no date", "calendar_cancel", map[string]any{"eventQuery": "gym", "onDate": "today"}, false},
		{"cancel on a number", "calendar_cancel", map[string]any{"eventQuery": "gym", "onDate": 10.0}, false},
		{"approved occurrences", "calendar_cancel", map[string]any{"eventQuery": "gym", "occurrences": map[string]any{"series-1": "2026-10-10T05:00:00Z"}}, true},
		{"bad occurrences", "calendar_cancel", map[string]any{"eventQuery": "gym", "occurrences": map[string]any{"series-1": 1.0}}, false},
		{"occurrences not a map", "calendar_cancel", map[string]any{"eventQuery": "gym", "occurrences": []any{"x"}}, false},
		{"update on a day", "calendar_update", map[string]any{"eventQuery": "gym", "onDate": "2026-10-10", "title": "x"}, false},
		{"create repeating", "calendar_create", map[string]any{"title": "Gym", "allDay": true, "recurrence": map[string]any{"frequency": "weekly"}}, false},
		{"create with location", "calendar_create", map[string]any{"title": "Dentist", "location": "Dr. Weber", "allDay": true, "startDate": "2026-10-12", "endDate": "2026-10-12", "timeZone": "Europe/Berlin"}, true},
		{"create with bad reminders", "calendar_create", map[string]any{"title": "Dentist", "allDay": true, "reminders": []any{"soon"}}, false},
		{"create with search terms", "calendar_create", map[string]any{"title": "Dentist", "allDay": true, "eventQuery": "dentist"}, false},
		{"delete is not planned", "calendar_delete", map[string]any{"eventQuery": "dentist"}, false},
	} {
		if got := validAction(plannedAction{Tool: tc.tool, Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

func TestKeepDurationCompletesAReschedule(t *testing.T) {
	timed := timedEvent("e", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:30:00Z")
	allDay := map[string]any{"id": "e", "title": "Trip", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-18"}
	for _, tc := range []struct {
		name      string
		event     map[string]any
		arguments map[string]any
		want      map[string]any
	}{
		{"timed keeps its length", timed, map[string]any{"startAt": "2026-10-12T09:00:00+02:00"}, map[string]any{"startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T10:30:00+02:00"}},
		// Moved across the end of summer time: 90 minutes stay 90 minutes.
		{"across daylight saving", timed, map[string]any{"startAt": "2026-10-26T09:00:00+01:00"}, map[string]any{"startAt": "2026-10-26T09:00:00+01:00", "endAt": "2026-10-26T10:30:00+01:00"}},
		{"an explicit end is kept", timed, map[string]any{"startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T09:15:00+02:00"}, map[string]any{"startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T09:15:00+02:00"}},
		{"all-day keeps its days", allDay, map[string]any{"startDate": "2026-11-01"}, map[string]any{"startDate": "2026-11-01", "endDate": "2026-11-03"}},
		{"to all day on one day", timed, map[string]any{"allDay": true, "startDate": "2026-10-20"}, map[string]any{"allDay": true, "startDate": "2026-10-20", "endDate": "2026-10-20"}},
		{"to timed is left to the plan", allDay, map[string]any{"allDay": false, "startAt": "2026-10-20T09:00:00+02:00"}, map[string]any{"allDay": false, "startAt": "2026-10-20T09:00:00+02:00"}},
		{"a title change has no times", timed, map[string]any{"title": "Dentist (Dr. Weber)"}, map[string]any{"title": "Dentist (Dr. Weber)"}},
	} {
		keepDuration(tc.arguments, tc.event)
		if !reflect.DeepEqual(tc.arguments, tc.want) {
			t.Errorf("%s: %#v, want %#v", tc.name, tc.arguments, tc.want)
		}
	}
}

func TestCompleteCreateGivesANewEventItsDefaultLength(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		want      map[string]any
	}{
		{"timed lasts an hour", map[string]any{"startAt": "2026-10-11T11:00:00+02:00"}, map[string]any{"startAt": "2026-10-11T11:00:00+02:00", "endAt": "2026-10-11T12:00:00+02:00"}},
		{"in UTC", map[string]any{"startAt": "2026-10-25T00:30:00Z"}, map[string]any{"startAt": "2026-10-25T00:30:00Z", "endAt": "2026-10-25T01:30:00Z"}},
		{"all-day is one day", map[string]any{"allDay": true, "startDate": "2026-10-12"}, map[string]any{"allDay": true, "startDate": "2026-10-12", "endDate": "2026-10-12"}},
		{"an explicit end is kept", map[string]any{"startAt": "2026-10-11T11:00:00+02:00", "endAt": "2026-10-11T11:30:00+02:00"}, map[string]any{"startAt": "2026-10-11T11:00:00+02:00", "endAt": "2026-10-11T11:30:00+02:00"}},
		{"an unreadable start is left to Calendar", map[string]any{"startAt": "Sunday at 11"}, map[string]any{"startAt": "Sunday at 11"}},
	} {
		completeCreate(tc.arguments)
		if !reflect.DeepEqual(tc.arguments, tc.want) {
			t.Errorf("%s: %#v, want %#v", tc.name, tc.arguments, tc.want)
		}
	}
}

func TestACalendarCreateWithoutAnEndLastsAnHour(t *testing.T) {
	tools := newCalendarTools("Add a haircut on Sunday at 11:00, remind me a day before and 3 hours before.")
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_create","arguments":{"title":"Haircut","allDay":false,"startAt":"2026-10-11T11:00:00+02:00","reminders":[{"kind":"relative","minutesBefore":1440},{"kind":"relative","minutesBefore":180}]}}],"confidence":0.95}`)
	input := tools.inputs["calendar_create"]
	if input == nil || input["endAt"] != "2026-10-11T12:00:00+02:00" || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, input = %#v", tools.calls, input)
	}
}

func TestCalendarArgumentsSendOnlyTheEventAndTheChange(t *testing.T) {
	planned := map[string]any{"eventQuery": "dentist", "eventQueryAlternatives": []any{"Zahnarzt"}, "taskId": "x", "location": nil, "description": "Bring the card", "clear": []any{"reminders"}, "startAt": nil}
	want := map[string]any{"eventId": "event-1", "description": "Bring the card", "clear": []any{"location", "reminders"}}
	if got := calendarArguments("calendar_update", planned, "event-1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("update arguments = %#v, want %#v", got, want)
	}
	if got := calendarArguments("calendar_cancel", planned, "event-1"); !reflect.DeepEqual(got, map[string]any{"eventId": "event-1"}) {
		t.Fatalf("cancel arguments = %#v", got)
	}
}

func TestEventLabelsReadInTheOwnersTimeZone(t *testing.T) {
	for _, tc := range []struct {
		event map[string]any
		want  string
	}{
		{timedEvent("e", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"), "Dentist · Fri 16 Oct 2026 15:00–16:00"},
		{timedEvent("e", "Night train", "2026-10-16T20:00:00Z", "2026-10-17T06:00:00Z"), "Night train · Fri 16 Oct 2026 22:00 – Sat 17 Oct 2026 08:00"},
		{map[string]any{"title": "Anna's birthday", "allDay": true, "startDate": "2026-10-12", "endDate": "2026-10-12"}, "Anna's birthday · Mon 12 Oct 2026 (all day)"},
		{map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-18", "location": "Hamburg"}, "Trip · Fri 16 Oct 2026 – Sun 18 Oct 2026 (all day) · Hamburg"},
	} {
		if got := eventLabel(tc.event, berlin(t)); got != tc.want {
			t.Errorf("label = %q, want %q", got, tc.want)
		}
	}
}

func TestPlanPromptAllowsCalendarChangesThroughSearchTermsAndReview(t *testing.T) {
	for _, want := range []string{
		"calendar_create|calendar_update|calendar_cancel|",
		"location (where it takes place",
		`eventQuery "dentist", eventQueryAlternatives ["Zahnarzt", "стоматолог", "зубн"]`,
		"changes one only when exactly one matches, otherwise it asks the owner, so you do not need to know the existing events; the owner approves every cancellation",
		"calendar_cancel may only use eventQuery, eventQueryAlternatives, and onDate (the ISO date of the day the item names",
		"for a repeating event the processor cancels only its occurrence on that day, else its next one",
		`reason "Recurring events are created in the Calendar."`,
		"the processor otherwise keeps the event's length",
		"Never create an event as a fallback for an uncertain existing event.",
		`and priority (only when the item says how important it is: "high" for important or urgent, such as "wichtig" or "важно", "low" for not important or low priority; otherwise leave it out)`,
		"useDefaultReminders, priority, and clear",
	} {
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
	if strings.Contains(planPrompt, "Never propose calendar update") || strings.Contains(planPrompt, "calendar_delete") {
		t.Error("planPrompt still forbids calendar changes or offers permanent deletion")
	}
}

// managedBySourceError is how a Calendar 409 managed_by_source reaches the
// processor through MCP.
var managedBySourceError = errors.New(`MCP tool calendar_update rejected: service returned 409: {"error":"this event is managed by its source","code":"managed_by_source"}`)

func TestAChangeCalendarRefusesForAnImportedEventGoesToHumanReviewNotRetry(t *testing.T) {
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-de", "Zahnarzt", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	tools.matches["Zahnarzt"] = []string{"event-de"}
	tools.failTool = map[string]error{"calendar_update": managedBySourceError}
	runCalendarPlan(t, tools, reschedulePlan)

	if tools.review == nil || tools.review["errorSummary"] != "This event is managed by its source and can't be changed by capture" || len(tools.candidates()) != 0 {
		t.Fatalf("review = %#v", tools.review)
	}
	if contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	for _, record := range tools.records {
		if record["status"] == "retryable" || record["status"] == "failed" {
			t.Fatalf("a source-managed event entered technical retry: %#v", tools.records)
		}
	}
}

func TestAnApprovedCancelCalendarRefusesGoesToHumanReviewNotRetry(t *testing.T) {
	tools := newCalendarTools("Cancel the waste pickup")
	approvedItem(tools, map[string]any{"tool": "calendar_cancel", "arguments": map[string]any{"eventQuery": "pickup", "taskId": "event-ics"}})
	tools.failTool = map[string]error{"calendar_cancel": errors.New(`MCP tool calendar_cancel rejected: service returned 409: {"code":"managed_by_source"}`)}
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tools.review == nil || tools.review["errorSummary"] != managedBySourceReason || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

func TestOtherCalendarConflictsAreStillRetried(t *testing.T) {
	// A concurrent change (409 without managed_by_source) may succeed later.
	tools := newCalendarTools("Move the dentist appointment to Monday at 9", timedEvent("event-1", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z"))
	tools.matches["dentist"] = []string{"event-1"}
	tools.failTool = map[string]error{"calendar_update": errors.New(`MCP tool calendar_update rejected: service returned 409: {"error":"the event kept changing; retry"}`)}
	runCalendarPlan(t, tools, reschedulePlan)
	if tools.review != nil {
		t.Fatalf("review = %#v", tools.review)
	}
	if last := tools.records[len(tools.records)-1]; last["status"] != "retryable" {
		t.Fatalf("audit = %#v", tools.records)
	}
}
