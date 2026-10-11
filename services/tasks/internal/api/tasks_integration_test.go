package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/tasks/internal/testdb"
)

// These tests run the Tasks API against a fresh, fully migrated schema on
// TASKS_TEST_DATABASE_URL, with an in-process Activity recorder.

type activityRecorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (a *activityRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var event activity.Event
	_ = json.NewDecoder(r.Body).Decode(&event)
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

// since returns the events recorded after the first seen, of the given type.
func (a *activityRecorder) since(seen int, eventType string) []activity.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var found []activity.Event
	for _, event := range a.events[seen:] {
		if event.Type == eventType {
			found = append(found, event)
		}
	}
	return found
}

func (a *activityRecorder) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

type apiClient struct {
	t        *testing.T
	app      *httpserver.App
	activity *activityRecorder
}

func newAPIClient(t *testing.T) apiClient {
	t.Helper()
	pool := testdb.Open(t, "")
	recorder := &activityRecorder{}
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	app := httpserver.New("tasks", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, NewHandler(store.New(pool), activity.NewEmitter(server.URL, "tasks", slog.New(slog.NewTextHandler(io.Discard, nil)))))
	return apiClient{t: t, app: app, activity: recorder}
}

// do sends a request with the given headers (name, value pairs) and returns
// the status and body.
func (c apiClient) do(method, target string, body any, headers ...string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	for index := 0; index+1 < len(headers); index += 2 {
		req.Header.Set(headers[index], headers[index+1])
	}
	resp, err := c.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		c.t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// create adds a Task as a development tool and returns it.
func (c apiClient) create(body map[string]any) generated.Task {
	c.t.Helper()
	status, raw := c.do(http.MethodPost, "/v1/tasks", body, activity.ActorHeader, "dev:test")
	if status != http.StatusCreated {
		c.t.Fatalf("create %v: %d %s", body, status, raw)
	}
	var task generated.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		c.t.Fatal(err)
	}
	return task
}

// list returns the titles GET /v1/tasks lists for the query.
func (c apiClient) list(query url.Values) string {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, "/v1/tasks?"+query.Encode(), nil)
	if status != http.StatusOK {
		c.t.Fatalf("list %v: %d %s", query, status, raw)
	}
	var list generated.TaskList
	if err := json.Unmarshal(raw, &list); err != nil {
		c.t.Fatal(err)
	}
	titles := make([]string, 0, len(list.Items))
	for _, task := range list.Items {
		titles = append(titles, task.Title)
	}
	return strings.Join(titles, ", ")
}

// expectError checks a status and the standard error envelope's code.
func expectError(t *testing.T, what string, status int, raw []byte, wantStatus int, wantCode string) {
	t.Helper()
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if status != wantStatus || envelope.Error.Code != wantCode {
		t.Fatalf("%s: %d %s, want %d %s", what, status, raw, wantStatus, wantCode)
	}
}

