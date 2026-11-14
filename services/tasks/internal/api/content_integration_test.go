package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/diagnostics"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/tasks/internal/testdb"
)

// Service logs carry ids and error codes, never titles, amounts, or what
// someone wrote; neither do the server's diagnostics
// (docs/architecture/multi-user.md#logs). A scenario with made-up content
// planted in every field a person fills, through success and failure, as
// Banking's TestSecretsNeverLeave does for secrets.
func TestLogsAndDiagnosticsNeverCarryContent(t *testing.T) {
	pool := testdb.Open(t, "")
	var diagnosticsSent bytes.Buffer
	var mu sync.Mutex
	diagnosticsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		diagnosticsSent.Write(body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer diagnosticsServer.Close()
	recorder := &activityRecorder{}
	activityServer := httptest.NewServer(recorder)
	defer activityServer.Close()

	var logs bytes.Buffer
	logger := httpserver.NewJSONLogger(&logs, slog.LevelDebug)
	diagnosed := diagnostics.NewRecorder(diagnosticsServer.URL, "tasks", logger)
	app := httpserver.New("tasks", httpserver.WithLogger(logger), httpserver.WithDiagnostics(diagnosed))
	Register(app, NewHandler(store.New(pool), activity.NewEmitter(activityServer.URL, "tasks", logger)))
	c := apiClient{t: t, app: app, activity: recorder}

	const (
		title  = "Zebra-Title-7Q pick up the prescription"
		notes  = "Secret-Note-Kx: 4711.93 EUR to Dr. Example"
		search = "search-term-Pz"
	)
	markers := []string{"Zebra-Title-7Q", "Secret-Note-Kx", "4711.93", "Dr. Example", search}

	task := c.create(map[string]any{"title": title, "notes": notes})
	id := task.Id.String()
	c.do(http.MethodPatch, "/v1/tasks/"+id, map[string]any{"title": title + " again", "notes": notes}, activity.ActorHeader, "dev:test")
	c.do(http.MethodPost, "/v1/tasks/"+id+"/complete", nil, activity.ActorHeader, "dev:test")
	c.list(url.Values{"query": {search}})
	c.list(url.Values{"query": {title}})
	// Requests that fail on what was sent (a bad date, no title), a long
	// title, an id that is no id, and a reused idempotency key.
	for _, body := range []map[string]any{
		{"title": title, "dueAt": "Zebra-Title-7Q"},
		{"title": "", "notes": notes},
		{"title": strings.Repeat("Zebra-Title-7Q ", 100)},
		{"notes": notes},
	} {
		c.do(http.MethodPost, "/v1/tasks", body, activity.ActorHeader, "dev:test")
	}
	c.do(http.MethodGet, "/v1/tasks/Zebra-Title-7Q", nil)
	c.do(http.MethodPatch, "/v1/tasks/4711.93", map[string]any{"title": title}, activity.ActorHeader, "dev:test")
	c.do(http.MethodPost, "/v1/tasks", map[string]any{"title": title}, activity.ActorHeader, "dev:test", "Idempotency-Key", "search-term-Pz-key")
	c.do(http.MethodPost, "/v1/tasks", map[string]any{"title": "other " + title}, activity.ActorHeader, "dev:test", "Idempotency-Key", "search-term-Pz-key")
	// A server error: the database goes away.
	pool.Close()
	c.do(http.MethodPost, "/v1/tasks", map[string]any{"title": title, "notes": notes}, activity.ActorHeader, "dev:test")
	c.do(http.MethodGet, "/v1/tasks?"+url.Values{"query": {search}}.Encode(), nil)
	if err := diagnosed.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	sent := diagnosticsSent.String()
	mu.Unlock()
	logged := logs.String()
	for _, marker := range markers {
		if strings.Contains(logged, marker) {
			t.Errorf("%q reached the service log:\n%s", marker, logged)
		}
		if strings.Contains(sent, marker) {
			t.Errorf("%q reached the diagnostics: %s", marker, sent)
		}
	}
	// The diagnostics did count the server errors, by code and route.
	var batch diagnostics.Batch
	if err := json.Unmarshal([]byte(sent), &batch); err != nil || len(batch.Events) == 0 {
		t.Fatalf("no diagnostics for the server errors: %s (%v)", sent, err)
	}
	for _, event := range batch.Events {
		if err := event.Validate(); err != nil || event.Service != "tasks" || !strings.HasPrefix(event.Route, "/v1/tasks") {
			t.Errorf("diagnostic %+v: %v", event, err)
		}
	}
	// The activity log is user data and keeps the title: that is its job.
	if len(recorder.since(0, "task.created")) == 0 {
		t.Error("no task.created event")
	}
}
