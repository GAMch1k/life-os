package backup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var mediaRegistry = []Database{{"finance", "finance_db"}, {"inbox", "inbox_db"}}

func newMediaRunner(t *testing.T, dumper *fakeDumper, source string, now *time.Time) (*Runner, *Store) {
	t.Helper()
	return newMediaRunnerKeeping(t, dumper, source, now, Retention{Daily: 1, FailedDays: 1})
}

func newMediaRunnerKeeping(t *testing.T, dumper *fakeDumper, source string, now *time.Time, retention Retention) (*Runner, *Store) {
	t.Helper()
	store := openStore(t)
	opts := RunnerOptions{
		Store:       store,
		Dumper:      dumper,
		Restorer:    &fakeRestorer{},
		Registry:    func() ([]Database, error) { return mediaRegistry, nil },
		Retention:   retention,
		MediaSource: source,
	}
	if now != nil {
		opts.Now = func() time.Time { return *now }
	}
	runner := NewRunner(opts)
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner, store
}

func TestARunStoresTheMediaItsSnapshotRefersTo(t *testing.T) {
	source := t.TempDir()
	letter, receipt := mediaFile(t, source, "letter"), mediaFile(t, source, "receipt")
	gone := MediaRef{SHA256: hashOf("pruned"), Bytes: 6}
	dumper := &fakeDumper{media: map[string][]MediaRef{"inbox_db": {letter, receipt, gone}}}
	runner, store := newMediaRunner(t, dumper, source, nil)

	summary := backupNow(t, runner, Request{Trigger: TriggerSchedule})
	// A file missing on the volume is a warning, not a failure.
	if summary.Result != ResultSucceeded || summary.Media == nil || summary.Media.Count != 2 || summary.Media.Copied != 2 || summary.Media.Missing != 1 || summary.Media.Bytes != letter.Bytes+receipt.Bytes {
		t.Fatalf("summary = %+v, media %+v", summary, summary.Media)
	}
	runs, _ := store.Runs()
	media := runs[0].Manifest.Media
	if media.Database != "inbox_db" || media.List != "media-inbox.json" || len(media.Missing) != 1 || media.Missing[0].SHA256 != gone.SHA256 {
		t.Fatalf("manifest media = %+v", media)
	}
	list, err := readMediaList(store.RunDir(summary.ID), media.List)
	if err != nil || !slices.Equal(list, []MediaRef{letter, receipt}) {
		t.Fatalf("list = %+v, %v", list, err)
	}
	status, err := runner.Status()
	if err != nil || status.MediaFiles != 2 || status.MediaBytes != letter.Bytes+receipt.Bytes {
		t.Fatalf("status media = %d, %d, %v", status.MediaFiles, status.MediaBytes, err)
	}

	// A run of only finance stores no media.
	if subset := backupNow(t, runner, Request{Trigger: TriggerManual, Databases: []string{"finance"}}); subset.Media != nil {
		t.Fatalf("a run without inbox has media: %+v", subset.Media)
	}
	// The restore check checks the stored media too.
	job, err := runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	check := waitCheck(t, job)
	if !check.OK || check.Media == nil || !check.Media.OK || check.Media.Count != 2 || check.Media.Hashed != 2 {
		t.Fatalf("check = %+v, media %+v", check, check.Media)
	}
	if err := os.Remove(store.Media().Path(letter.SHA256)); err != nil {
		t.Fatal(err)
	}
	job, _ = runner.StartRestoreCheck(CheckRequest{Trigger: TriggerManual})
	check = waitCheck(t, job)
	if check.OK || check.Error != "1 of 2 media files are missing or damaged in the store" {
		t.Fatalf("a check with a missing media file = %+v", check)
	}
}

