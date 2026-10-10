package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
)

type recorder struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []map[string]any
	statuses []int // answered in order; then 201
}

func (r *recorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.requests = append(r.requests, req)
		r.bodies = append(r.bodies, body)
		status := http.StatusCreated
		if len(r.statuses) > 0 {
			status, r.statuses = r.statuses[0], r.statuses[1:]
		}
		r.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"category must be dotted"},"request_id":"r1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func reminder() Notification {
	return Notification{
		Title:      "Dentist in 30 minutes",
		Body:       "Dr. Weber",
		Importance: Medium,
		Category:   "calendar.reminder",
		Link:       "/calendar?date=2026-10-10",
		DedupeKey:  "calendar.reminder:e1:30",
		OccurredAt: time.Date(2026, 10, 10, 10, 30, 0, 0, time.FixedZone("CEST", 2*3600)),
	}
}

func TestSendPostsTheNotificationAsTheService(t *testing.T) {
	rec := &recorder{}
	server := rec.serve(t)
	causation := uuid.New()
	ctx := activity.WithTrace(context.Background(), activity.Trace{CorrelationID: "workflow-1", CausationID: &causation, Actor: activity.User()})

	if err := New(server.URL+"/", "calendar").Send(ctx, reminder()); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 {
		t.Fatalf("requests = %d", rec.count())
	}
	req, body := rec.requests[0], rec.bodies[0]
	if req.Method != http.MethodPost || req.URL.Path != "/v1/notifications" || req.Header.Get("content-type") != "application/json" {
		t.Fatalf("request = %s %s %v", req.Method, req.URL.Path, req.Header)
	}
	if got := req.Header.Get(activity.ActorHeader); got != "system:calendar" {
		t.Errorf("actor = %q, want system:calendar even inside a user's trace", got)
	}
	if req.Header.Get(activity.CorrelationIDHeader) != "workflow-1" || req.Header.Get(activity.CausationIDHeader) != causation.String() {
		t.Errorf("trace headers = %v", req.Header)
	}
	want := map[string]any{
		"title": "Dentist in 30 minutes", "body": "Dr. Weber", "importance": "medium", "category": "calendar.reminder",
		"link": "/calendar?date=2026-10-10", "dedupeKey": "calendar.reminder:e1:30", "occurredAt": "2026-10-10T08:30:00Z",
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("body[%s] = %v, want %v", key, body[key], value)
		}
	}
	if _, ok := body["source"]; ok {
		t.Error("source is left to the service, which derives it from the actor")
	}
}

func TestSendOmitsOptionalFields(t *testing.T) {
	rec := &recorder{}
	server := rec.serve(t)
	if err := New(server.URL, "monitor").Send(context.Background(), Notification{Title: "Tasks is down", Importance: High, Category: "status.service_down", DedupeKey: "status.service_down:tasks:1"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"body", "link", "occurredAt"} {
		if _, ok := rec.bodies[0][key]; ok {
			t.Errorf("body has %s", key)
		}
	}
}

func TestSendIsANoOpWithoutAURL(t *testing.T) {
	var nilClient *Client
	if nilClient.Enabled() || nilClient.Send(context.Background(), reminder()) != nil {
		t.Fatal("a nil client must be a disabled no-op")
	}
	client := New("  ", "calendar")
	if client.Enabled() || client.Send(context.Background(), Notification{}) != nil {
		t.Fatal("a client without a URL must be a disabled no-op, even for an invalid notification")
	}
	t.Setenv(EnvURL, "")
	if FromEnv("calendar").Enabled() {
		t.Fatal("an empty NOTIFICATIONS_SERVICE_URL disables the client")
	}
	t.Setenv(EnvURL, "http://notifications:8080/")
	if client := FromEnv("calendar"); !client.Enabled() || client.URL != "http://notifications:8080" {
		t.Fatalf("FromEnv = %+v", client)
	}
}

func TestSendRefusesInvalidNotifications(t *testing.T) {
	rec := &recorder{}
	server := rec.serve(t)
	for name, change := range map[string]func(*Notification){
		"no dedupe key": func(n *Notification) { n.DedupeKey = " " },
		"no title":      func(n *Notification) { n.Title = "" },
		"importance":    func(n *Notification) { n.Importance = "urgent" },
		"no category":   func(n *Notification) { n.Category = "" },
	} {
		n := reminder()
		change(&n)
		if err := New(server.URL, "calendar").Send(context.Background(), n); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	if err := New(server.URL, "bad name").Send(context.Background(), reminder()); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid service name: error = %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("invalid notifications reached the service %d times", rec.count())
	}
}

func TestSendRetriesTemporaryFailures(t *testing.T) {
	rec := &recorder{statuses: []int{http.StatusServiceUnavailable, http.StatusBadGateway}}
	server := rec.serve(t)
	if err := New(server.URL, "calendar").Send(context.Background(), reminder()); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 3 {
		t.Fatalf("requests = %d, want 3", rec.count())
	}
	if rec.bodies[0]["dedupeKey"] != rec.bodies[2]["dedupeKey"] {
		t.Fatal("a retry must reuse the dedupe key")
	}
}

func TestSendGivesUpAfterThreeTemporaryFailures(t *testing.T) {
	rec := &recorder{statuses: []int{500, 500, 500, 500}}
	server := rec.serve(t)
	err := New(server.URL, "calendar").Send(context.Background(), reminder())
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 500 || !status.Temporary() || rec.count() != 3 {
		t.Fatalf("error = %v after %d requests", err, rec.count())
	}
}

func TestSendDoesNotRetryRejections(t *testing.T) {
	rec := &recorder{statuses: []int{http.StatusBadRequest}}
	server := rec.serve(t)
	err := New(server.URL, "calendar").Send(context.Background(), reminder())
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 400 || status.Code != "bad_request" || status.Message != "category must be dotted" || status.Temporary() {
		t.Fatalf("error = %#v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1", rec.count())
	}
}

func TestSendTreatsADuplicateAsSuccess(t *testing.T) {
	rec := &recorder{statuses: []int{http.StatusOK}}
	server := rec.serve(t)
	if err := New(server.URL, "calendar").Send(context.Background(), reminder()); err != nil {
		t.Fatal(err)
	}
}

func TestSendHonoursTheTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	client := New(server.URL, "calendar")
	client.Timeout = 100 * time.Millisecond
	started := time.Now()
	err := client.Send(context.Background(), reminder())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a deadline error", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Send took %v with a 100ms timeout", elapsed)
	}
}
