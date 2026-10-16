package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

const (
	taskID       = "8a1f0c2e-5b6d-4e7f-9a0b-1c2d3e4f5a6b"
	recurrenceID = "2026-10-17T05:00:00Z"
)

// fakeAPI is a small owning service: GET answers the object stored at the
// path (or a list), and PATCH merges the body into it, as Tasks and
// Calendar do. It records every request.
type fakeAPI struct {
	objects  map[string]map[string]any
	lists    map[string]string
	requests []calendarRequest
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	request := calendarRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Actor: r.Header.Get(activity.ActorHeader), RawBody: string(raw)}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &request.Body)
	}
	f.requests = append(f.requests, request)
	object, found := f.objects[r.URL.Path]
	switch {
	case r.Method == http.MethodGet && !found && f.lists[r.URL.Path] != "":
		_, _ = w.Write([]byte(f.lists[r.URL.Path]))
	case !found:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	case r.Method == http.MethodPatch:
		for key, value := range request.Body {
			object[key] = value
		}
		_ = json.NewEncoder(w).Encode(object)
	default:
		_ = json.NewEncoder(w).Encode(object)
	}
}

// writes are the requests that changed something.
func (f *fakeAPI) writes() []calendarRequest {
	var writes []calendarRequest
	for _, request := range f.requests {
		if request.Method != http.MethodGet {
			writes = append(writes, request)
		}
	}
	return writes
}

// toolSession connects an MCP client to a server whose tools add registers,
// backed by api.
func toolSession(t *testing.T, api *fakeAPI, add func(*mcp.Server, *http.Client, string)) *mcp.ClientSession {
	t.Helper()
	service := httptest.NewServer(api)
	t.Cleanup(service.Close)
	server := mcp.NewServer(&mcp.Implementation{Name: "life-os-test", Version: "0"}, nil)
	add(server, service.Client(), service.URL)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func taskToolSession(t *testing.T, api *fakeAPI) *mcp.ClientSession {
	return toolSession(t, api, func(s *mcp.Server, hc *http.Client, url string) {
		addTaskTools(s, hc, url, activity.NewEmitter("", "mcp", nil), testOwner(t))
	})
}

func tasksWith(task map[string]any) *fakeAPI {
	task["id"] = taskID
	return &fakeAPI{objects: map[string]map[string]any{"/v1/tasks/" + taskID: task}}
}

func data(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool failed: %s", resultText(result))
	}
	out, _ := result.StructuredContent.(map[string]any)["data"].(map[string]any)
	return out
}

func TestTaskGetGivesEveryDetailOnTheOwnersClock(t *testing.T) {
	api := tasksWith(map[string]any{"title": "Call the vet", "notes": "Ask about Mia's vaccine", "completed": false, "dueAt": "2026-10-19T13:30:00Z", "createdAt": "2026-10-10T07:00:00Z", "updatedAt": "2026-10-11T07:00:00Z"})
	session := taskToolSession(t, api)
	task := data(t, callTool(t, session, "task_get", map[string]any{"taskId": taskID}))
	due, _ := task["due"].(map[string]any)
	if task["notes"] != "Ask about Mia's vaccine" || task["timeZone"] != "Europe/Berlin" || due["time"] != "15:30" || due["date"] != "2026-10-19" || due["hasTime"] != true || task["updatedAtLocal"] != "2026-10-11T09:00:00+02:00" {
		t.Fatalf("task = %#v", task)
	}
	if got := api.requests[0]; got.Method != http.MethodGet || got.Path != "/v1/tasks/"+taskID {
		t.Fatalf("Tasks saw %+v", got)
	}
	// Another zone on request; an id that is not a UUID never reaches Tasks.
	other := data(t, callTool(t, session, "task_get", map[string]any{"taskId": taskID, "timeZone": "America/New_York"}))
	if other["due"].(map[string]any)["time"] != "09:30" {
		t.Fatalf("New York = %#v", other["due"])
	}
	for _, arguments := range []map[string]any{{"taskId": "../inbox-items"}, {"taskId": taskID, "timeZone": "Berlin"}} {
		if result := callTool(t, session, "task_get", arguments); !result.IsError {
			t.Errorf("task_get %v was accepted", arguments)
		}
	}
	if len(api.requests) != 2 {
		t.Fatalf("Tasks was called for an invalid get: %+v", api.requests)
	}
}

