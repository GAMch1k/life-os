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

// fakeDumper writes a small file per database. A database in fail fails;
// with block set, each dump waits for a value on it or for the context.
type fakeDumper struct {
	fail  map[string]bool
	block chan struct{}
	mu    sync.Mutex
	order []string
	// media are the media files each database refers to.
	media map[string][]MediaRef
}

func (f *fakeDumper) Version(context.Context) (string, error) {
	return "pg_dump (PostgreSQL) 18.6", nil
}

func (f *fakeDumper) Dump(ctx context.Context, database Database, dir string) (DatabaseResult, string) {
	f.mu.Lock()
	f.order = append(f.order, database.Name)
	f.mu.Unlock()
	result := DatabaseResult{Service: database.Service, Name: database.Name, Tables: []TableRows{{"public", "items", 2}}}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			result.Result, result.Error = ResultFailed, "pg_dump stopped: "+ctx.Err().Error()
			return result, "18.6"
		}
	}
	if f.fail[database.Name] {
		result.Result, result.Error, result.Tables = ResultFailed, "connect: permission denied for database "+database.Name, []TableRows{}
		return result, "18.6"
	}
	if err := os.WriteFile(filepath.Join(dir, database.Name+".dump"), []byte("PGDMP "+database.Name), 0o600); err != nil {
		result.Result, result.Error = ResultFailed, err.Error()
		return result, "18.6"
	}
	result.Result, result.File, result.Bytes, result.SHA256 = ResultSucceeded, database.Name+".dump", int64(len("PGDMP "+database.Name)), "abc"
	f.mu.Lock()
	result.Media = f.media[database.Name]
	f.mu.Unlock()
	return result, "18.6"
}

var testRegistry = []Database{{"finance", "finance_db"}, {"tasks", "tasks_db"}, {"calendar", "calendar_db"}}

func newTestRunner(t *testing.T, dumper DatabaseDumper, now func() time.Time) (*Runner, *Store) {
	t.Helper()
	store := openStore(t)
	runner := NewRunner(RunnerOptions{
		Store:     store,
		Dumper:    dumper,
		Registry:  func() ([]Database, error) { return testRegistry, nil },
		Retention: Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3},
		Now:       now,
	})
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner, store
}

func wait(t *testing.T, job *Job) Summary {
	t.Helper()
	select {
	case <-job.Done():
		return job.Summary()
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not finish")
		return Summary{}
	}
}

