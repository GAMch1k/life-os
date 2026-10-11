package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeRestorer passes every database except those in differ; with block
// set, each check waits for a value on it or for the context.
type fakeRestorer struct {
	mu       sync.Mutex
	differ   map[string]bool
	block    chan struct{}
	checked  []string
	dirs     []string
	leftover []string
	sweeps   int
}

func (f *fakeRestorer) Check(ctx context.Context, dir string, database DatabaseResult) DatabaseCheck {
	f.mu.Lock()
	f.checked = append(f.checked, database.Name)
	f.dirs = append(f.dirs, dir)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return DatabaseCheck{Name: database.Name, Mismatches: []Mismatch{}, Error: "pg_restore stopped"}
		}
	}
	check := DatabaseCheck{Name: database.Name, OK: true, Tables: 1, Rows: 2, Mismatches: []Mismatch{}}
	if f.differ[database.Name] {
		check.OK = false
		check.Mismatches = []Mismatch{{Subject: "public.items", Expected: "2 rows", Actual: "1 row"}}
	}
	return check
}

func (f *fakeRestorer) DropLeftovers(_ context.Context, databases []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps++
	if len(databases) == 0 {
		return nil, errors.New("no database to connect through")
	}
	dropped := f.leftover
	f.leftover = nil
	return dropped, nil
}

func newCheckRunner(t *testing.T, restorer *fakeRestorer, dumper DatabaseDumper) (*Runner, *Store) {
	t.Helper()
	store := openStore(t)
	runner := NewRunner(RunnerOptions{
		Store:     store,
		Dumper:    dumper,
		Restorer:  restorer,
		Registry:  func() ([]Database, error) { return testRegistry, nil },
		Retention: Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3},
	})
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner, store
}

func waitCheck(t *testing.T, job *CheckJob) RestoreCheck {
	t.Helper()
	select {
	case <-job.Done():
		return job.Result()
	case <-time.After(10 * time.Second):
		t.Fatal("the restore check did not finish")
		return RestoreCheck{}
	}
}

func backupNow(t *testing.T, runner *Runner, request Request) Summary {
	t.Helper()
	job, err := runner.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, job)
}

func TestARestoreCheckCoversTheNewestSuccessfulFullRun(t *testing.T) {
	restorer := &fakeRestorer{differ: map[string]bool{"tasks_db": true}}
	runner, store := newCheckRunner(t, restorer, &fakeDumper{})
	if _, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual}); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("a check without backups = %v", err)
	}
	full := backupNow(t, runner, Request{Trigger: TriggerSchedule})
	// A newer subset run and a newer failed run are not what the weekly
	// check covers.
	backupNow(t, runner, Request{Trigger: TriggerManual, Databases: []string{"tasks"}})

	job, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual, Actor: "dev:test"})
	if err != nil {
		t.Fatal(err)
	}
	if progress := job.Progress(); progress.RunID != full.ID || len(progress.Databases) != 3 {
		t.Fatalf("progress = %+v", progress)
	}
	check := waitCheck(t, job)
	if check.OK || check.RunID != full.ID || check.Actor != "dev:test" || len(check.Databases) != 3 || check.Error != "1 of 3 databases did not restore as their manifest says" {
		t.Fatalf("check = %+v", check)
	}
	if !check.Databases[0].OK || check.Databases[1].OK || check.Databases[1].Mismatches[0].Subject != "public.items" {
		t.Fatalf("databases = %+v", check.Databases)
	}
	if restorer.dirs[0] != filepath.Join(store.Root(), AutoDir, full.ID) || restorer.sweeps != 1 {
		t.Fatalf("dir %s, sweeps %d", restorer.dirs[0], restorer.sweeps)
	}
	status, err := runner.Status()
	if err != nil || status.LastRestoreCheck == nil || status.LastRestoreCheck.ID != check.ID || status.RunningCheck != nil {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if checks, _ := store.RestoreChecks(); len(checks) != 1 {
		t.Fatalf("recorded checks = %+v", checks)
	}
}

func TestARestoreCheckOfANamedRun(t *testing.T) {
	restorer := &fakeRestorer{}
	runner, _ := newCheckRunner(t, restorer, &fakeDumper{fail: map[string]bool{"tasks_db": true}})
	failed := backupNow(t, runner, Request{Trigger: TriggerSchedule})
	var unknown *UnknownRunError
	if _, err := runner.StartRestoreCheck(CheckRequest{RunID: "20200101T000000Z"}); !errors.As(err, &unknown) {
		t.Fatalf("an unknown run = %v", err)
	}
	// A failed run's dumps that worked can be checked on demand.
	job, err := runner.StartRestoreCheck(CheckRequest{RunID: failed.ID, Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if check := waitCheck(t, job); !check.OK || len(check.Databases) != 2 || !slices.Equal(restorer.checked, []string{"finance_db", "calendar_db"}) {
		t.Fatalf("check = %+v, checked %v", check, restorer.checked)
	}
}

func TestRunsAndChecksNeverOverlap(t *testing.T) {
	restorer := &fakeRestorer{block: make(chan struct{})}
	dumper := &fakeDumper{}
	runner, store := newCheckRunner(t, restorer, dumper)
	backupNow(t, runner, Request{Trigger: TriggerSchedule})
	job, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(Request{Trigger: TriggerManual}); !errors.Is(err, ErrRunning) {
		t.Fatalf("a backup during a check = %v", err)
	}
	if _, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual}); !errors.Is(err, ErrRunning) {
		t.Fatalf("a second check = %v", err)
	}
	status, _ := runner.Status()
	if status.RunningCheck == nil || status.RunningCheck.ID != job.ID() {
		t.Fatalf("running check = %+v", status.RunningCheck)
	}
	close(restorer.block)
	waitCheck(t, job)

	// Another process holding the lock counts too.
	lock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual}); !errors.Is(err, ErrRunning) {
		t.Fatalf("a check while another process holds the lock = %v", err)
	}
	lock.Release()
}

