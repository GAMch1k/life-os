package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrRunning means a run or a restore check is already in progress, in
	// this process or in another one holding the backup lock.
	ErrRunning = errors.New("a backup or restore check is already running")
	// ErrStopped means the service is shutting down.
	ErrStopped = errors.New("the backup service is stopping")
)

// UnknownDatabasesError names databases a request asked for that are not in
// the registry.
type UnknownDatabasesError struct{ Names []string }

func (e *UnknownDatabasesError) Error() string {
	return "not in the database registry: " + strings.Join(e.Names, ", ")
}

// Request asks for a run.
type Request struct {
	Trigger Trigger
	// Databases are service or database names; empty means every database.
	Databases []string
	// Actor is who asked for a manual run, such as dev:backup-now.
	Actor string
	// ThenRestoreCheck runs a restore check of the newest successful full
	// run right after this run, under the same lock.
	ThenRestoreCheck bool
}

// Progress is a run in progress.
type Progress struct {
	ID        string
	Trigger   Trigger
	Scope     Scope
	Actor     string
	StartedAt time.Time
	Databases []string
	Completed int
	Current   string
}

// Job is a started run.
type Job struct {
	done     chan struct{}
	mu       sync.Mutex
	progress Progress
	summary  Summary
}

// ID is the run id.
func (j *Job) ID() string { return j.progress.ID }

// Progress is how far the run is.
func (j *Job) Progress() Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	progress := j.progress
	progress.Databases = slices.Clone(progress.Databases)
	return progress
}

// Done is closed when the run has finished.
func (j *Job) Done() <-chan struct{} { return j.done }

// Summary is the finished run; valid once Done is closed.
func (j *Job) Summary() Summary {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.summary
}

func (j *Job) dumping(database string) {
	j.mu.Lock()
	j.progress.Current = database
	j.mu.Unlock()
}

func (j *Job) dumped() {
	j.mu.Lock()
	j.progress.Completed++
	j.progress.Current = ""
	j.mu.Unlock()
}

func (j *Job) finish(summary Summary) {
	j.mu.Lock()
	j.summary = summary
	j.mu.Unlock()
	close(j.done)
}

// DatabaseDumper backs up one database; *Dumper is the real one.
type DatabaseDumper interface {
	Version(ctx context.Context) (string, error)
	Dump(ctx context.Context, database Database, dir string) (DatabaseResult, string)
}

// RunnerOptions configure a Runner.
type RunnerOptions struct {
	Store  *Store
	Dumper DatabaseDumper
	// Registry reads the database registry. It is read at every run, so a
	// database added to the registry is backed up from the next run on.
	Registry  func() ([]Database, error)
	Retention Retention
	// Targets receive a copy of every completed run (none yet).
	Targets []Target
	// Restorer runs restore checks; without one there are none.
	Restorer DatabaseRestorer
	// MediaSource is the directory of Inbox's capture images (the
	// inbox-media volume, read-only); empty means media are not backed up.
	MediaSource string
	// MediaHashBudget bounds how many media bytes a restore check hashes
	// (DefaultMediaHashBudget when zero); the rest are checked for size.
	MediaHashBudget int64
	// Location is the owner's timezone, for the rotation's days, weeks,
	// and months.
	Location *time.Location
	Logger   *slog.Logger
	Now      func() time.Time
}

// Runner runs backups, one at a time, and applies the rotation after each.
type Runner struct {
	opts   RunnerOptions
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	current  *Job
	checking *CheckJob
}

// NewRunner creates a Runner.
func NewRunner(opts RunnerOptions) *Runner {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{opts: opts, ctx: ctx, cancel: cancel}
}