func TestOneFailingDatabaseDoesNotStopTheOthers(t *testing.T) {
	dumper := &fakeDumper{fail: map[string]bool{"tasks_db": true}}
	runner, store := newTestRunner(t, dumper, nil)
	job, err := runner.Start(Request{Trigger: TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	summary := wait(t, job)
	if summary.Result != ResultFailed || summary.Error != "1 of 3 databases failed" || summary.Scope != ScopeAll || summary.ServerVersion != "18.6" {
		t.Fatalf("summary = %+v", summary)
	}
	if !slices.Equal(dumper.order, []string{"finance_db", "tasks_db", "calendar_db"}) {
		t.Fatalf("dumped %v", dumper.order)
	}
	runs, err := store.Runs()
	if err != nil || len(runs) != 1 || runs[0].ID != job.ID() {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	manifest := runs[0].Manifest
	if manifest.PgDumpVersion != "pg_dump (PostgreSQL) 18.6" || manifest.Databases[1].Error == "" || manifest.Databases[2].Result != ResultSucceeded {
		t.Fatalf("manifest = %+v", manifest)
	}
	for name, want := range map[string]bool{"finance_db.dump": true, "tasks_db.dump": false, "calendar_db.dump": true} {
		_, err := os.Stat(filepath.Join(store.Root(), AutoDir, job.ID(), name))
		if (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
	history, _ := store.History()
	if len(history) != 1 || history[0].ID != job.ID() || history[0].Result != ResultFailed {
		t.Fatalf("history = %+v", history)
	}
	if runner.Current() != nil {
		t.Fatal("the run is still current")
	}
}

func TestOnlyOneRunAtATime(t *testing.T) {
	dumper := &fakeDumper{block: make(chan struct{})}
	runner, store := newTestRunner(t, dumper, nil)
	job, err := runner.Start(Request{Trigger: TriggerManual, Actor: "dev:test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(Request{Trigger: TriggerSchedule}); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Start = %v, want ErrRunning", err)
	}
	progress := job.Progress()
	if progress.ID != job.ID() || progress.Actor != "dev:test" || len(progress.Databases) != 3 {
		t.Fatalf("progress = %+v", progress)
	}
	close(dumper.block)
	if summary := wait(t, job); summary.Result != ResultSucceeded || summary.Actor != "dev:test" {
		t.Fatalf("summary = %+v", summary)
	}

	// Another process holding the lock (a second container) also counts.
	lock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(Request{Trigger: TriggerSchedule}); !errors.Is(err, ErrRunning) {
		t.Fatalf("Start while another process holds the lock = %v", err)
	}
	lock.Release()
	if job, err := runner.Start(Request{Trigger: TriggerSchedule}); err != nil {
		t.Fatal(err)
	} else {
		wait(t, job)
	}
}

func TestASubsetRunNamesItsDatabases(t *testing.T) {
	runner, store := newTestRunner(t, &fakeDumper{}, nil)
	_, err := runner.Start(Request{Trigger: TriggerManual, Databases: []string{"tasks", "food"}})
	var unknown *UnknownDatabasesError
	if !errors.As(err, &unknown) || !slices.Equal(unknown.Names, []string{"food"}) {
		t.Fatalf("Start(food) = %v", err)
	}
	if history, _ := store.History(); len(history) != 0 {
		t.Fatalf("an invalid request was recorded: %+v", history)
	}
	job, err := runner.Start(Request{Trigger: TriggerManual, Databases: []string{"calendar_db", "finance"}})
	if err != nil {
		t.Fatal(err)
	}
	summary := wait(t, job)
	if summary.Scope != ScopeSubset || len(summary.Databases) != 2 || summary.Databases[0].Name != "finance_db" || summary.Result != ResultSucceeded {
		t.Fatalf("summary = %+v", summary)
	}
	// Naming every database is a full run.
	job, err = runner.Start(Request{Trigger: TriggerManual, Databases: []string{"finance", "tasks", "calendar"}})
	if err != nil {
		t.Fatal(err)
	}
	if summary := wait(t, job); summary.Scope != ScopeAll {
		t.Fatalf("scope = %s", summary.Scope)
	}
	if last, err := runner.LastFullSuccess(); err != nil || last == nil || last.ID != job.ID() {
		t.Fatalf("LastFullSuccess = %+v, %v", last, err)
	}
	// The subset run before it is not a full success; this is the only one.
	if status, err := runner.Status(); err != nil || !status.OldestSuccess.Equal(status.LastSuccess.StartedAt) {
		t.Fatalf("OldestSuccess = %v, %v", status.OldestSuccess, err)
	}
}

func TestARunThatCannotStartIsRecorded(t *testing.T) {
	store := openStore(t)
	runner := NewRunner(RunnerOptions{
		Store:    store,
		Dumper:   &fakeDumper{},
		Registry: func() ([]Database, error) { return nil, os.ErrNotExist },
	})
	if _, err := runner.Start(Request{Trigger: TriggerSchedule}); err == nil {
		t.Fatal("Start succeeded without a registry")
	}
	history, _ := store.History()
	if len(history) != 1 || history[0].Result != ResultFailed || history[0].Trigger != TriggerSchedule || history[0].Error == "" {
		t.Fatalf("history = %+v", history)
	}
	status, err := runner.Status()
	if err != nil || status.LastRun == nil || status.LastRun.Result != ResultFailed || status.DatabasesError == "" || status.LastSuccess != nil {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestCloseInterruptsARunAndRemovesItsFiles(t *testing.T) {
	dumper := &fakeDumper{block: make(chan struct{})}
	runner, store := newTestRunner(t, dumper, nil)
	job, err := runner.Start(Request{Trigger: TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	dumper.block <- struct{}{} // finance_db completes
	if err := runner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	summary := wait(t, job)
	if summary.Result != ResultInterrupted || summary.TotalBytes != 0 || summary.Databases[0].File != "" {
		t.Fatalf("summary = %+v", summary)
	}
	entries, _ := os.ReadDir(filepath.Join(store.Root(), AutoDir))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Errorf("left %s behind", entry.Name())
		}
	}
	if history, _ := store.History(); len(history) != 1 || history[0].Result != ResultInterrupted {
		t.Fatalf("history = %+v", history)
	}
	if _, err := runner.Start(Request{Trigger: TriggerManual}); !errors.Is(err, ErrStopped) {
		t.Fatalf("Start after Close = %v", err)
	}
}

func TestRecoverRecordsRunsACrashLeftBehind(t *testing.T) {
	runner, store := newTestRunner(t, &fakeDumper{}, nil)
	started := time.Now().UTC().Add(-time.Hour)
	manifest, dir, err := store.Begin(Manifest{FormatVersion: ManifestVersion, Trigger: TriggerCatchUp, Scope: ScopeAll, StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finance_db.dump.partial"), []byte("PGD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the partial directory survived: %v", err)
	}
	history, _ := store.History()
	if len(history) != 1 || history[0].ID != manifest.ID || history[0].Result != ResultInterrupted || history[0].Trigger != TriggerCatchUp || history[0].FinishedAt.Before(started) {
		t.Fatalf("history = %+v", history)
	}
	status, err := runner.Status()
	if err != nil || status.LastRun == nil || status.LastRun.Result != ResultInterrupted {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestEachRunAppliesTheRotation(t *testing.T) {
	now := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	runner, store := newTestRunner(t, &fakeDumper{}, func() time.Time { return now })
	// Twenty nights of full runs and an old failure, written as earlier runs would have.
	var ids []string
	for day := 20; day >= 1; day-- {
		at := now.AddDate(0, 0, -day)
		manifest, _, err := store.Begin(Manifest{FormatVersion: ManifestVersion, Trigger: TriggerSchedule, Scope: ScopeAll, StartedAt: at, Databases: []DatabaseResult{}})
		if err != nil {
			t.Fatal(err)
		}
		manifest.Databases = []DatabaseResult{{Name: "finance_db", Result: ResultSucceeded}}
		manifest.finish(at.Add(time.Minute))
		if day == 10 {
			manifest.Result = ResultFailed
		}
		if err := store.Complete(manifest); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, manifest.ID)
	}
	job, err := runner.Start(Request{Trigger: TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, job)
	runs, _ := store.Runs()
	var kept []string
	for _, run := range runs {
		kept = append(kept, run.ID)
	}
	// Today and the 6 nights before it; the newest run of each earlier ISO
	// week (4 October, 27 September); September's newest run for its month.
	// The failure is older than 3 days.
	want := []string{job.ID(), ids[19], ids[18], ids[17], ids[16], ids[15], ids[14], ids[13], ids[6], ids[9]}
	slices.Sort(want)
	slices.Sort(kept)
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v\nwant %v", kept, want)
	}
}

// fakeTarget records what the runner gives it; with fail set, Put fails.
type fakeTarget struct {
	name    string
	fail    bool
	puts    []string
	deletes []string
}

func (f *fakeTarget) Name() string { return f.name }

func (f *fakeTarget) Put(_ context.Context, manifest Manifest, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		return err
	}
	if f.fail {
		return errors.New("bucket unreachable")
	}
	f.puts = append(f.puts, manifest.ID)
	return nil
}

func (f *fakeTarget) Delete(_ context.Context, id string) error {
	f.deletes = append(f.deletes, id)
	return nil
}

func TestTargetsReceiveCompletedRunsAndDeletions(t *testing.T) {
	now := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	good, bad := &fakeTarget{name: "offsite"}, &fakeTarget{name: "broken", fail: true}
	store := openStore(t)
	runner := NewRunner(RunnerOptions{
		Store:     store,
		Dumper:    &fakeDumper{},
		Registry:  func() ([]Database, error) { return testRegistry, nil },
		Retention: Retention{Daily: 1, FailedDays: 1},
		Targets:   []Target{good, bad},
		Now:       func() time.Time { return now },
	})
	t.Cleanup(func() { _ = runner.Close(context.Background()) })

	first, err := runner.Start(Request{Trigger: TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	summary := wait(t, first)
	if summary.Result != ResultSucceeded || len(summary.Copies) != 2 || summary.Copies[0] != (Copy{Target: "offsite", Result: ResultSucceeded}) || summary.Copies[1].Result != ResultFailed || summary.Copies[1].Error != "bucket unreachable" {
		t.Fatalf("summary = %+v", summary)
	}
	if history, _ := store.History(); len(history) != 1 || len(history[0].Copies) != 2 {
		t.Fatalf("history = %+v", history)
	}
	// The next day's run makes the rotation (one day kept) delete the first,
	// on the targets too.
	now = now.Add(24 * time.Hour)
	second, err := runner.Start(Request{Trigger: TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, second)
	if !slices.Equal(good.puts, []string{first.ID(), second.ID()}) || !slices.Equal(good.deletes, []string{first.ID()}) || !slices.Equal(bad.deletes, []string{first.ID()}) {
		t.Fatalf("puts %v, deletes %v / %v", good.puts, good.deletes, bad.deletes)
	}
}
