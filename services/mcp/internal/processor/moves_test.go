package processor

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestValidMoveTakesOnlyARelativeShape(t *testing.T) {
	update := func(arguments map[string]any) plannedAction {
		arguments["taskQuery"] = "vet"
		return plannedAction{Tool: "task_update", Arguments: arguments}
	}
	for name, arguments := range map[string]map[string]any{
		"tomorrow, same time": {"move": map[string]any{"toDate": "2026-10-11"}},
		"an hour later":       {"move": map[string]any{"hours": 1.0}},
		"a date and a time":   {"move": map[string]any{"toDate": "2026-10-16", "toTime": "15:00"}},
		"no time":             {"move": map[string]any{"dateOnly": true}},
		"renamed and moved":   {"title": "Call the vet clinic", "move": map[string]any{"days": -1.0, "minutes": 30.0}},
		"no move at all":      {"dueAt": "2026-10-12T09:00:00+02:00"},
		"a null move":         {"move": nil, "dueAt": "2026-10-12T09:00:00+02:00"},
	} {
		if !validAction(update(arguments)) {
			t.Errorf("%s was refused", name)
		}
	}
	for name, arguments := range map[string]map[string]any{
		"a move and a due time":  {"dueAt": "2026-10-12T09:00:00+02:00", "move": map[string]any{"days": 1.0}},
		"a move and no due date": {"clearDueAt": true, "move": map[string]any{"days": 1.0}},
		"an empty move":          {"move": map[string]any{}},
		"a move in words":        {"move": "tomorrow"},
		"days in words":          {"move": map[string]any{"days": "1"}},
		"half an hour as hours":  {"move": map[string]any{"hours": 0.5}},
		"a date as a number":     {"move": map[string]any{"toDate": 20261011.0}},
		"an unknown field":       {"move": map[string]any{"weeks": 1.0}},
		"the model's timezone":   {"move": map[string]any{"days": 1.0, "timeZone": "UTC"}},
	} {
		if validAction(update(arguments)) {
			t.Errorf("%s was accepted", name)
		}
	}
	event := plannedAction{Tool: "calendar_update", Arguments: map[string]any{"eventQuery": "meeting", "move": map[string]any{"hours": 1.0}}}
	if !validAction(event) {
		t.Error("an event moved an hour later was refused")
	}
	event.Arguments["startAt"] = "2026-10-12T09:00:00+02:00"
	if validAction(event) {
		t.Error("an event move with a new start was accepted")
	}
	if validAction(plannedAction{Tool: "task_create", Arguments: map[string]any{"title": "x", "move": map[string]any{"days": 1.0}}}) {
		t.Error("task_create took a move")
	}
}