func TestTaskSearchAndListShowEveryTasksDueDate(t *testing.T) {
	api := &fakeAPI{lists: map[string]string{"/v1/tasks": `{"items":[{"id":"a","title":"Call the vet","completed":false,"dueAt":"2026-10-19T13:30:00Z"},{"id":"b","title":"Renew passport","completed":false,"dueAt":"2026-10-22T22:00:00Z"},{"id":"c","title":"Someday","completed":false}],"nextCursor":"next"}`}}
	session := taskToolSession(t, api)
	for _, tool := range []string{"task_search", "task_list"} {
		out := data(t, callTool(t, session, tool, map[string]any{"query": "query=vet&completed=false&limit=10"}))
		items, _ := out["items"].([]any)
		if len(items) != 3 || out["nextCursor"] != "next" || out["timeZone"] != "Europe/Berlin" {
			t.Fatalf("%s = %#v", tool, out)
		}
		timed := items[0].(map[string]any)["due"].(map[string]any)
		dateOnly := items[1].(map[string]any)["due"].(map[string]any)
		if timed["time"] != "15:30" || timed["hasTime"] != true || dateOnly["hasTime"] != false || dateOnly["date"] != "2026-10-23" || items[2].(map[string]any)["due"] != nil {
			t.Fatalf("%s items = %#v", tool, items)
		}
	}
	if got := api.requests[0]; got.Path != "/v1/tasks" || got.Query != "query=vet&completed=false&limit=10" {
		t.Fatalf("Tasks saw %+v", got)
	}
}

func TestTaskUpdateMovesTheDueDateOnTheOwnersClock(t *testing.T) {
	api := tasksWith(map[string]any{"title": "Call the vet", "completed": false, "dueAt": "2026-10-24T08:00:00Z"})
	session := taskToolSession(t, api)
	// "Move the vet call to tomorrow, same time", across the change to
	// winter time: 10:00 CEST on Saturday becomes 10:00 CET on Sunday.
	out := data(t, callTool(t, session, "task_update", map[string]any{"taskId": taskID, "move": map[string]any{"toDate": "2026-10-25"}}))
	patched := api.writes()
	if len(patched) != 1 || patched[0].Method != http.MethodPatch || patched[0].Path != "/v1/tasks/"+taskID || patched[0].RawBody != `{"dueAt":"2026-10-25T10:00:00+01:00"}` || patched[0].Actor != "agent:mcp" {
		t.Fatalf("Tasks saw %+v", patched)
	}
	moved, _ := out["moved"].(map[string]any)
	if moved["from"] != "Sat 24 Oct 2026, 10:00 CEST" || moved["to"] != "Sun 25 Oct 2026, 10:00 CET" || out["due"].(map[string]any)["utc"] != "2026-10-25T09:00:00Z" {
		t.Fatalf("answer = %#v", out)
	}
	// An hour later, with a new title in the same change.
	callTool(t, session, "task_update", map[string]any{"taskId": taskID, "title": "Call the vet clinic", "move": map[string]any{"hours": 1}})
	if got := api.writes()[1].Body; got["dueAt"] != "2026-10-25T11:00:00+01:00" || got["title"] != "Call the vet clinic" {
		t.Fatalf("second PATCH = %#v", got)
	}
	// Dropping the time, twice (midnight is still summer time): the second
	// time nothing is written.
	callTool(t, session, "task_update", map[string]any{"taskId": taskID, "move": map[string]any{"dateOnly": true}})
	if got := api.writes()[2].Body; got["dueAt"] != "2026-10-25T00:00:00+02:00" {
		t.Fatalf("date-only PATCH = %#v", got)
	}
	again := data(t, callTool(t, session, "task_update", map[string]any{"taskId": taskID, "move": map[string]any{"dateOnly": true}}))
	if len(api.writes()) != 3 || again["moved"].(map[string]any)["unchanged"] != true || again["due"].(map[string]any)["hasTime"] != false {
		t.Fatalf("unchanged move wrote %d times: %#v", len(api.writes()), again)
	}
}

func TestTaskUpdateRefusesMovesThatDoNotFit(t *testing.T) {
	api := tasksWith(map[string]any{"title": "Renew passport", "completed": false, "dueAt": "2026-10-22T22:00:00Z"})
	session := taskToolSession(t, api)
	result := callTool(t, session, "task_update", map[string]any{"taskId": taskID, "move": map[string]any{"hours": 1}})
	if !result.IsError || !strings.Contains(resultText(result), "cannot move: “Renew passport” is due on Fri 23 Oct 2026 without a time") {
		t.Fatalf("result = %s", resultText(result))
	}
	for name, arguments := range map[string]map[string]any{
		"move and dueAt":      {"taskId": taskID, "dueAt": "2026-10-20T10:00:00+02:00", "move": map[string]any{"days": 1}},
		"move and clearDueAt": {"taskId": taskID, "clearDueAt": true, "move": map[string]any{"days": 1}},
		"an empty move":       {"taskId": taskID, "move": map[string]any{}},
		"an unknown field":    {"taskId": taskID, "move": map[string]any{"weeks": 1}},
		"nothing to change":   {"taskId": taskID},
		"not a Task id":       {"taskId": "abc", "title": "x"},
	} {
		if result := callTool(t, session, "task_update", arguments); !result.IsError {
			t.Errorf("%s: update was accepted", name)
		}
	}
	if len(api.writes()) != 0 || len(api.requests) != 1 {
		t.Fatalf("Tasks saw %+v", api.requests)
	}
	// A plain update sends only the given fields.
	callTool(t, session, "task_update", map[string]any{"taskId": taskID, "clearDueAt": true})
	if got := api.writes()[0]; got.RawBody != `{"clearDueAt":true}` {
		t.Fatalf("clear PATCH = %s", got.RawBody)
	}
}