func TestRotationKeepsTheMediaKeptRunsReferTo(t *testing.T) {
	source := t.TempDir()
	shared, old := mediaFile(t, source, "on every night"), mediaFile(t, source, "deleted by inbox since")
	now := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	dumper := &fakeDumper{media: map[string][]MediaRef{"inbox_db": {old, shared}}}
	runner, store := newMediaRunner(t, dumper, source, &now)
	first := backupNow(t, runner, Request{Trigger: TriggerSchedule})

	// The next night inbox no longer refers to the old photo; with one day
	// kept, the first run expires and the photo only it listed goes.
	now = now.Add(24 * time.Hour)
	dumper.mu.Lock()
	dumper.media["inbox_db"] = []MediaRef{shared}
	dumper.mu.Unlock()
	second := backupNow(t, runner, Request{Trigger: TriggerSchedule})
	if second.Media.Copied != 0 {
		t.Fatalf("the shared photo was copied again: %+v", second.Media)
	}
	runs, _ := store.Runs()
	if len(runs) != 1 || runs[0].ID != second.ID {
		t.Fatalf("runs = %+v (the first, %s, should have expired)", runs, first.ID)
	}
	if _, err := os.Stat(store.Media().Path(shared.SHA256)); err != nil {
		t.Fatalf("a kept run's photo is gone: %v", err)
	}
	if _, err := os.Stat(store.Media().Path(old.SHA256)); !os.IsNotExist(err) {
		t.Fatalf("a photo no kept run lists survived: %v", err)
	}
}

func TestMediaAreNeverCollectedWhileARunListCannotBeRead(t *testing.T) {
	source := t.TempDir()
	photo := mediaFile(t, source, "photo")
	now := time.Date(2026, 10, 11, 1, 15, 0, 0, time.UTC)
	dumper := &fakeDumper{media: map[string][]MediaRef{"inbox_db": {photo}}}
	runner, store := newMediaRunnerKeeping(t, dumper, source, &now, Retention{Daily: 7, FailedDays: 3})
	first := backupNow(t, runner, Request{Trigger: TriggerSchedule})
	if err := os.Remove(filepath.Join(store.RunDir(first.ID), "media-inbox.json")); err != nil {
		t.Fatal(err)
	}
	// An unlisted photo in the store, which collecting would delete.
	stray := mediaFile(t, source, "stray")
	if _, _, _, _, err := store.Media().Copy(context.Background(), source, []MediaRef{stray}); err != nil {
		t.Fatal(err)
	}
	dumper.mu.Lock()
	dumper.media["inbox_db"] = nil
	dumper.mu.Unlock()
	now = now.Add(24 * time.Hour)
	backupNow(t, runner, Request{Trigger: TriggerSchedule})
	if runs, _ := store.Runs(); len(runs) != 2 {
		t.Fatalf("both runs should be kept, not %d", len(runs))
	}
	if _, err := os.Stat(store.Media().Path(stray.SHA256)); err != nil {
		t.Fatalf("media were collected although a run's list is unreadable: %v", err)
	}
}

func TestAMediaVolumeThatCannotBeReadFailsTheRun(t *testing.T) {
	dumper := &fakeDumper{media: map[string][]MediaRef{"inbox_db": {{SHA256: hashOf("x"), Bytes: 1}}}}
	runner, _ := newMediaRunner(t, dumper, filepath.Join(t.TempDir(), "not-mounted"), nil)
	summary := backupNow(t, runner, Request{Trigger: TriggerSchedule})
	if summary.Result != ResultFailed || summary.Media == nil || summary.Media.Error == "" || !strings.HasPrefix(summary.Error, "the media could not be backed up: ") {
		t.Fatalf("summary = %+v, media %+v", summary, summary.Media)
	}
	// So does an inbox_db that failed: its media were never listed.
	failing := &fakeDumper{fail: map[string]bool{"inbox_db": true}}
	runner, _ = newMediaRunner(t, failing, t.TempDir(), nil)
	summary = backupNow(t, runner, Request{Trigger: TriggerSchedule})
	if summary.Result != ResultFailed || summary.Media == nil || summary.Media.Error != "inbox_db was not backed up, so its media were not listed" {
		t.Fatalf("summary = %+v, media %+v", summary, summary.Media)
	}
}