func TestATaskMoveGoesToTheOneTaskInTheCapturesTimezone(t *testing.T) {
	tools := newCalendarTools("Move the vet task to tomorrow, same time.")
	tools.taskMatches = map[string][]string{"vet": {"task-1"}}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"vet","taskQueryAlternatives":["Tierarzt","ветеринар"],"move":{"toDate":"2026-10-11"}}}],"confidence":0.95}`)
	want := map[string]any{"taskId": "task-1", "move": map[string]any{"toDate": "2026-10-11", "timeZone": "Europe/Berlin"}}
	if got := tools.inputs["task_update"]; !reflect.DeepEqual(got, want) || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("task_update = %#v, calls = %v", got, tools.calls)
	}
	// A null move is no move.
	plain := newCalendarTools("Move the vet task to Monday at 9.")
	plain.taskMatches = map[string][]string{"vet": {"task-1"}}
	runCalendarPlan(t, plain, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"vet","dueAt":"2026-10-12T09:00:00+02:00","move":null}}],"confidence":0.95}`)
	if got := plain.inputs["task_update"]; !reflect.DeepEqual(got, map[string]any{"taskId": "task-1", "dueAt": "2026-10-12T09:00:00+02:00"}) {
		t.Fatalf("task_update = %#v", got)
	}
	// A capture from a browser elsewhere means that browser's wall clock.
	away := newCalendarTools("Move the vet task an hour later.")
	away.item.SourceMetadata = map[string]any{"timeZone": "America/New_York"}
	away.taskMatches = map[string][]string{"vet": {"task-1"}}
	runCalendarPlan(t, away, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"vet","move":{"hours":1}}}],"confidence":0.95}`)
	if got := away.inputs["task_update"]["move"]; !reflect.DeepEqual(got, map[string]any{"hours": 1.0, "timeZone": "America/New_York"}) {
		t.Fatalf("move = %#v", got)
	}
}

func TestAnEventMoveKeepsItsLengthInMCP(t *testing.T) {
	review := timedEvent("event-1", "Budget review", "2026-10-13T11:00:00Z", "2026-10-13T11:45:00Z")
	tools := newCalendarTools("Push the budget review an hour later.", review)
	tools.matches = map[string][]string{"budget review": {"event-1"}}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"budget review","move":{"hours":1}}}],"confidence":0.95}`)
	// Only the move goes to MCP: no start or end is worked out here.
	want := map[string]any{"eventId": "event-1", "move": map[string]any{"hours": 1.0, "timeZone": "Europe/Berlin"}}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update = %#v", got)
	}
	// A recurring event is still changed only in the Calendar.
	gym := timedEvent("event-2", "Gym", "2026-10-13T05:00:00Z", "2026-10-13T06:00:00Z")
	gym["occurrence"] = map[string]any{"seriesId": "event-2", "recurrenceId": "2026-10-13T05:00:00Z"}
	recurring := newCalendarTools("Move gym an hour later.", gym)
	recurring.matches = map[string][]string{"gym": {"event-2"}}
	runCalendarPlan(t, recurring, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"gym","move":{"hours":1}}}],"confidence":0.95}`)
	if contains(recurring.calls, "calendar_update") || recurring.review["errorSummary"] != recurringChangeReason {
		t.Fatalf("calls = %v, review = %#v", recurring.calls, recurring.review)
	}
}

func TestAMoveCrossesDomainsLikeAReschedule(t *testing.T) {
	// "Push the electrician call an hour later" planned as an event change,
	// while it is a Task.
	tools := newCalendarTools("Push the electrician call an hour later.")
	tools.taskMatches = map[string][]string{"electrician": {"task-1"}}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"electrician","move":{"hours":1}}}],"confidence":0.95}`)
	want := map[string]any{"taskId": "task-1", "move": map[string]any{"hours": 1.0, "timeZone": "Europe/Berlin"}}
	if got := tools.inputs["task_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("task_update = %#v, calls = %v", got, tools.calls)
	}
	// And the other way: a Task move that finds only an event.
	review := timedEvent("event-1", "Budget review", "2026-10-13T11:00:00Z", "2026-10-13T11:45:00Z")
	other := newCalendarTools("Move the budget review to Friday.", review)
	other.matches = map[string][]string{"budget review": {"event-1"}}
	runCalendarPlan(t, other, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"budget review","move":{"toDate":"2026-10-16"}}}],"confidence":0.95}`)
	if got := other.inputs["calendar_update"]; !reflect.DeepEqual(got, map[string]any{"eventId": "event-1", "move": map[string]any{"toDate": "2026-10-16", "timeZone": "Europe/Berlin"}}) {
		t.Fatalf("calendar_update = %#v, calls = %v", got, other.calls)
	}
}

func TestAMoveThatDoesNotFitGoesToTheOwner(t *testing.T) {
	tools := newCalendarTools("Push the passport renewal an hour later.")
	tools.taskMatches = map[string][]string{"passport": {"task-1"}}
	tools.failTool = map[string]error{"task_update": errors.New("MCP tool task_update rejected: cannot move: “Renew passport” is due on Fri 23 Oct 2026 without a time, so it cannot move by hours or minutes: name the time (move.toTime)")}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"passport","move":{"hours":1}}}],"confidence":0.95}`)
	if want := "“Renew passport” is due on Fri 23 Oct 2026 without a time, so it cannot move by hours or minutes: name the time. Nothing was changed."; tools.review["errorSummary"] != want {
		t.Fatalf("review = %#v", tools.review)
	}
	for _, record := range tools.records {
		if record["status"] == "retryable" {
			t.Fatalf("a refused move was retried: %#v", tools.records)
		}
	}
	if _, ok := refusedMove(errors.New("MCP tool task_update rejected: service returned 400: dueAt is invalid")); ok {
		t.Error("another rejection was taken for a refused move")
	}
}

func TestMovesRunAfterThePlansOtherActions(t *testing.T) {
	move := plannedAction{Tool: "task_update", Arguments: map[string]any{"taskQuery": "vet", "move": map[string]any{"days": 1.0}}}
	update := plannedAction{Tool: "task_update", Arguments: map[string]any{"taskQuery": "plants", "dueAt": "2026-10-12T09:00:00+02:00"}}
	create := plannedAction{Tool: "task_create", Arguments: map[string]any{"title": "Call mom"}}
	got := movesLast(plan{Actions: []plannedAction{move, create, update}}).Actions
	if !reflect.DeepEqual(got, []plannedAction{create, update, move}) {
		t.Fatalf("order = %#v", got)
	}
}

func TestMovePromptExplainsRelativeMoves(t *testing.T) {
	// The benchmark's own captures stay out of the prompt.
	for _, scenario := range []string{"vet", "budget review", "parking", "expense"} {
		if strings.Contains(strings.ToLower(movePrompt), scenario) {
			t.Errorf("movePrompt names the benchmark's %q", scenario)
		}
	}
	for _, want := range []string{
		"task_update and calendar_update may use move instead of dueAt, clearDueAt, allDay, startAt, endAt, startDate, and endDate",
		`"Move the dentist task to tomorrow, same time" is move {"toDate": tomorrow's date}`,
		`"push the meeting an hour later" {"hours": 1}`,
		`which keeps the time it has (a Task without a time stays without one)`,
		`for task_update only, dateOnly true`,
		`with reason "Questions are not answered by capture: see the Tasks or Calendar view."`,
	} {
		if !strings.Contains(movePrompt, want) {
			t.Errorf("movePrompt lacks %q", want)
		}
	}
}
