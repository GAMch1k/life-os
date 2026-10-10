package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

const eventID = "6f1c2b9e-3d4a-4f5b-8c7d-9e0f1a2b3c4d"

// calendarRequest is what the fake Calendar API received.
type calendarRequest struct {
	Method, Path, Query string
	Actor, Idempotency  string
	Body                map[string]any
	RawBody             string
}

// calendarSession connects an MCP client to a server with only the Calendar
// tools, backed by a fake Calendar API that records every request.
func calendarSession(t *testing.T, status int, response string) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	requests := &[]calendarRequest{}
	calendar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := calendarRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Actor: r.Header.Get(activity.ActorHeader), Idempotency: r.Header.Get("Idempotency-Key"), RawBody: string(raw)}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &request.Body)
		}
		*requests = append(*requests, request)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(calendar.Close)
	server := mcp.NewServer(&mcp.Implementation{Name: "life-os-test", Version: "0"}, nil)
	addCalendarTools(server, calendar.Client(), calendar.URL, activity.NewEmitter("", "mcp", nil))
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
	return session, requests
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, " ")
}

func TestCalendarSearchUsesTheSearchAPI(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{"items":[{"id":"`+eventID+`","title":"Zahnarzt"}]}`)
	result := callTool(t, session, "calendar_search", map[string]any{"query": "q=Zahnarzt&from=2026-10-09T22%3A00%3A00Z&timeZone=Europe%2FBerlin&limit=10"})
	if result.IsError {
		t.Fatalf("search failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodGet || got.Path != "/v1/events/search" || got.Query != "q=Zahnarzt&from=2026-10-09T22%3A00%3A00Z&timeZone=Europe%2FBerlin&limit=10" {
		t.Fatalf("Calendar saw %+v", got)
	}
}

func TestCalendarCreateForwardsLocationRemindersAndTimeZone(t *testing.T) {
	session, requests := calendarSession(t, http.StatusCreated, `{"id":"`+eventID+`"}`)
	result := callTool(t, session, "calendar_create", map[string]any{
		"idempotencyKey": "action-1", "title": "Dentist", "location": "Dr. Weber", "allDay": false,
		"startAt": "2026-10-16T15:00:00+02:00", "endAt": "2026-10-16T16:00:00+02:00", "timeZone": "Europe/Berlin",
		"reminders": []any{map[string]any{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	})
	if result.IsError {
		t.Fatalf("create failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPost || got.Path != "/v1/events" || got.Idempotency != "action-1" || got.Actor != "agent:mcp" {
		t.Fatalf("Calendar saw %+v", got)
	}
	if got.Body["location"] != "Dr. Weber" || got.Body["timeZone"] != "Europe/Berlin" {
		t.Fatalf("body = %s", got.RawBody)
	}
	want := []any{map[string]any{"kind": "localTime", "daysBefore": 1.0, "localTime": "19:00"}}
	if !reflect.DeepEqual(got.Body["reminders"], want) {
		t.Fatalf("reminders = %#v", got.Body["reminders"])
	}

	// Leaving reminders and useDefaultReminders out keeps the defaults alone;
	// useDefaultReminders false with [] means none at all.
	callTool(t, session, "calendar_create", map[string]any{"title": "A", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16"})
	callTool(t, session, "calendar_create", map[string]any{"title": "B", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16", "reminders": []any{}, "useDefaultReminders": false})
	for _, field := range []string{"reminders", "useDefaultReminders"} {
		if _, present := (*requests)[1].Body[field]; present {
			t.Fatalf("omitted %s was sent: %s", field, (*requests)[1].RawBody)
		}
	}
	if reminders, ok := (*requests)[2].Body["reminders"].([]any); !ok || len(reminders) != 0 || (*requests)[2].Body["useDefaultReminders"] != false {
		t.Fatalf("no reminders at all became %s", (*requests)[2].RawBody)
	}
	if _, present := (*requests)[1].Body["priority"]; present {
		t.Fatalf("omitted priority was sent: %s", (*requests)[1].RawBody)
	}

	// An important event is high priority.
	callTool(t, session, "calendar_create", map[string]any{"title": "Visa", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16", "priority": "high"})
	if (*requests)[3].Body["priority"] != "high" {
		t.Fatalf("priority became %s", (*requests)[3].RawBody)
	}
}

func TestCalendarUpdatePatchesOnlyTheGivenFieldsAndClears(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{"id":"`+eventID+`"}`)
	result := callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "startAt": "2026-10-19T09:00:00+02:00", "endAt": "2026-10-19T10:00:00+02:00", "clear": []any{"location", "reminders"}})
	if result.IsError {
		t.Fatalf("update failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPatch || got.Path != "/v1/events/"+eventID || got.Actor != "agent:mcp" {
		t.Fatalf("Calendar saw %+v", got)
	}
	want := map[string]any{"startAt": "2026-10-19T09:00:00+02:00", "endAt": "2026-10-19T10:00:00+02:00", "location": nil, "reminders": nil}
	if !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("PATCH body = %s", got.RawBody)
	}

	// The defaults are turned off on their own; the event's reminders stay.
	callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "useDefaultReminders": false})
	if got := (*requests)[1]; !reflect.DeepEqual(got.Body, map[string]any{"useDefaultReminders": false}) {
		t.Fatalf("defaults off PATCH body = %s", got.RawBody)
	}
	callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "priority": "low"})
	if got := (*requests)[2]; !reflect.DeepEqual(got.Body, map[string]any{"priority": "low"}) {
		t.Fatalf("priority PATCH body = %s", got.RawBody)
	}
}

