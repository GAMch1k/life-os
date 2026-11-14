package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
)

// Markers stand for what people write: titles, amounts, notes, search terms.
// None may reach a service's log or its diagnostics
// (docs/architecture/multi-user.md#logs).
var markers = []string{"Zebra-Title-7Q", "4711.93", "Secret-Note-Kx", "search-term-Pz"}

type diagnosticsSink struct {
	mu   sync.Mutex
	raw  bytes.Buffer
	sent []diagnostics.Event
}

func (s *diagnosticsSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var batch diagnostics.Batch
	_ = json.Unmarshal(body, &batch)
	s.mu.Lock()
	s.raw.Write(body)
	s.sent = append(s.sent, batch.Events...)
	s.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func TestLogsAndDiagnosticsCarryNoContent(t *testing.T) {
	sink := &diagnosticsSink{}
	server := httptest.NewServer(sink)
	defer server.Close()
	recorder := diagnostics.NewRecorder(server.URL, "tasks", slog.New(slog.NewTextHandler(io.Discard, nil)))

	var logs bytes.Buffer
	app := New("tasks", WithLogger(NewJSONLogger(&logs, slog.LevelDebug)), WithDiagnostics(recorder))
	// A client's error quotes what was sent, as many handlers' messages do.
	app.Post("/v1/tasks", func(c fiber.Ctx) error {
		var body struct{ Title, Note, Amount string }
		_ = json.Unmarshal(c.Body(), &body)
		return NewError(fiber.StatusBadRequest, "invalid_task",
			fmt.Sprintf("%q is too long; note %q; amount %s", body.Title, body.Note, body.Amount))
	})
	app.Get("/v1/search/:term", func(c fiber.Ctx) error {
		return fiber.NewError(fiber.StatusNotFound, "nothing matches "+c.Params("term"))
	})
	app.Get("/v1/tasks", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"items": []string{}, "q": c.Query("q")})
	})
	// A server error: the cause is the service's own (no content), and it
	// is counted as a diagnostic.
	app.Get("/v1/tasks/:taskId", func(fiber.Ctx) error {
		return WrapError(fmt.Errorf("query tasks: connection refused"), fiber.StatusServiceUnavailable, "service_unavailable", "Tasks is unavailable.")
	})
	// A handler that panics with what it was given.
	app.Put("/v1/tasks/:taskId", func(c fiber.Ctx) error {
		panic("cannot save " + string(c.Body()))
	})

	send := func(method, path, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if status := send(http.MethodPost, "/v1/tasks", `{"title":"Zebra-Title-7Q","note":"Secret-Note-Kx","amount":"4711.93"}`); status != 400 {
		t.Fatalf("POST: %d", status)
	}
	if status := send(http.MethodGet, "/v1/search/search-term-Pz", ""); status != 404 {
		t.Fatalf("search: %d", status)
	}
	if status := send(http.MethodGet, "/v1/tasks?q=search-term-Pz&title=Zebra-Title-7Q", ""); status != 200 {
		t.Fatalf("list: %d", status)
	}
	if status := send(http.MethodGet, "/v1/tasks/Zebra-Title-7Q", ""); status != 503 {
		t.Fatalf("get: %d", status)
	}
	if status := send(http.MethodPut, "/v1/tasks/4711.93", `Secret-Note-Kx`); status != 500 {
		t.Fatalf("put: %d", status)
	}
	if err := recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	logged := logs.String()
	sink.mu.Lock()
	sent := sink.raw.String()
	events := append([]diagnostics.Event(nil), sink.sent...)
	sink.mu.Unlock()
	for _, marker := range markers {
		if strings.Contains(logged, marker) {
			t.Errorf("%q reached the log:\n%s", marker, logged)
		}
		if strings.Contains(sent, marker) {
			t.Errorf("%q reached the diagnostics: %s", marker, sent)
		}
	}
	// What the log does say: the route, the status, and the error code.
	for _, want := range []string{`"route":"/v1/tasks"`, `"code":"invalid_task"`, `"route":"/v1/search/:term"`,
		`"route":"/v1/tasks/:taskId"`, `"code":"service_unavailable"`, `"error":"service_unavailable: query tasks: connection refused"`, `"code":"panic"`} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log lacks %s:\n%s", want, logged)
		}
	}
	// Only the two server errors are diagnostics, by code and route.
	got := map[string]int{}
	for _, event := range events {
		if err := event.Validate(); err != nil {
			t.Errorf("an invalid diagnostic %+v: %v", event, err)
		}
		if event.Service != "tasks" || event.RequestID == nil {
			t.Errorf("diagnostic %+v: want service tasks and a request id", event)
		}
		got[string(event.Code)+" "+event.Route] += event.Count
	}
	want := map[string]int{"service_unavailable /v1/tasks/:taskId": 1, "panic /v1/tasks/:taskId": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("diagnostics %v, want %v", got, want)
	}
}

// A panic with one of the runtime's own errors is a fact about the code and
// is logged as it is.
func TestRuntimePanicsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	app := New("tasks", WithLogger(NewJSONLogger(&logs, slog.LevelDebug)), WithDiagnostics(nil))
	app.Get("/v1/nil", func(fiber.Ctx) error {
		var items []int
		return fiber.NewError(fiber.StatusOK, fmt.Sprint(items[3]))
	})
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/nil", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 500 || !strings.Contains(logs.String(), "index out of range [3] with length 0") {
		t.Fatalf("%d\n%s", res.StatusCode, logs.String())
	}
}
