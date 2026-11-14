package diagnostics

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The Event type has no free-text field: every string is a name from Life
// OS's own code or a code from the list. A new field must be one of these
// kinds, or this test fails until someone decides it cannot carry content.
func TestEventHasNoFreeTextField(t *testing.T) {
	allowed := map[string]bool{
		"Service":    true, // checked against a service-name pattern
		"Code":       true, // from the fixed list
		"Route":      true, // a route pattern from the code, checked
		"Status":     true,
		"SubjectID":  true,
		"RequestID":  true,
		"Count":      true,
		"DurationMs": true,
		"FirstAt":    true,
		"LastAt":     true,
	}
	kind := reflect.TypeFor[Event]()
	for i := range kind.NumField() {
		field := kind.Field(i)
		if !allowed[field.Name] {
			t.Errorf("Event.%s (%s) is new: diagnostics carry no content, so it must be an id, a number, a time, or a checked name", field.Name, field.Type)
		}
	}
	for _, name := range []string{"SubjectID", "RequestID"} {
		field, _ := kind.FieldByName(name)
		if field.Type != reflect.TypeFor[*uuid.UUID]() {
			t.Errorf("Event.%s is %s, want *uuid.UUID", name, field.Type)
		}
	}
}

func TestValidate(t *testing.T) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	good := Event{Service: "tasks", Code: InternalError, Route: "/v1/tasks/:taskId", Status: 500, Count: 2, FirstAt: at, LastAt: at.Add(time.Second)}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Event){
		"a title as the service":  func(e *Event) { e.Service = "Buy milk" },
		"a code not on the list":  func(e *Event) { e.Code = "Buy milk" },
		"a sentence as the route": func(e *Event) { e.Route = "/v1/tasks/Buy milk tomorrow" },
		"a route that is no path": func(e *Event) { e.Route = "12.34 EUR" },
		"no count":                func(e *Event) { e.Count = 0 },
		"a negative duration":     func(e *Event) { e.DurationMs = -1 },
		"no time":                 func(e *Event) { e.FirstAt = time.Time{} },
		"last before first":       func(e *Event) { e.LastAt = at.Add(-time.Hour) },
		"an odd status":           func(e *Event) { e.Status = 42 },
	} {
		event := good
		change(&event)
		if event.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestForStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   Code
	}{
		{500, "internal_error", InternalError},
		{503, "google_unavailable", GoogleUnavailable},
		{503, "some_new_code", ServiceUnavailable},
		{502, "", BadGateway},
		{504, "", GatewayTimeout},
		{507, "", ServerError},
	} {
		if got := ForStatus(tc.status, tc.code); got != tc.want {
			t.Errorf("ForStatus(%d, %q) = %s, want %s", tc.status, tc.code, got, tc.want)
		}
	}
}

type sink struct {
	mu      sync.Mutex
	batches []Batch
	fail    bool
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path != Path || r.Header.Get("X-Life-OS-Actor") != "system:tasks" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if s.fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var batch Batch
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &batch)
	s.batches = append(s.batches, batch)
	w.WriteHeader(http.StatusAccepted)
}

// A burst of errors is one row per code and route, sent once a minute; what
// cannot be sent waits for the next flush.
func TestRecorderAggregatesAndRetries(t *testing.T) {
	target := &sink{fail: true}
	server := httptest.NewServer(target)
	defer server.Close()
	recorder := NewRecorder(server.URL, "tasks", slog.New(slog.NewTextHandler(io.Discard, nil)))
	requestID := uuid.NewString()
	for range 40 {
		recorder.Answer(503, "service_unavailable", "/v1/tasks", requestID, 30*time.Millisecond)
	}
	recorder.Answer(500, "", "/v1/tasks/:taskId", "not-a-uuid", time.Second)
	recorder.Answer(404, "not_found", "/v1/tasks/:taskId", requestID, 0)
	recorder.Answer(500, "", "/v1/search/Buy milk", requestID, 0)

	if err := recorder.Flush(t.Context()); err == nil {
		t.Fatal("a failed flush reported no error")
	}
	target.mu.Lock()
	target.fail = false
	target.mu.Unlock()
	recorder.Answer(503, "service_unavailable", "/v1/tasks", requestID, 10*time.Millisecond)
	if err := recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(target.batches) != 1 {
		t.Fatalf("%d batches, want 1 (an empty flush sends nothing)", len(target.batches))
	}
	counts := map[string]int{}
	for _, event := range target.batches[0].Events {
		if err := event.Validate(); err != nil {
			t.Errorf("%+v: %v", event, err)
		}
		counts[string(event.Code)+" "+event.Route] = event.Count
		if event.Code == ServiceUnavailable && (event.DurationMs != 30 || event.RequestID == nil || event.RequestID.String() != requestID) {
			t.Errorf("aggregate %+v", event)
		}
		if event.Route == "/v1/tasks/:taskId" && event.RequestID != nil {
			t.Errorf("a request id that is no UUID was kept: %+v", event)
		}
	}
	want := map[string]int{"service_unavailable /v1/tasks": 41, "internal_error /v1/tasks/:taskId": 1, "internal_error ": 1}
	if len(counts) != len(want) {
		t.Fatalf("counts %v, want %v", counts, want)
	}
	for key, count := range want {
		if counts[key] != count {
			t.Errorf("%s: %d, want %d (all %v)", key, counts[key], count, counts)
		}
	}
}

func TestNilAndEnv(t *testing.T) {
	var recorder *Recorder
	recorder.Answer(500, "", "/x", "", 0)
	if err := recorder.Flush(t.Context()); err != nil || recorder.Pending() != nil {
		t.Fatal("a nil recorder records")
	}
	t.Setenv("ACTIVITY_SERVICE_URL", "")
	if FromEnv("tasks", nil) != nil {
		t.Error("a recorder without ACTIVITY_SERVICE_URL")
	}
	t.Setenv("ACTIVITY_SERVICE_URL", "http://activity:8080")
	if FromEnv("tasks", nil) == nil {
		t.Error("no recorder with ACTIVITY_SERVICE_URL")
	}
	t.Setenv("ACTIVITY_DIAGNOSTICS", "off")
	if FromEnv("tasks", nil) != nil {
		t.Error("ACTIVITY_DIAGNOSTICS=off kept a recorder")
	}
}

// The codes the Activity service accepts are this list; its contract names
// them (services/activity/api/openapi.yaml), and the service's own test
// compares the two.
func TestCodesAreNames(t *testing.T) {
	seen := map[Code]bool{}
	for _, code := range Codes {
		if seen[code] || strings.Trim(string(code), "abcdefghijklmnopqrstuvwxyz_") != "" {
			t.Errorf("code %q is repeated or not a lowercase name", code)
		}
		seen[code] = true
	}
}
