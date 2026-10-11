package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/backup/internal/api/generated"
	"github.com/GAMch1k/life-os/services/backup/internal/backup"
)

type fakeRunner struct {
	runs     []backup.Summary
	status   backup.Status
	job      *backup.Job
	startErr error
	request  backup.Request
}

func (f *fakeRunner) Start(request backup.Request) (*backup.Job, error) {
	f.request = request
	return f.job, f.startErr
}
func (f *fakeRunner) Runs() ([]backup.Summary, error)   { return f.runs, nil }
func (f *fakeRunner) Status() (backup.Status, error)    { return f.status, nil }
func (fakeScheduler) Next() (time.Time, backup.Trigger) { return next, backup.TriggerSchedule }

type fakeScheduler struct{}

var next = time.Date(2026, 10, 12, 1, 15, 0, 0, time.UTC)

func newApp(t *testing.T, runner Runner) *httpserver.App {
	t.Helper()
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	app := httpserver.New("backup")
	Register(app, NewHandler(runner, fakeScheduler{}, Settings{
		Schedule:  backup.Schedule{Minute: 195, Location: location},
		Retention: backup.Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3},
	}))
	return app
}

func call(t *testing.T, app *httpserver.App, method, target, body string, actor bool) (int, []byte) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if actor {
		request.Header.Set("X-Life-OS-Actor", "dev:claude-code")
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data
}

func summary(id string, result backup.Result, scope backup.Scope) backup.Summary {
	started, _ := time.Parse(backup.RunIDLayout, id)
	return backup.Summary{
		ID: id, Trigger: backup.TriggerSchedule, Scope: scope, Result: result, StartedAt: started, FinishedAt: started.Add(2 * time.Second),
		DurationSeconds: 2, ServerVersion: "18.6", TotalBytes: 2048,
		Databases: []backup.DatabaseSummary{{Service: "finance", Name: "finance_db", Result: backup.ResultSucceeded, File: "finance_db.dump", Bytes: 2048, SHA256: "ab", Tables: 4, Rows: 120, Migration: &backup.Migration{Version: 7}}},
	}
}

func TestGetBackupStatus(t *testing.T) {
	last := summary("20261011T011500Z", backup.ResultFailed, backup.ScopeAll)
	last.Error = "1 of 2 databases failed"
	success := summary("20261010T011500Z", backup.ResultSucceeded, backup.ScopeAll)
	runner := &fakeRunner{status: backup.Status{
		LastRun: &last, LastSuccess: &success, Runs: 2, TotalBytes: 4096, FreeBytes: 10 << 30, SizeBytes: 50 << 30,
		Running:   &backup.Progress{ID: "20261011T120000Z", Trigger: backup.TriggerManual, Scope: backup.ScopeSubset, Actor: "user", Databases: []string{"tasks_db"}, Current: "tasks_db"},
		Databases: []string{"finance_db", "tasks_db"},
	}}
	status, body := call(t, newApp(t, runner), http.MethodGet, "/v1/status", "", false)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got generated.BackupStatus
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schedule.Time != "03:15" || got.Schedule.TimeZone != "Europe/Berlin" || !got.Schedule.NextRunAt.Equal(next) || got.Schedule.NextTrigger != generated.Schedule {
		t.Errorf("schedule = %+v", got.Schedule)
	}
	if got.LastRun == nil || got.LastRun.Result != generated.BackupResultFailed || *got.LastRun.Error != "1 of 2 databases failed" || got.LastRun.Databases[0].Migration.Version != 7 {
		t.Errorf("lastRun = %+v", got.LastRun)
	}
	if got.LastSuccess == nil || got.LastSuccess.Id != "20261010T011500Z" || got.Running == nil || *got.Running.Current != "tasks_db" {
		t.Errorf("lastSuccess = %+v, running = %+v", got.LastSuccess, got.Running)
	}
	if got.Storage.Runs != 2 || got.Storage.FreeBytes != 10<<30 || got.Retention.Daily != 7 || got.Retention.FailedDays != 3 || len(got.Databases) != 2 {
		t.Errorf("status = %+v", got)
	}
}