func TestCalendarUpdateRejectsUnsafeOrEmptyChanges(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{}`)
	for name, arguments := range map[string]map[string]any{
		"nothing to change":     {"eventId": eventID},
		"unknown clear":         {"eventId": eventID, "clear": []any{"title"}},
		"set and cleared":       {"eventId": eventID, "location": "Home", "clear": []any{"location"}},
		"not an event id":       {"eventId": "../tasks", "title": "x"},
		"status is not a field": {"eventId": eventID, "status": "cancelled"},
	} {
		if result := callTool(t, session, "calendar_update", arguments); !result.IsError {
			t.Errorf("%s: update was accepted", name)
		}
	}
	if len(*requests) != 0 {
		t.Fatalf("Calendar was called: %+v", *requests)
	}
}

func TestCalendarCancelKeepsTheEventAsHistory(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{"id":"`+eventID+`","status":"cancelled"}`)
	if result := callTool(t, session, "calendar_cancel", map[string]any{"eventId": eventID}); result.IsError {
		t.Fatalf("cancel failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPatch || got.Path != "/v1/events/"+eventID || !reflect.DeepEqual(got.Body, map[string]any{"status": "cancelled"}) || got.Actor != "agent:mcp" {
		t.Fatalf("Calendar saw %+v", got)
	}
}

func TestCalendarCancelSkipsOneOccurrenceOfARecurringEvent(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{"id":"`+eventID+`","status":"cancelled"}`)
	if result := callTool(t, session, "calendar_cancel", map[string]any{"eventId": eventID, "recurrenceId": "2026-10-10T05:00:00Z"}); result.IsError {
		t.Fatalf("cancel failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPatch || got.Path != "/v1/events/"+eventID+"/occurrences/2026-10-10T05:00:00Z" || !reflect.DeepEqual(got.Body, map[string]any{"status": "cancelled"}) {
		t.Fatalf("Calendar saw %+v", got)
	}
	callTool(t, session, "calendar_cancel", map[string]any{"eventId": eventID, "recurrenceId": "2026-10-24"})
	if got := (*requests)[1]; got.Path != "/v1/events/"+eventID+"/occurrences/2026-10-24" {
		t.Fatalf("all-day occurrence: Calendar saw %+v", got)
	}
	for _, rid := range []string{"../../sources", "today", "2026-10-10T05:00:00Z/../x"} {
		if result := callTool(t, session, "calendar_cancel", map[string]any{"eventId": eventID, "recurrenceId": rid}); !result.IsError {
			t.Errorf("recurrenceId %q was accepted", rid)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("Calendar was called for an invalid recurrenceId: %+v", *requests)
	}
}

// Recurring events are created and changed only in the Calendar: the
// tools take no recurrence.
func TestCalendarToolsTakeNoRecurrence(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{}`)
	weekly := map[string]any{"frequency": "weekly", "interval": 1}
	if result := callTool(t, session, "calendar_create", map[string]any{"title": "Gym", "allDay": true, "startDate": "2026-10-10", "endDate": "2026-10-10", "recurrence": weekly}); !result.IsError {
		t.Error("calendar_create accepted a recurrence")
	}
	if result := callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "recurrence": weekly}); !result.IsError {
		t.Error("calendar_update accepted a recurrence")
	}
	if len(*requests) != 0 {
		t.Fatalf("Calendar was called: %+v", *requests)
	}
}

func TestCalendarDeleteRemovesTheEvent(t *testing.T) {
	session, requests := calendarSession(t, http.StatusNoContent, ``)
	result := callTool(t, session, "calendar_delete", map[string]any{"eventId": strings.ToUpper(eventID)})
	if result.IsError {
		t.Fatalf("delete failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodDelete || got.Path != "/v1/events/"+eventID || got.Actor != "agent:mcp" {
		t.Fatalf("Calendar saw %+v", got)
	}
	data, _ := result.StructuredContent.(map[string]any)["data"].(map[string]any)
	if data["id"] != eventID || data["deleted"] != true {
		t.Fatalf("result = %#v", result.StructuredContent)
	}
}

func TestCalendarGetReadsOneEvent(t *testing.T) {
	session, requests := calendarSession(t, http.StatusOK, `{"id":"`+eventID+`","status":"cancelled"}`)
	if result := callTool(t, session, "calendar_get", map[string]any{"eventId": eventID}); result.IsError {
		t.Fatalf("get failed: %s", resultText(result))
	}
	if got := (*requests)[0]; got.Method != http.MethodGet || got.Path != "/v1/events/"+eventID {
		t.Fatalf("Calendar saw %+v", got)
	}
}

func TestCalendarErrorsReachTheCaller(t *testing.T) {
	session, _ := calendarSession(t, http.StatusBadRequest, `{"error":"endAt must be after startAt"}`)
	result := callTool(t, session, "calendar_update", map[string]any{"eventId": eventID, "endAt": "2020-01-01T00:00:00Z"})
	if !result.IsError || !strings.Contains(resultText(result), "endAt must be after startAt") {
		t.Fatalf("result = %+v", result)
	}
}