func TestANightlyRunCanBeFollowedByACheckUnderTheSameLock(t *testing.T) {
	restorer := &fakeRestorer{block: make(chan struct{})}
	runner, store := newCheckRunner(t, restorer, &fakeDumper{})
	job, err := runner.Start(Request{Trigger: TriggerSchedule, ThenRestoreCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	summary := wait(t, job)
	// The run is done, the check is going, and nothing else may start.
	if check := runner.CurrentCheck(); check == nil || check.Progress().RunID != summary.ID || check.Progress().Trigger != TriggerSchedule {
		t.Fatalf("current check = %+v", check)
	}
	if _, err := runner.Start(Request{Trigger: TriggerManual}); !errors.Is(err, ErrRunning) {
		t.Fatalf("a backup during the follow-up check = %v", err)
	}
	if _, err := store.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("the lock is free during the follow-up check: %v", err)
	}
	check := runner.CurrentCheck()
	close(restorer.block)
	if result := waitCheck(t, check); !result.OK {
		t.Fatalf("check = %+v", result)
	}
	if runner.CurrentCheck() != nil {
		t.Fatal("the check is still current")
	}
	if lock, err := store.Lock(); err != nil {
		t.Fatalf("the lock was not released: %v", err)
	} else {
		lock.Release()
	}
}

func TestAFollowUpCheckNeedsASuccessfulFullRun(t *testing.T) {
	restorer := &fakeRestorer{}
	runner, store := newCheckRunner(t, restorer, &fakeDumper{fail: map[string]bool{"finance_db": true}})
	backupNow(t, runner, Request{Trigger: TriggerSchedule, ThenRestoreCheck: true})
	if runner.CurrentCheck() != nil || len(restorer.checked) != 0 {
		t.Fatalf("a check ran without a successful full run: %v", restorer.checked)
	}
	if checks, _ := store.RestoreChecks(); len(checks) != 0 {
		t.Fatalf("checks = %+v", checks)
	}
	if lock, err := store.Lock(); err != nil {
		t.Fatalf("the lock was not released: %v", err)
	} else {
		lock.Release()
	}
}

func TestStoppingDuringACheckRecordsIt(t *testing.T) {
	restorer := &fakeRestorer{block: make(chan struct{})}
	runner, store := newCheckRunner(t, restorer, &fakeDumper{})
	backupNow(t, runner, Request{Trigger: TriggerSchedule})
	job, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	restorer.block <- struct{}{}
	if err := runner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	check := waitCheck(t, job)
	if check.OK || check.Error != "stopped before it finished: the backup service shut down" {
		t.Fatalf("check = %+v", check)
	}
	if checks, _ := store.RestoreChecks(); len(checks) != 1 || checks[0].OK {
		t.Fatalf("checks = %+v", checks)
	}
}

func TestRecoverDropsScratchDatabases(t *testing.T) {
	restorer := &fakeRestorer{leftover: []string{"lifeos_restore_check_finance_db"}}
	runner, _ := newCheckRunner(t, restorer, &fakeDumper{})
	if err := runner.Recover(); err != nil {
		t.Fatal(err)
	}
	if restorer.sweeps != 1 || restorer.leftover != nil {
		t.Fatalf("sweeps %d, leftover %v", restorer.sweeps, restorer.leftover)
	}
}

func TestRestoreChecksHistory(t *testing.T) {
	store := openStore(t)
	start := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	for i := range ChecksLimit + 3 {
		at := start.Add(time.Duration(i) * 24 * time.Hour)
		if err := store.RecordCheck(RestoreCheck{ID: at.Format(RunIDLayout), StartedAt: at, OK: i%2 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	checks, err := store.RestoreChecks()
	if err != nil || len(checks) != ChecksLimit || !checks[0].StartedAt.Equal(start.Add(time.Duration(ChecksLimit+2)*24*time.Hour)) {
		t.Fatalf("checks = %d, %v", len(checks), err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), AutoDir, ChecksFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreChecks(); err == nil {
		t.Fatal("a damaged history read without error")
	}
}