func calendarToolSession(t *testing.T, api *fakeAPI) *mcp.ClientSession {
	return toolSession(t, api, func(s *mcp.Server, hc *http.Client, url string) {
		addCalendarTools(s, hc, url, activity.NewEmitter("", "mcp", nil), testOwner(t))
	})
}

func TestCalendarUpdateMovesAnEventKeepingItsLength(t *testing.T) {
	api := &fakeAPI{objects: map[string]map[string]any{"/v1/events/" + eventID: {"id": eventID, "title": "Budget review", "allDay": false, "startAt": "2026-10-16T21:30:00Z", "endAt": "2026-10-16T22:15:00Z", "location": "Office"}}}
	session := calendarToolSession(t, api)
	// "Push the budget review an hour later": 23:30–00:15 becomes
	// 00:30–01:15 on the next day.
	out := data(t, callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "move": map[string]any{"hours": 1}}))
	patched := api.writes()
	if len(patched) != 1 || patched[0].Path != "/v1/events/"+eventID || patched[0].RawBody != `{"endAt":"2026-10-17T01:15:00+02:00","startAt":"2026-10-17T00:30:00+02:00"}` {
		t.Fatalf("Calendar saw %+v", patched)
	}
	if moved := out["moved"].(map[string]any); moved["from"] != "Fri 16 Oct 2026, 23:30 CEST – Sat 17 Oct 2026, 00:15 CEST" || moved["to"] != "Sat 17 Oct 2026, 00:30–01:15 CEST" {
		t.Fatalf("moved = %#v", moved)
	}
	if out["durationMinutes"] != 45.0 || out["location"] != "Office" {
		t.Fatalf("answer = %#v", out)
	}
	// Moved to a date with a new title; the time of day and length stay.
	callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "title": "Budget review Q4", "move": map[string]any{"toDate": "2026-10-26"}})
	if got := api.writes()[1].Body; got["startAt"] != "2026-10-26T00:30:00+01:00" || got["endAt"] != "2026-10-26T01:15:00+01:00" || got["title"] != "Budget review Q4" {
		t.Fatalf("second PATCH = %#v", got)
	}
}

func TestCalendarUpdateMovesOneOccurrenceOfARecurringEvent(t *testing.T) {
	series := "/v1/events/" + eventID
	occurrence := series + "/occurrences/" + recurrenceID
	weekly := map[string]any{"frequency": "weekly", "interval": 1.0}
	api := &fakeAPI{objects: map[string]map[string]any{
		series:     {"id": eventID, "title": "Gym", "allDay": false, "startAt": "2026-10-10T05:00:00Z", "endAt": "2026-10-10T06:00:00Z", "recurrence": weekly},
		occurrence: {"id": eventID, "title": "Gym", "allDay": false, "startAt": "2026-10-17T05:00:00Z", "endAt": "2026-10-17T06:00:00Z", "recurrence": weekly, "occurrence": map[string]any{"seriesId": eventID, "recurrenceId": recurrenceID, "modified": false}},
	}}
	session := calendarToolSession(t, api)
	// Only this Saturday's gym moves to Sunday at the same time; the series
	// and its other occurrences stay.
	out := data(t, callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "recurrenceId": recurrenceID, "move": map[string]any{"days": 1}}))
	patched := api.writes()
	if len(patched) != 1 || patched[0].Path != occurrence || patched[0].RawBody != `{"endAt":"2026-10-18T08:00:00+02:00","startAt":"2026-10-18T07:00:00+02:00"}` {
		t.Fatalf("Calendar saw %+v", api.requests)
	}
	if out["recurring"] != true || out["moved"].(map[string]any)["to"] != "Sun 18 Oct 2026, 07:00–08:00 CEST" {
		t.Fatalf("answer = %#v", out)
	}
	for name, arguments := range map[string]map[string]any{
		"the whole series":           {"eventId": eventID, "move": map[string]any{"hours": 1}},
		"an occurrence and a rename": {"eventId": eventID, "recurrenceId": recurrenceID, "title": "Yoga", "move": map[string]any{"hours": 1}},
	} {
		result := callTool(t, session, "calendar_update", arguments)
		if !result.IsError || !strings.Contains(resultText(result), "cannot move: “Gym” is a recurring event") {
			t.Errorf("%s: %s", name, resultText(result))
		}
	}
	if len(api.writes()) != 1 {
		t.Fatalf("the series was changed: %+v", api.writes())
	}
}

