package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Layout of the backup directory. The service keeps everything under
// auto/ and touches nothing else in the directory:
//
//	auto/<id>/                one completed run: manifest.json and <database>.dump files
//	auto/.partial-<id>/       a run in progress (renamed to <id> when complete)
//	auto/history.json         the latest runs' summaries, including runs that left no files
//	auto/restore-checks.json  the latest restore checks
//	auto/.lock                held (flock) while a run or a restore check works
const (
	AutoDir       = "auto"
	ManifestFile  = "manifest.json"
	HistoryFile   = "history.json"
	ChecksFile    = "restore-checks.json"
	LockFile      = ".lock"
	PartialPrefix = ".partial-"
	// RunIDLayout formats a run's start time in UTC as its id and
	// directory name, such as 20261011T031500Z.
	RunIDLayout = "20060102T150405Z"
	// HistoryLimit is how many run summaries history.json keeps.
	HistoryLimit = 100
	// ChecksLimit is how many restore checks restore-checks.json keeps.
	ChecksLimit = 50
)

var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)

// IsRunID says whether name is a run id, the only directory names under
// auto/ the rotation may delete.
func IsRunID(name string) bool {
	if !runIDPattern.MatchString(name) {
		return false
	}
	_, err := time.Parse(RunIDLayout, name)
	return err == nil
}

// ErrLocked means another process holds the backup lock: a run is going.
var ErrLocked = errors.New("the backup directory is locked by another run")

// Store is the backup directory.
type Store struct {
	root string
	auto string
}

// OpenStore prepares the backup directory at root, which must exist: it is
// the host directory mounted into the container, never created here.
func OpenStore(root string) (*Store, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("backup directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("backup directory %s is not a directory", root)
	}
	store := &Store{root: root, auto: filepath.Join(root, AutoDir)}
	if err := os.Mkdir(store.auto, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", store.auto, err)
	}
	return store, nil
}

// Root is the backup directory.
func (s *Store) Root() string { return s.root }

// Writable checks that runs can be written.
func (s *Store) Writable() error {
	return unix.Access(s.auto, unix.W_OK|unix.X_OK)
}

// Lock is the backup lock, held while a run writes.
type Lock struct{ file *os.File }

// Lock takes the backup lock without waiting. It returns ErrLocked when
// another process holds it. The kernel drops the lock when its holder
// exits, so a crash never leaves the directory locked.
func (s *Store) Lock() (*Lock, error) {
	file, err := os.OpenFile(filepath.Join(s.auto, LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", file.Name(), err)
	}
	return &Lock{file: file}, nil
}

// Release releases the lock.
func (l *Lock) Release() {
	if l != nil && l.file != nil {
		_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
		l.file.Close()
		l.file = nil
	}
}

// Begin creates the partial directory of a new run and writes its first
// manifest there, so the remains of an interrupted run can be recognised.
// The run id is the manifest's start time in UTC, moved on a second while
// that name is taken. Begin returns the manifest with its id, and the
// directory.
func (s *Store) Begin(manifest Manifest) (Manifest, string, error) {
	started := manifest.StartedAt.UTC().Truncate(time.Second)
	for {
		id := started.Format(RunIDLayout)
		if !s.exists(id) && !s.exists(PartialPrefix+id) {
			manifest.ID = id
			break
		}
		started = started.Add(time.Second)
	}
	dir := s.partialPath(manifest.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Manifest{}, "", err
	}
	if err := writeJSONAtomic(dir, ManifestFile, manifest); err != nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, "", err
	}
	return manifest, dir, nil
}

// Complete writes the final manifest of a run and gives its directory the
// final name. Each step is flushed to disk first, so a crash leaves either
// the partial directory (removed at the next start) or the complete run.
func (s *Store) Complete(manifest Manifest) error {
	dir := s.partialPath(manifest.ID)
	if err := writeJSONAtomic(dir, ManifestFile, manifest); err != nil {
		return err
	}
	if err := os.Rename(dir, filepath.Join(s.auto, manifest.ID)); err != nil {
		return err
	}
	return syncDir(s.auto)
}

// Discard removes a run's partial directory.
func (s *Store) Discard(id string) error {
	if !IsRunID(id) {
		return fmt.Errorf("not a run id: %q", id)
	}
	return os.RemoveAll(s.partialPath(id))
}

// Leftover is the partial directory of a run that never completed.
type Leftover struct {
	ID       string
	Manifest Manifest
	// StoppedAt is when the run last wrote a file.
	StoppedAt time.Time
}

// Leftovers lists partial directories, the remains of runs interrupted by a
// crash or a stop. Call it only while holding the lock.
func (s *Store) Leftovers() ([]Leftover, error) {
	entries, err := os.ReadDir(s.auto)
	if err != nil {
		return nil, err
	}
	var leftovers []Leftover
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry.Name(), PartialPrefix)
		if !ok || !entry.IsDir() || !IsRunID(id) {
			continue
		}
		leftover := Leftover{ID: id}
		if manifest, err := readManifest(s.partialPath(id)); err == nil {
			leftover.Manifest = manifest
		}
		if leftover.Manifest.StartedAt.IsZero() {
			leftover.Manifest.StartedAt, _ = time.Parse(RunIDLayout, id)
		}
		leftover.Manifest.ID = id
		leftover.StoppedAt = lastWrite(s.partialPath(id), leftover.Manifest.StartedAt)
		leftovers = append(leftovers, leftover)
	}
	return leftovers, nil
}

