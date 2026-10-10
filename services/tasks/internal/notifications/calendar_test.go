package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCalendarListsADaysEventsAcrossPages(t *testing.T) {
	var mu sync.Mutex
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/events" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		mu.Lock()
		queries = append(queries, r.URL.Query())
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"items":[
				{"title":"Birthday","allDay":true,"startDate":"2026-10-10","endDate":"2026-10-10","status":"active"},
				{"title":"Dentist","allDay":false,"startAt":"2026-10-10T07:00:00Z","endAt":"2026-10-10T08:00:00Z","status":"active"},
				{"title":"Skipped","allDay":false,"startAt":"2026-10-10T09:00:00Z","endAt":"2026-10-10T10:00:00Z","status":"cancelled"}
			],"nextCursor":"page2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"title":"Dinner","allDay":false,"startAt":"2026-10-10T18:00:00Z","endAt":null,"status":"active"}],"nextCursor":null}`))
	}))
	defer server.Close()

	events, err := NewCalendar(server.URL+"/", berlin).EventsOn(context.Background(), date("2026-10-10"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Title: "Birthday", AllDay: true},
		{Title: "Dentist", Start: instant("2026-10-10T07:00:00Z"), End: instant("2026-10-10T08:00:00Z")},
		{Title: "Dinner", Start: instant("2026-10-10T18:00:00Z")},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i := range want {
		if events[i].Title != want[i].Title || events[i].AllDay != want[i].AllDay || !events[i].Start.Equal(want[i].Start) || !events[i].End.Equal(want[i].End) {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}
	if len(queries) != 2 {
		t.Fatalf("%d requests", len(queries))
	}
	// The owner's day, Berlin midnight to midnight, in the owner's zone.
	first := queries[0]
	if first.Get("from") != "2026-10-09T22:00:00Z" || first.Get("to") != "2026-10-10T22:00:00Z" || first.Get("timeZone") != "Europe/Berlin" || first.Get("limit") != "100" || first.Has("includeCancelled") {
		t.Errorf("query = %v", first)
	}
	if queries[1].Get("cursor") != "page2" || queries[1].Get("from") != first.Get("from") {
		t.Errorf("second page = %v", queries[1])
	}
}

func TestCalendarErrorsAndTheDayAcrossDaylightSaving(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	var from, to string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		from, to = r.URL.Query().Get("from"), r.URL.Query().Get("to")
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := NewCalendar(server.URL, berlin).EventsOn(context.Background(), date("2026-10-25"))
	if err == nil || !strings.Contains(err.Error(), "503") || requests != 2 {
		t.Fatalf("err = %v after %d requests", err, requests)
	}
	// 25 October has 25 hours in Berlin.
	if from != "2026-10-24T22:00:00Z" || to != "2026-10-25T23:00:00Z" {
		t.Errorf("range = %s – %s", from, to)
	}

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer refusing.Close()
	requests = 0
	if _, err := NewCalendar(refusing.URL, berlin).EventsOn(context.Background(), date("2026-10-10")); err == nil || requests != 1 {
		t.Errorf("a 400 is not retried: err = %v after %d requests", err, requests)
	}

	// Unreachable within the timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	client := NewCalendar(slow.URL, berlin)
	client.Timeout = 200 * time.Millisecond
	if _, err := client.EventsOn(context.Background(), date("2026-10-10")); err == nil {
		t.Error("a hanging Calendar answered")
	}

	// Not configured.
	off := NewCalendar("  ", berlin)
	if events, err := off.EventsOn(context.Background(), date("2026-10-10")); off.Enabled() || events != nil || err != nil {
		t.Errorf("off: %v %v", events, err)
	}
	var none *Calendar
	if none.Enabled() {
		t.Error("a nil Calendar is enabled")
	}
}