// Recover cleans up after an earlier process: it records the runs a crash
// or stop interrupted, removes their files, drops the scratch databases of
// interrupted restore checks, and applies the rotation. It does nothing
// while another process holds the backup lock.
func (r *Runner) Recover() error {
	lock, err := r.opts.Store.Lock()
	if errors.Is(err, ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	r.recoverLeftovers()
	r.dropScratch(r.ctx, r.opts.Logger)
	r.rotate()
	return nil
}

// Start begins a run and returns without waiting for it. It returns
// ErrRunning while a run is in progress and *UnknownDatabasesError for a
// name that is not in the registry; neither is recorded. Any other error
// means the run could not start; it is recorded as a failed run.
func (r *Runner) Start(request Request) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return nil, ErrStopped
	}
	if r.current != nil || r.checking != nil {
		return nil, ErrRunning
	}
	registry, err := r.opts.Registry()
	if err != nil {
		return nil, r.failToStart(request, nil, fmt.Errorf("read the database registry: %w", err))
	}
	databases, unknown := Select(registry, request.Databases)
	if len(unknown) > 0 {
		return nil, &UnknownDatabasesError{Names: unknown}
	}
	if len(databases) == 0 {
		return nil, r.failToStart(request, nil, errors.New("the database registry lists no databases"))
	}
	scope := ScopeAll
	if len(databases) < len(registry) {
		scope = ScopeSubset
	}
	lock, err := r.opts.Store.Lock()
	if errors.Is(err, ErrLocked) {
		return nil, ErrRunning
	}
	if err != nil {
		return nil, r.failToStart(request, nil, err)
	}
	r.recoverLeftovers()
	manifest, dir, err := r.opts.Store.Begin(Manifest{
		FormatVersion: ManifestVersion,
		Trigger:       request.Trigger,
		Scope:         scope,
		Actor:         request.Actor,
		StartedAt:     r.opts.Now().UTC(),
		Databases:     []DatabaseResult{},
	})
	if err != nil {
		defer lock.Release()
		return nil, r.failToStart(request, lock, fmt.Errorf("create the run directory: %w", err))
	}
	names := make([]string, 0, len(databases))
	for _, database := range databases {
		names = append(names, database.Name)
	}
	job := &Job{done: make(chan struct{}), progress: Progress{
		ID:        manifest.ID,
		Trigger:   manifest.Trigger,
		Scope:     manifest.Scope,
		Actor:     manifest.Actor,
		StartedAt: manifest.StartedAt,
		Databases: names,
	}}
	r.current = job
	r.wg.Add(1)
	go r.execute(job, manifest, dir, databases, lock, request.ThenRestoreCheck)
	return job, nil
}

// Current is the run in progress, or nil.
func (r *Runner) Current() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// Close stops a run in progress (it is recorded as interrupted and its
// files are removed) and waits for it, at most until ctx ends.
func (r *Runner) Close(ctx context.Context) error {
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("the backup run did not stop: %w", ctx.Err())
	}
}

func (r *Runner) execute(job *Job, manifest Manifest, dir string, databases []Database, lock *Lock, thenCheck bool) {
	defer r.wg.Done()
	defer lock.Release()
	ctx := r.ctx
	logger := r.opts.Logger.With("run", manifest.ID)
	logger.Info("backup started", "trigger", manifest.Trigger, "scope", manifest.Scope, "databases", len(databases))

	if version, err := r.opts.Dumper.Version(ctx); err != nil {
		logger.Warn("reading the pg_dump version failed", "error", err)
	} else {
		manifest.PgDumpVersion = version
	}
	for _, database := range databases {
		if ctx.Err() != nil {
			break
		}
		job.dumping(database.Name)
		result, serverVersion := r.opts.Dumper.Dump(ctx, database, dir)
		if manifest.ServerVersion == "" {
			manifest.ServerVersion = serverVersion
		}
		manifest.Databases = append(manifest.Databases, result)
		job.dumped()
		if result.Result != ResultSucceeded && ctx.Err() == nil {
			logger.Warn("database backup failed", "database", database.Name, "error", result.Error)
		}
	}
	if r.opts.MediaSource != "" && ctx.Err() == nil {
		manifest.Media = r.storeMedia(ctx, manifest.Databases, dir, logger)
	}
	manifest.finish(r.opts.Now().UTC())

	kept := true
	switch {
	case ctx.Err() != nil:
		manifest.Result = ResultInterrupted
		manifest.Error = "stopped before it finished: the backup service shut down; its files were removed"
		kept = false
	default:
		if err := r.opts.Store.Complete(manifest); err != nil {
			manifest.Result = ResultFailed
			manifest.Error = "could not complete the run directory: " + err.Error()
			kept = false
		}
	}
	summary := manifest.Summary()
	if !kept {
		if err := r.opts.Store.Discard(manifest.ID); err != nil {
			logger.Warn("removing the run's files failed", "error", err)
		}
		withoutFiles(&summary)
	} else {
		summary.Copies = r.copy(ctx, manifest, logger)
	}
	r.record(summary)
	if ctx.Err() == nil {
		r.rotate()
	}
	logger.Info("backup finished", "result", summary.Result, "duration_seconds", summary.DurationSeconds, "bytes", summary.TotalBytes, "error", summary.Error)

	// A restore check that follows goes on under the same lock, so no run
	// starts between the two.
	var check *CheckJob
	var checked Run
	if thenCheck && ctx.Err() == nil && r.opts.Restorer != nil {
		run, err := r.checkTarget("")
		if err != nil {
			logger.Warn("no restore check after this run", "error", err)
		} else {
			check, checked = r.newCheckJob(CheckRequest{Trigger: TriggerSchedule}, run), run
		}
	}
	if check == nil {
		lock.Release()
	}
	r.mu.Lock()
	r.current = nil
	r.checking = check
	r.mu.Unlock()
	job.finish(summary)
	if check != nil {
		result := r.runCheck(check, checked)
		lock.Release()
		r.finishCheck(check, result)
	}
}