func TestCalendarUpdateRefusesMovesThatDoNotFit(t *testing.T) {
	api := &fakeAPI{objects: map[string]map[string]any{
		"/v1/events/" + eventID: {"id": eventID, "title": "Trip", "allDay": true, "startDate": "2026-10-24", "endDate": "2026-10-25"},
	}}
	session := calendarToolSession(t, api)
	for name, test := range map[string]struct {
		arguments map[string]any
		want      string
	}{
		"an all-day event an hour later": {map[string]any{"eventId": eventID, "move": map[string]any{"hours": 1}}, "cannot move: “Trip” is an all-day event"},
		"an occurrence of a single one":  {map[string]any{"eventId": eventID, "recurrenceId": "2026-10-24", "move": map[string]any{"days": 1}}, "cannot move: “Trip” does not repeat"},
		"a move and new times":           {map[string]any{"eventId": eventID, "startDate": "2026-10-30", "move": map[string]any{"days": 1}}, "move replaces"},
		"an occurrence without a move":   {map[string]any{"eventId": eventID, "recurrenceId": "2026-10-24", "title": "x"}, "recurrenceId goes with move"},
		"a bad clear beside a move":      {map[string]any{"eventId": eventID, "clear": []any{"title"}, "move": map[string]any{"days": 1}}, "cannot clear"},
		"a move that changes nothing":    {map[string]any{"eventId": eventID, "move": map[string]any{"days": 0}}, "changes nothing"},
	} {
		result := callTool(t, session, "calendar_update", test.arguments)
		if !result.IsError || !strings.Contains(resultText(result), test.want) {
			t.Errorf("%s: %s", name, resultText(result))
		}
	}
	if len(api.writes()) != 0 {
		t.Fatalf("Calendar was changed: %+v", api.writes())
	}
	// By days it moves, keeping its two days.
	callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "move": map[string]any{"days": 7}})
	if got := api.writes()[0].RawBody; got != `{"endDate":"2026-11-01","startDate":"2026-10-31"}` {
		t.Fatalf("PATCH = %s", got)
	}
}

func TestCalendarReadsShowTimesOnTheOwnersClock(t *testing.T) {
	series := "/v1/events/" + eventID
	api := &fakeAPI{
		objects: map[string]map[string]any{
			series:                                  {"id": eventID, "title": "Dentist", "allDay": false, "startAt": "2026-10-23T13:00:00Z", "endAt": "2026-10-23T14:00:00Z", "priority": "high", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 30.0}}},
			series + "/occurrences/" + recurrenceID: {"id": eventID, "title": "Gym", "allDay": false, "startAt": "2026-10-17T05:00:00Z", "endAt": "2026-10-17T06:00:00Z", "recurrence": map[string]any{"frequency": "weekly", "interval": 1.0}, "occurrence": map[string]any{"recurrenceId": recurrenceID}},
		},
		lists: map[string]string{"/v1/events/search": `{"items":[{"id":"x","title":"Gym","allDay":false,"startAt":"2026-10-17T05:00:00Z","endAt":"2026-10-17T06:00:00Z"}]}`},
	}
	session := calendarToolSession(t, api)
	event := data(t, callTool(t, session, "calendar_get", map[string]any{"eventId": eventID}))
	if event["when"] != "Fri 23 Oct 2026, 15:00–16:00 CEST" || event["priority"] != "high" || event["start"].(map[string]any)["local"] != "2026-10-23T15:00:00+02:00" || event["reminders"] == nil {
		t.Fatalf("event = %#v", event)
	}
	occurrence := data(t, callTool(t, session, "calendar_get", map[string]any{"eventId": eventID, "recurrenceId": recurrenceID}))
	if occurrence["recurring"] != true || occurrence["recurrenceText"] != "every week" || api.requests[1].Path != series+"/occurrences/"+recurrenceID {
		t.Fatalf("occurrence = %#v, Calendar saw %+v", occurrence, api.requests[1])
	}
	// The zone the search names is the one its times are shown in.
	found := data(t, callTool(t, session, "calendar_search", map[string]any{"query": "q=gym&timeZone=Europe%2FLondon"}))
	if item := found["items"].([]any)[0].(map[string]any); found["timeZone"] != "Europe/London" || item["start"].(map[string]any)["time"] != "06:00" {
		t.Fatalf("search = %#v", found)
	}
}
