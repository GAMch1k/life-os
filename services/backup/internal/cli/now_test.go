package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/backup/internal/api/generated"
)

func finished(id string, result generated.BackupResult) generated.BackupRun {
	started := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	errText := "connect: permission denied"
	return generated.BackupRun{
		Id: id, Trigger: generated.Manual, Scope: generated.Subset, Result: result,
		StartedAt: started, FinishedAt: started.Add(3 * time.Second), DurationSeconds: 3.2, TotalBytes: 1536,
		Databases: []generated.BackupDatabase{
			{Service: "finance", Name: "finance_db", Result: generated.BackupDatabaseResultSucceeded, Bytes: 1536, Tables: 5, Rows: 42, Migration: &generated.BackupMigration{Version: 9}},
			{Service: "tasks", Name: "tasks_db", Result: generated.BackupDatabaseResultFailed, Error: &errText},
		},
	}
}

func TestNowWaitsInTheRequest(t *testing.T) {
	var got generated.BackupCreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/backups" || r.URL.Query().Get("waitSeconds") == "" || r.Header.Get("X-Life-OS-Actor") != "dev:backup-now" {
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(finished("20261011T120000Z", generated.BackupResultFailed))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Now(context.Background(), []string{"-url", server.URL, "-actor", "dev:backup-now", "finance", "tasks_db"}, &stdout, &stderr)
	if code != ExitFailed {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	if got.Databases == nil || strings.Join(*got.Databases, ",") != "finance,tasks_db" {
		t.Errorf("request = %+v", got)
	}
	out := stdout.String()
	for _, want := range []string{"Backup 20261011T120000Z failed in 3.2 s (manual, some databases), 1.5 KiB.", "finance_db", "42 rows", "migration 9", "tasks_db", "failed: connect: permission denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestNowFollowsALongRunThroughTheStatus(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(generated.BackupProgress{Id: "20261011T120000Z", Trigger: generated.Manual, Scope: generated.All, Databases: []string{"finance_db"}})
		case r.URL.Path == "/v1/status":
			status := generated.BackupStatus{Databases: []string{"finance_db"}}
			if polls.Add(1) < 3 {
				status.Running = &generated.BackupProgress{Id: "20261011T120000Z", Databases: []string{"finance_db"}}
			} else {
				run := finished("20261011T120000Z", generated.BackupResultSucceeded)
				run.Databases = run.Databases[:1]
				status.LastRun = &run
			}
			_ = json.NewEncoder(w).Encode(status)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Now(context.Background(), []string{"-url", server.URL, "-poll", "10ms"}, &stdout, &stderr)
	if code != ExitSucceeded || polls.Load() != 3 {
		t.Fatalf("exit %d after %d polls, stderr %s", code, polls.Load(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "still running") || !strings.Contains(stdout.String(), "succeeded") {
		t.Errorf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestNowReportsTheServicesAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"backup_running","message":"A backup is already running; try again when it has finished."},"request_id":"r"}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := Now(context.Background(), []string{"-url", server.URL}, &stdout, &stderr); code != ExitUsage || !strings.Contains(stderr.String(), "already running") {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	if code := Now(context.Background(), []string{"-actor", "claude"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("an invalid actor: exit %d", code)
	}
	if code := Now(context.Background(), []string{"-url", "http://127.0.0.1:1"}, &stdout, &stderr); code != ExitFailed || !strings.Contains(stderr.String(), "cannot be reached") {
		t.Fatalf("no service: exit %d, stderr %s", code, stderr.String())
	}
}

func TestRestoreCheckWaitsAndPrints(t *testing.T) {
	var got generated.RestoreCheckCreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/restore-checks" || r.Header.Get("X-Life-OS-Actor") != "dev:backup-cli" {
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		errText := "1 of 2 databases did not restore as their manifest says"
		_ = json.NewEncoder(w).Encode(generated.RestoreCheck{
			Id: "20261011T012000Z", RunId: "20261011T011500Z", Trigger: generated.Manual, DurationSeconds: 4.2, Error: &errText,
			Databases: []generated.RestoreCheckDatabase{
				{Name: "finance_db", Ok: true, Tables: 5, Rows: 42, Mismatches: []generated.RestoreCheckMismatch{}},
				{Name: "tasks_db", Mismatches: []generated.RestoreCheckMismatch{{Subject: "public.tasks", Expected: "12 rows", Actual: "11 rows"}}},
			},
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := RestoreCheck(context.Background(), []string{"-url", server.URL, "-run", "20261011T011500Z"}, &stdout, &stderr)
	if code != ExitFailed || got.RunId == nil || *got.RunId != "20261011T011500Z" {
		t.Fatalf("exit %d, request %+v, stderr %s", code, got, stderr.String())
	}
	for _, want := range []string{"Restore check 20261011T012000Z of backup 20261011T011500Z failed in 4.2 s.", "finance_db", "42 rows", "tasks_db", "public.tasks: expected 12 rows, restored 11 rows"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, stdout.String())
		}
	}
	if code := RestoreCheck(context.Background(), []string{"extra"}, &stdout, &stderr); code != ExitUsage {
		t.Errorf("an argument: exit %d", code)
	}
}

func TestRestoreCheckFollowsTheStatus(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(generated.RestoreCheckProgress{Id: "20261011T012000Z", RunId: "20261011T011500Z", Databases: []string{"finance_db"}})
			return
		}
		status := generated.BackupStatus{Databases: []string{"finance_db"}}
		if polls.Add(1) < 2 {
			status.RunningRestoreCheck = &generated.RestoreCheckProgress{Id: "20261011T012000Z", Databases: []string{"finance_db"}}
		} else {
			status.LastRestoreCheck = &generated.RestoreCheck{Id: "20261011T012000Z", RunId: "20261011T011500Z", Ok: true, Databases: []generated.RestoreCheckDatabase{{Name: "finance_db", Ok: true, Mismatches: []generated.RestoreCheckMismatch{}}}}
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := RestoreCheck(context.Background(), []string{"-url", server.URL, "-poll", "10ms"}, &stdout, &stderr); code != ExitSucceeded || !strings.Contains(stdout.String(), "passed") {
		t.Fatalf("exit %d, stdout %s, stderr %s", code, stdout.String(), stderr.String())
	}
}