// storeMedia copies the media files the run's inbox_db snapshot refers to
// into the shared store, and lists them in the run directory. A run that
// did not cover Inbox stores none.
func (r *Runner) storeMedia(ctx context.Context, databases []DatabaseResult, dir string, logger *slog.Logger) *MediaSet {
	index := slices.IndexFunc(databases, func(d DatabaseResult) bool { return d.Service == MediaService })
	if index < 0 {
		return nil
	}
	database := databases[index]
	set := &MediaSet{Service: MediaService, Database: database.Name, Missing: []MediaProblem{}}
	if database.Result != ResultSucceeded {
		set.Error = database.Name + " was not backed up, so its media were not listed"
		return set
	}
	stored, copied, copiedBytes, problems, err := r.opts.Store.Media().Copy(ctx, r.opts.MediaSource, database.Media)
	if err != nil {
		set.Error = err.Error()
		logger.Warn("backing up the media failed", "error", err)
		return set
	}
	name := mediaListFile(MediaService)
	if err := writeMediaList(dir, name, stored); err != nil {
		set.Error = "writing the media list failed: " + err.Error()
		return set
	}
	set.List, set.Count, set.Copied, set.CopiedBytes = name, len(stored), copied, copiedBytes
	for _, ref := range stored {
		set.Bytes += ref.Bytes
	}
	if problems != nil {
		set.Missing = problems
	}
	for _, problem := range problems {
		logger.Warn("a media file was not backed up", "sha256", problem.SHA256, "problem", problem.Problem)
	}
	logger.Info("media backed up", "files", set.Count, "copied", copied, "missing", len(problems))
	return set
}

// collectMedia deletes the stored media files no kept run lists. It does
// nothing when any run's list cannot be read: a file it might need is
// never deleted. The caller holds the lock.
func (r *Runner) collectMedia(runs []Run) {
	keep := map[string]bool{}
	for _, run := range runs {
		if run.Err != nil {
			r.opts.Logger.Warn("a backup run has no readable manifest; media are not collected", "run", run.ID)
			return
		}
		media := run.Manifest.Media
		if media == nil || media.List == "" {
			continue
		}
		refs, err := readMediaList(r.opts.Store.RunDir(run.ID), media.List)
		if err != nil {
			r.opts.Logger.Warn("a run's media list cannot be read; media are not collected", "run", run.ID, "error", err)
			return
		}
		for _, ref := range refs {
			keep[ref.SHA256] = true
		}
	}
	removed, err := r.opts.Store.Media().Collect(keep)
	if removed > 0 {
		r.opts.Logger.Info("deleted media files no kept run refers to", "files", removed)
	}
	if err != nil {
		r.opts.Logger.Warn("collecting media files failed", "error", err)
	}
}