// Run is a completed run on disk.
type Run struct {
	ID       string
	Manifest Manifest
	// Err is set when the manifest cannot be read; such a run is listed but
	// never deleted.
	Err error
}

// Runs lists the completed runs, newest first.
func (s *Store) Runs() ([]Run, error) {
	entries, err := os.ReadDir(s.auto)
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, entry := range entries {
		if !entry.IsDir() || !IsRunID(entry.Name()) {
			continue
		}
		run := Run{ID: entry.Name()}
		run.Manifest, run.Err = readManifest(filepath.Join(s.auto, entry.Name()))
		if run.Err == nil && run.Manifest.ID != run.ID {
			run.Err = fmt.Errorf("manifest names run %q", run.Manifest.ID)
		}
		runs = append(runs, run)
	}
	slices.SortFunc(runs, func(a, b Run) int { return strings.Compare(b.ID, a.ID) })
	return runs, nil
}

// Delete removes a completed run. It refuses anything that is not a run
// directory under auto/.
func (s *Store) Delete(id string) error {
	if !IsRunID(id) {
		return fmt.Errorf("not a run id: %q", id)
	}
	return os.RemoveAll(filepath.Join(s.auto, id))
}

// Usage is the size of every file under auto/.
func (s *Store) Usage() (int64, error) {
	var total int64
	err := filepath.WalkDir(s.auto, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// Disk is the space available to the service and the size of the file
// system the backup directory is on.
func (s *Store) Disk() (free, size int64, err error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(s.root, &stat); err != nil {
		return 0, 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), int64(stat.Blocks) * int64(stat.Bsize), nil
}

type history struct {
	Runs []Summary `json:"runs"`
}

// History is the latest runs' summaries, newest first.
func (s *Store) History() ([]Summary, error) {
	data, err := os.ReadFile(filepath.Join(s.auto, HistoryFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h history
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("read %s: %w", HistoryFile, err)
	}
	return h.Runs, nil
}

// Record adds a run's summary to the history, keeping the newest
// HistoryLimit. Call it only while holding the lock.
func (s *Store) Record(summary Summary) error {
	runs, err := s.History()
	if err != nil {
		// A damaged history is replaced rather than blocking every run.
		runs = nil
	}
	runs = slices.DeleteFunc(runs, func(run Summary) bool { return run.ID == summary.ID })
	runs = append([]Summary{summary}, runs...)
	slices.SortStableFunc(runs, func(a, b Summary) int { return b.StartedAt.Compare(a.StartedAt) })
	if len(runs) > HistoryLimit {
		runs = runs[:HistoryLimit]
	}
	return writeJSONAtomic(s.auto, HistoryFile, history{Runs: runs})
}

type checkHistory struct {
	Checks []RestoreCheck `json:"checks"`
}

// RestoreChecks are the latest restore checks, newest first.
func (s *Store) RestoreChecks() ([]RestoreCheck, error) {
	data, err := os.ReadFile(filepath.Join(s.auto, ChecksFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h checkHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("read %s: %w", ChecksFile, err)
	}
	return h.Checks, nil
}

// RecordCheck adds a restore check to its history, keeping the newest
// ChecksLimit. Call it only while holding the lock.
func (s *Store) RecordCheck(check RestoreCheck) error {
	checks, err := s.RestoreChecks()
	if err != nil {
		checks = nil
	}
	checks = slices.DeleteFunc(checks, func(c RestoreCheck) bool { return c.ID == check.ID })
	checks = append([]RestoreCheck{check}, checks...)
	slices.SortStableFunc(checks, func(a, b RestoreCheck) int { return b.StartedAt.Compare(a.StartedAt) })
	if len(checks) > ChecksLimit {
		checks = checks[:ChecksLimit]
	}
	return writeJSONAtomic(s.auto, ChecksFile, checkHistory{Checks: checks})
}

// Run reads one completed run.
func (s *Store) Run(id string) (Run, error) {
	if !IsRunID(id) {
		return Run{}, fmt.Errorf("not a run id: %q", id)
	}
	run := Run{ID: id}
	run.Manifest, run.Err = readManifest(filepath.Join(s.auto, id))
	if run.Err == nil && run.Manifest.ID != id {
		run.Err = fmt.Errorf("manifest names run %q", run.Manifest.ID)
	}
	return run, nil
}

// RunDir is the directory of a completed run.
func (s *Store) RunDir(id string) string { return filepath.Join(s.auto, id) }

func (s *Store) partialPath(id string) string {
	return filepath.Join(s.auto, PartialPrefix+id)
}

func (s *Store) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(s.auto, name))
	return err == nil
}

// lastWrite is the latest modification time of dir and the files in it,
// and at least since.
func lastWrite(dir string, since time.Time) time.Time {
	latest := since
	if info, err := os.Stat(dir); err == nil && info.ModTime().After(latest) {
		latest = info.ModTime()
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest.UTC()
}

func readManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", ManifestFile, err)
	}
	return manifest, nil
}

// writeJSONAtomic replaces dir/name with value as JSON: written to a
// temporary file, flushed, renamed over the old file, and the directory
// flushed, so readers see the old or the new file, never a part.
func writeJSONAtomic(dir, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