func TestListBackupsPages(t *testing.T) {
	runner := &fakeRunner{runs: []backup.Summary{
		summary("20261011T011500Z", backup.ResultSucceeded, backup.ScopeAll),
		summary("20261010T011500Z", backup.ResultSucceeded, backup.ScopeSubset),
		summary("20261009T011500Z", backup.ResultFailed, backup.ScopeAll),
	}}
	app := newApp(t, runner)
	status, body := call(t, app, http.MethodGet, "/v1/backups?limit=2", "", false)
	var page generated.BackupList
	if err := json.Unmarshal(body, &page); status != http.StatusOK || err != nil {
		t.Fatalf("status %d: %s", status, body)
	}
	if len(page.Items) != 2 || page.Items[0].Id != "20261011T011500Z" || page.NextCursor == nil || *page.NextCursor != "20261010T011500Z" {
		t.Fatalf("first page = %+v", page)
	}
	if page.Items[1].Scope != generated.Subset || page.Items[0].Databases[0].Rows != 120 {
		t.Errorf("items = %+v", page.Items)
	}
	_, body = call(t, app, http.MethodGet, "/v1/backups?limit=2&cursor="+*page.NextCursor, "", false)
	page = generated.BackupList{}
	if err := json.Unmarshal(body, &page); err != nil || len(page.Items) != 1 || page.Items[0].Id != "20261009T011500Z" || page.NextCursor != nil {
		t.Fatalf("second page = %s", body)
	}
	for _, target := range []string{"/v1/backups?cursor=yesterday", "/v1/backups?limit=0", "/v1/backups?limit=101"} {
		if status, body := call(t, app, http.MethodGet, target, "", false); status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", target, status, body)
		}
	}
}

func TestCreateBackupNeedsAnActor(t *testing.T) {
	runner := &fakeRunner{}
	status, body := call(t, newApp(t, runner), http.MethodPost, "/v1/backups", "", false)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "actor_required") {
		t.Fatalf("status %d: %s", status, body)
	}
}

func TestCreateBackupAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		body   string
		status int
		code   string
	}{
		{"running", backup.ErrRunning, "", http.StatusConflict, "backup_running"},
		{"unknown", &backup.UnknownDatabasesError{Names: []string{"food"}}, `{"databases":["food"]}`, http.StatusUnprocessableEntity, "unknown_database"},
		{"cannot start", errors.New("read the database registry: missing"), "", http.StatusInternalServerError, "internal_error"},
		{"bad body", nil, `{"databases":`, http.StatusBadRequest, "bad_request"},
		{"empty list", nil, `{"databases":[]}`, http.StatusBadRequest, "bad_request"},
	} {
		runner := &fakeRunner{startErr: tc.err}
		status, body := call(t, newApp(t, runner), http.MethodPost, "/v1/backups", tc.body, true)
		if status != tc.status || !strings.Contains(string(body), `"`+tc.code+`"`) {
			t.Errorf("%s: %d %s", tc.name, status, body)
		}
	}
}

// A real run through the handler: 202 at once, 200 with the finished run
// when it waits.
func TestCreateBackupStartsARun(t *testing.T) {
	store, err := backup.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := backup.NewRunner(backup.RunnerOptions{
		Store:  store,
		Dumper: instantDumper{},
		Registry: func() ([]backup.Database, error) {
			return []backup.Database{{Service: "finance", Name: "finance_db"}, {Service: "tasks", Name: "tasks_db"}}, nil
		},
		Retention: backup.Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3},
	})
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	app := newApp(t, runner)

	status, body := call(t, app, http.MethodPost, "/v1/backups?waitSeconds=10", `{"databases":["tasks"]}`, true)
	var run generated.BackupRun
	if err := json.Unmarshal(body, &run); status != http.StatusOK || err != nil {
		t.Fatalf("status %d: %s", status, body)
	}
	if run.Result != generated.BackupResultSucceeded || run.Scope != generated.Subset || run.Trigger != generated.Manual || *run.Actor != "dev:claude-code" || len(run.Databases) != 1 {
		t.Fatalf("run = %s", body)
	}

	status, body = call(t, app, http.MethodPost, "/v1/backups", "", true)
	var progress generated.BackupProgress
	if err := json.Unmarshal(body, &progress); status != http.StatusAccepted || err != nil || progress.Scope != generated.All || len(progress.Databases) != 2 {
		t.Fatalf("status %d: %s", status, body)
	}
	if job := runner.Current(); job != nil {
		<-job.Done()
	}
	runs, _ := runner.Runs()
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
}

type instantDumper struct{}

func (instantDumper) Version(context.Context) (string, error) {
	return "pg_dump (PostgreSQL) 18.6", nil
}

func (instantDumper) Dump(_ context.Context, database backup.Database, dir string) (backup.DatabaseResult, string) {
	path := dir + "/" + database.Name + ".dump"
	if err := os.WriteFile(path, []byte("PGDMP"), 0o600); err != nil {
		return backup.DatabaseResult{Name: database.Name, Result: backup.ResultFailed, Error: err.Error()}, ""
	}
	return backup.DatabaseResult{Service: database.Service, Name: database.Name, Result: backup.ResultSucceeded, File: database.Name + ".dump", Bytes: 5, Tables: []backup.TableRows{}}, "18.6"
}