// failToStart records a run that could not start. lock is the backup lock
// when the caller holds it; otherwise it is taken for the record, which is
// skipped when another process holds it.
func (r *Runner) failToStart(request Request, lock *Lock, cause error) error {
	now := r.opts.Now().UTC()
	summary := Summary{
		ID:         now.Truncate(time.Second).Format(RunIDLayout),
		Trigger:    request.Trigger,
		Scope:      ScopeAll,
		Result:     ResultFailed,
		Actor:      request.Actor,
		StartedAt:  now,
		FinishedAt: now,
		Databases:  []DatabaseSummary{},
		Error:      cause.Error(),
	}
	if len(request.Databases) > 0 {
		summary.Scope = ScopeSubset
	}
	r.opts.Logger.Error("backup could not start", "trigger", request.Trigger, "error", cause)
	if lock == nil {
		held, err := r.opts.Store.Lock()
		if err != nil {
			return cause
		}
		defer held.Release()
	}
	r.record(summary)
	return cause
}

// recoverLeftovers records and removes the partial directories of runs
// that never completed. The caller holds the lock.
func (r *Runner) recoverLeftovers() {
	leftovers, err := r.opts.Store.Leftovers()
	if err != nil {
		r.opts.Logger.Warn("listing interrupted backup runs failed", "error", err)
		return
	}
	for _, leftover := range leftovers {
		manifest := leftover.Manifest
		if manifest.Trigger == "" {
			manifest.Trigger = TriggerSchedule
		}
		if manifest.Scope == "" {
			manifest.Scope = ScopeAll
		}
		summary := manifest.Summary()
		summary.Result = ResultInterrupted
		summary.FinishedAt = leftover.StoppedAt
		summary.DurationSeconds = max(0, leftover.StoppedAt.Sub(manifest.StartedAt).Round(time.Millisecond).Seconds())
		summary.Error = "the backup service stopped during this run; its files were removed"
		withoutFiles(&summary)
		if err := r.opts.Store.Discard(leftover.ID); err != nil {
			r.opts.Logger.Warn("removing an interrupted backup run failed", "run", leftover.ID, "error", err)
			continue
		}
		r.opts.Logger.Warn("removed an interrupted backup run", "run", leftover.ID)
		r.record(summary)
	}
}

// rotate deletes the runs the retention policy no longer keeps. Runs whose
// manifest cannot be read are never deleted. The caller holds the lock.
func (r *Runner) rotate() {
	runs, err := r.opts.Store.Runs()
	if err != nil {
		r.opts.Logger.Warn("listing backup runs for the rotation failed", "error", err)
		return
	}
	infos := make([]RunInfo, 0, len(runs))
	for _, run := range runs {
		if run.Err != nil {
			r.opts.Logger.Warn("a backup run has no readable manifest; the rotation leaves it alone", "run", run.ID, "error", run.Err)
			continue
		}
		infos = append(infos, RunInfo{ID: run.ID, StartedAt: run.Manifest.StartedAt, Result: run.Manifest.Result, Scope: run.Manifest.Scope})
	}
	defer func() {
		if remaining, err := r.opts.Store.Runs(); err == nil {
			r.collectMedia(remaining)
		}
	}()
	for _, id := range r.opts.Retention.Expired(infos, r.opts.Now(), r.opts.Location) {
		if err := r.opts.Store.Delete(id); err != nil {
			r.opts.Logger.Warn("deleting an expired backup run failed", "run", id, "error", err)
			continue
		}
		r.opts.Logger.Info("deleted an expired backup run", "run", id)
		for _, target := range r.opts.Targets {
			if err := target.Delete(r.ctx, id); err != nil {
				r.opts.Logger.Warn("deleting an expired backup run from a target failed", "run", id, "target", target.Name(), "error", err)
			}
		}
	}
}

// copy puts a completed run on every target.
func (r *Runner) copy(ctx context.Context, manifest Manifest, logger *slog.Logger) []Copy {
	var copies []Copy
	dir := filepath.Join(r.opts.Store.Root(), AutoDir, manifest.ID)
	for _, target := range r.opts.Targets {
		result := Copy{Target: target.Name(), Result: ResultSucceeded}
		if err := target.Put(ctx, manifest, dir); err != nil {
			result.Result, result.Error = ResultFailed, err.Error()
			logger.Warn("copying the backup run to a target failed", "target", target.Name(), "error", err)
		}
		copies = append(copies, result)
	}
	return copies
}