func TestDeletingATaskRemovesItEverywhere(t *testing.T) {
	c := newAPIClient(t)
	rent := c.create(map[string]any{"title": "Pay rent", "dueAt": "2026-10-12T09:00:00Z"})
	plants := c.create(map[string]any{"title": "Water plants", "dueAt": "2026-10-11T00:00:00Z", "recurrence": map[string]any{"frequency": "weekly", "interval": 1}})
	old := c.create(map[string]any{"title": "Old test task", "notes": "rent check"})
	if status, raw := c.do(http.MethodPost, "/v1/tasks/"+old.Id.String()+"/complete", nil, activity.ActorHeader, "dev:test"); status != http.StatusOK {
		t.Fatalf("complete: %d %s", status, raw)
	}
	c.create(map[string]any{"title": "Call mom", "dueAt": "2026-10-12T09:00:00Z"})
	c.create(map[string]any{"title": "Renew passport"})
	if got := c.list(nil); got != "Water plants, Call mom, Pay rent, Renew passport, Old test task" {
		t.Fatalf("before = %q", got)
	}

	// Without an actor nothing is deleted.
	status, raw := c.do(http.MethodDelete, "/v1/tasks/"+rent.Id.String(), nil)
	expectError(t, "delete without an actor", status, raw, http.StatusBadRequest, activity.ActorRequiredCode)
	if status, raw := c.do(http.MethodGet, "/v1/tasks/"+rent.Id.String(), nil); status != http.StatusOK {
		t.Fatalf("after a delete without an actor: %d %s", status, raw)
	}

	// The owner deletes a dated Task and a recurring one (its whole series),
	// a development tool a completed one.
	seen := c.activity.count()
	for index, deletion := range []struct {
		task  generated.Task
		actor string
	}{{rent, "user"}, {plants, "user"}, {old, "dev:cleanup"}} {
		status, raw := c.do(http.MethodDelete, "/v1/tasks/"+deletion.task.Id.String(), nil,
			activity.ActorHeader, deletion.actor, activity.CorrelationIDHeader, fmt.Sprintf("corr-%d", index))
		if status != http.StatusNoContent || len(raw) != 0 {
			t.Fatalf("delete %s: %d %q", deletion.task.Title, status, raw)
		}
	}

	// Gone from get, every list, search, and dueBefore.
	for _, task := range []generated.Task{rent, plants, old} {
		status, raw := c.do(http.MethodGet, "/v1/tasks/"+task.Id.String(), nil)
		expectError(t, "get "+task.Title, status, raw, http.StatusNotFound, "not_found")
	}
	for query, want := range map[string]string{
		"":                               "Call mom, Renew passport",
		"completed=false":                "Call mom, Renew passport",
		"completed=true":                 "",
		"undated=false":                  "Call mom",
		"query=rent":                     "",
		"query=plants":                   "",
		"query=test":                     "",
		"dueBefore=2026-10-20T00:00:00Z": "Call mom, Renew passport",
		"dueBefore=2026-10-20T00:00:00Z&includeUndated=false": "Call mom",
	} {
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.list(values); got != want {
			t.Errorf("list %q = %q, want %q", query, got, want)
		}
	}

	// One event each, by who deleted it, with what the Task was.
	deleted := c.activity.since(seen, "task.deleted")
	if len(deleted) != 3 || c.activity.count() != seen+3 {
		t.Fatalf("recorded %d task.deleted of %d events", len(deleted), c.activity.count()-seen)
	}
	for index, want := range []struct {
		task  generated.Task
		actor activity.Actor
	}{{rent, activity.User()}, {plants, activity.User()}, {old, activity.Dev("cleanup")}} {
		event := deleted[index]
		id := want.task.Id.String()
		if event.ID != activity.EventID("task.deleted", id) || event.Subject != (activity.Subject{Type: "task", ID: id}) || event.Source != "tasks" ||
			event.Actor != want.actor || event.Outcome != activity.Succeeded || event.CorrelationID != fmt.Sprintf("corr-%d", index) ||
			event.Summary != "Task “"+want.task.Title+"” deleted" || event.Data["title"] != want.task.Title {
			t.Errorf("deleted %s = %+v", want.task.Title, event)
		}
	}
	if deleted[0].Data["dueAt"] != "2026-10-12T09:00:00Z" || deleted[0].Data["completed"] != false || deleted[2].Data["completed"] != true {
		t.Errorf("deleted data = %v, %v", deleted[0].Data, deleted[2].Data)
	}

	// Deleting again, or an unknown Task, is a 404 and records nothing; a
	// deleted Task cannot be changed either.
	seen = c.activity.count()
	for what, target := range map[string]string{
		"delete again":     "/v1/tasks/" + rent.Id.String(),
		"delete unknown":   "/v1/tasks/" + googleuuid.NewString(),
		"complete deleted": "/v1/tasks/" + rent.Id.String() + "/complete",
		"reopen deleted":   "/v1/tasks/" + old.Id.String() + "/reopen",
	} {
		method := http.MethodDelete
		if !strings.HasPrefix(what, "delete") {
			method = http.MethodPost
		}
		status, raw := c.do(method, target, nil, activity.ActorHeader, "user")
		expectError(t, what, status, raw, http.StatusNotFound, "not_found")
	}
	status, raw = c.do(http.MethodPatch, "/v1/tasks/"+plants.Id.String(), map[string]any{"title": "Water the plants"}, activity.ActorHeader, "user")
	expectError(t, "update deleted", status, raw, http.StatusNotFound, "not_found")
	status, raw = c.do(http.MethodDelete, "/v1/tasks/not-a-uuid", nil, activity.ActorHeader, "user")
	expectError(t, "delete a malformed id", status, raw, http.StatusBadRequest, "bad_request")
	if c.activity.count() != seen {
		t.Fatalf("failed deletes recorded %d events", c.activity.count()-seen)
	}
}

// A Task is removed with its row, so its create idempotency key goes with
// it, as a deleted Calendar event's does: a replay after the delete creates
// the Task again.
func TestACreateReplayedAfterTheDeleteCreatesTheTaskAgain(t *testing.T) {
	c := newAPIClient(t)
	key := []string{activity.ActorHeader, "agent:codex-inbox-processor", "Idempotency-Key", "action-1"}
	body := map[string]any{"title": "Call the insurer"}
	status, raw := c.do(http.MethodPost, "/v1/tasks", body, key...)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var first generated.Task
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if status, raw := c.do(http.MethodDelete, "/v1/tasks/"+first.Id.String(), nil, activity.ActorHeader, "user"); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	status, raw = c.do(http.MethodPost, "/v1/tasks", body, key...)
	var again generated.Task
	if err := json.Unmarshal(raw, &again); status != http.StatusCreated || err != nil || again.Id == first.Id {
		t.Fatalf("replay after the delete: %d %s", status, raw)
	}
}
