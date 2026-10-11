package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestIsRunID(t *testing.T) {
	for _, name := range []string{"20261011T031500Z", "20240229T000000Z"} {
		if !IsRunID(name) {
			t.Errorf("IsRunID(%q) = false", name)
		}
	}
	for _, name := range []string{"", "20261011T031500", ".partial-20261011T031500Z", "20261311T031500Z", "20230229T000000Z", "finance_db.dump", "../20261011T031500Z", "20261011T031500Zx"} {
		if IsRunID(name) {
			t.Errorf("IsRunID(%q) = true", name)
		}
	}
}

func TestOpenStoreNeedsAnExistingDirectory(t *testing.T) {
	if _, err := OpenStore(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing backup directory was accepted")
	}
	store := openStore(t)
	if info, err := os.Stat(filepath.Join(store.Root(), AutoDir)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("auto/ = %v, %v; want a 0700 directory", info, err)
	}
	if err := store.Writable(); err != nil {
		t.Fatal(err)
	}
}

func TestARunAppearsOnlyOnceComplete(t *testing.T) {
	store := openStore(t)
	started := time.Date(2026, 10, 11, 1, 15, 0, 300, time.UTC)
	manifest, dir, err := store.Begin(Manifest{FormatVersion: ManifestVersion, Trigger: TriggerSchedule, Scope: ScopeAll, StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "20261011T011500Z" || filepath.Base(dir) != ".partial-20261011T011500Z" {
		t.Fatalf("Begin = %s in %s", manifest.ID, dir)
	}
	if err := os.WriteFile(filepath.Join(dir, "finance_db.dump"), []byte("PGDMP"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runs, _ := store.Runs(); len(runs) != 0 {
		t.Fatalf("a partial run is listed: %+v", runs)
	}
	// A second run in the same second gets the next second's name.
	second, secondDir, err := store.Begin(Manifest{StartedAt: started})
	if err != nil || second.ID != "20261011T011501Z" {
		t.Fatalf("second Begin = %s, %v", second.ID, err)
	}
	if err := os.RemoveAll(secondDir); err != nil {
		t.Fatal(err)
	}

	manifest.Databases = []DatabaseResult{{Service: "finance", Name: "finance_db", Result: ResultSucceeded, File: "finance_db.dump", Bytes: 5, Tables: []TableRows{{"public", "accounts", 3}}}}
	manifest.finish(started.Add(1500 * time.Millisecond))
	if err := store.Complete(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the partial directory is still there: %v", err)
	}
	runs, err := store.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	got := runs[0].Manifest
	if runs[0].Err != nil || got.Result != ResultSucceeded || got.DurationSeconds != 1.5 || got.Databases[0].Tables[0].Rows != 3 {
		t.Fatalf("manifest = %+v, %v", got, runs[0].Err)
	}
	if info, err := os.Stat(filepath.Join(store.Root(), AutoDir, manifest.ID, ManifestFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest.json = %v, %v", info, err)
	}
	summary := got.Summary()
	if summary.TotalBytes != 5 || summary.Databases[0].Rows != 3 || summary.Databases[0].Tables != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestManifestResult(t *testing.T) {
	started := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	m := Manifest{StartedAt: started, Databases: []DatabaseResult{{Result: ResultSucceeded}, {Result: ResultFailed}, {Result: ResultSucceeded}}}
	m.finish(started.Add(time.Minute))
	if m.Result != ResultFailed || m.Error != "1 of 3 databases failed" || m.DurationSeconds != 60 {
		t.Fatalf("finish = %+v", m)
	}
	empty := Manifest{StartedAt: started}
	empty.finish(started)
	if empty.Result != ResultFailed {
		t.Fatalf("a run of no databases = %s", empty.Result)
	}
}

func TestLeftoversAndDeleteTouchOnlyRunDirectories(t *testing.T) {
	store := openStore(t)
	auto := filepath.Join(store.Root(), AutoDir)
	// What else lives in the backup directory is never touched.
	for _, path := range []string{filepath.Join(store.Root(), "finance_db-before-p11-09.dump"), filepath.Join(auto, "notes.txt")} {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"keep-me", "2026-10-11", ".partial-garbage"} {
		if err := os.Mkdir(filepath.Join(auto, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _, err := store.Begin(Manifest{Trigger: TriggerManual, Scope: ScopeSubset, Actor: "user", StartedAt: time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	leftovers, err := store.Leftovers()
	if err != nil || len(leftovers) != 1 || leftovers[0].ID != manifest.ID || leftovers[0].Manifest.Actor != "user" || leftovers[0].StoppedAt.IsZero() {
		t.Fatalf("Leftovers = %+v, %v", leftovers, err)
	}
	if err := store.Discard(manifest.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"keep-me", "../finance_db-before-p11-09.dump", ".partial-garbage", ""} {
		if err := store.Delete(id); err == nil {
			t.Errorf("Delete(%q) accepted", id)
		}
	}
	runs, err := store.Runs()
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	for _, path := range []string{filepath.Join(store.Root(), "finance_db-before-p11-09.dump"), filepath.Join(auto, "notes.txt"), filepath.Join(auto, "keep-me"), filepath.Join(auto, ".partial-garbage")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestRunsWithoutAReadableManifestAreFlagged(t *testing.T) {
	store := openStore(t)
	dir := filepath.Join(store.Root(), AutoDir, "20261010T011500Z")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs()
	if err != nil || len(runs) != 1 || runs[0].Err == nil {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
}

func TestHistoryKeepsTheNewestRuns(t *testing.T) {
	store := openStore(t)
	if history, err := store.History(); err != nil || history != nil {
		t.Fatalf("empty History = %v, %v", history, err)
	}
	start := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	for i := range HistoryLimit + 5 {
		at := start.Add(time.Duration(i) * time.Hour)
		if err := store.Record(Summary{ID: at.Format(RunIDLayout), Result: ResultSucceeded, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	// A record for a run already in the history replaces it.
	last := start.Add(time.Duration(HistoryLimit+4) * time.Hour)
	if err := store.Record(Summary{ID: last.Format(RunIDLayout), Result: ResultFailed, StartedAt: last}); err != nil {
		t.Fatal(err)
	}
	history, err := store.History()
	if err != nil || len(history) != HistoryLimit {
		t.Fatalf("History has %d runs, %v", len(history), err)
	}
	if history[0].ID != last.Format(RunIDLayout) || history[0].Result != ResultFailed || history[1].StartedAt.After(history[0].StartedAt) {
		t.Fatalf("History[0] = %+v", history[0])
	}
	// A damaged history is replaced by the next record.
	if err := os.WriteFile(filepath.Join(store.Root(), AutoDir, HistoryFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(); err == nil {
		t.Fatal("a damaged history read without error")
	}
	if err := store.Record(Summary{ID: "20261011T011500Z", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if history, err := store.History(); err != nil || len(history) != 1 {
		t.Fatalf("History = %v, %v", history, err)
	}
}

func TestTheLockIsExclusive(t *testing.T) {
	store := openStore(t)
	lock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	// A second holder (here another open file, as another process would
	// have) is refused.
	if _, err := store.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Lock = %v, want ErrLocked", err)
	}
	lock.Release()
	lock.Release()
	again, err := store.Lock()
	if err != nil {
		t.Fatalf("Lock after Release = %v", err)
	}
	again.Release()
}

func TestUsageAndDisk(t *testing.T) {
	store := openStore(t)
	dir := filepath.Join(store.Root(), AutoDir, "20261011T011500Z")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a.dump": 1000, ManifestFile: 24} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Files outside auto/ do not count.
	if err := os.WriteFile(filepath.Join(store.Root(), "manual.dump"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	if total, err := store.Usage(); err != nil || total != 1024 {
		t.Fatalf("Usage = %d, %v", total, err)
	}
	free, size, err := store.Disk()
	if err != nil || size <= 0 || free < 0 || free > size {
		t.Fatalf("Disk = %d, %d, %v", free, size, err)
	}
}