func (r *Runner) record(summary Summary) {
	if err := r.opts.Store.Record(summary); err != nil {
		r.opts.Logger.Warn("recording the backup run failed", "run", summary.ID, "error", err)
	}
}

// withoutFiles clears what a summary says about files that were removed.
func withoutFiles(summary *Summary) {
	summary.TotalBytes = 0
	for i := range summary.Databases {
		summary.Databases[i].File = ""
		summary.Databases[i].Bytes = 0
		summary.Databases[i].SHA256 = ""
	}
}

// Runs are the completed runs on disk, newest first. Runs without a
// readable manifest are left out.
func (r *Runner) Runs() ([]Summary, error) {
	runs, err := r.opts.Store.Runs()
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(runs))
	for _, run := range runs {
		if run.Err == nil {
			summaries = append(summaries, run.Manifest.Summary())
		}
	}
	return summaries, nil
}

// LastFullSuccess is the newest successful run of every database, or nil.
func (r *Runner) LastFullSuccess() (*Summary, error) {
	runs, err := r.Runs()
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.Result == ResultSucceeded && run.Scope == ScopeAll {
			return &run, nil
		}
	}
	return nil, nil
}

// Status is the state of the backups.
type Status struct {
	// LastRun is the latest run, including runs that left no files.
	LastRun *Summary
	// LastSuccess is the latest successful run of every database, and
	// OldestSuccess the start of the oldest one kept (zero without one).
	LastSuccess   *Summary
	OldestSuccess time.Time
	Running       *Progress
	// LastRestoreCheck is the newest restore check; RunningCheck the one in
	// progress.
	LastRestoreCheck *RestoreCheck
	RunningCheck     *CheckProgress
	Runs             int
	TotalBytes       int64
	// MediaFiles and MediaBytes are the stored media files.
	MediaFiles int
	MediaBytes int64
	FreeBytes  int64
	SizeBytes  int64
	// Databases are the databases in the registry now, or DatabasesError
	// says why it cannot be read.
	Databases      []string
	DatabasesError string
}

// Status reads the state of the backups from the backup directory.
func (r *Runner) Status() (Status, error) {
	var status Status
	runs, err := r.Runs()
	if err != nil {
		return Status{}, err
	}
	status.Runs = len(runs)
	for _, run := range runs {
		if run.Result == ResultSucceeded && run.Scope == ScopeAll {
			if status.LastSuccess == nil {
				status.LastSuccess = &run
			}
			status.OldestSuccess = run.StartedAt
		}
	}
	if len(runs) > 0 {
		status.LastRun = &runs[0]
	}
	history, err := r.opts.Store.History()
	if err != nil {
		r.opts.Logger.Warn("reading the backup history failed", "error", err)
	}
	if len(history) > 0 && (status.LastRun == nil || history[0].StartedAt.After(status.LastRun.StartedAt) || history[0].ID == status.LastRun.ID) {
		status.LastRun = &history[0]
	}
	if job := r.Current(); job != nil {
		progress := job.Progress()
		status.Running = &progress
	}
	if job := r.CurrentCheck(); job != nil {
		progress := job.Progress()
		status.RunningCheck = &progress
	}
	if status.LastRestoreCheck, err = r.LastRestoreCheck(); err != nil {
		r.opts.Logger.Warn("reading the restore checks failed", "error", err)
	}
	if status.TotalBytes, err = r.opts.Store.Usage(); err != nil {
		return Status{}, err
	}
	if status.MediaFiles, status.MediaBytes, err = r.opts.Store.Media().Usage(); err != nil {
		return Status{}, err
	}
	if status.FreeBytes, status.SizeBytes, err = r.opts.Store.Disk(); err != nil {
		return Status{}, err
	}
	registry, err := r.opts.Registry()
	if err != nil {
		status.DatabasesError = err.Error()
	}
	status.Databases = make([]string, 0, len(registry))
	for _, database := range registry {
		status.Databases = append(status.Databases, database.Name)
	}
	return status, nil
}
