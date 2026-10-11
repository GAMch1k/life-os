package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrRunning means a run is already in progress, in this process or in
	// another one holding the backup lock.
	ErrRunning = errors.New("a backup is already running")
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

	mu      sync.Mutex
	current *Job
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
// or stop interrupted, removes their files, and applies the rotation. It
// does nothing while another process holds the backup lock.
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
	if r.current != nil {
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
	go r.execute(job, manifest, dir, databases, lock)
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

func (r *Runner) execute(job *Job, manifest Manifest, dir string, databases []Database, lock *Lock) {
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
	}
	r.record(summary)
	if ctx.Err() == nil {
		r.rotate()
	}
	logger.Info("backup finished", "result", summary.Result, "duration_seconds", summary.DurationSeconds, "bytes", summary.TotalBytes, "error", summary.Error)

	lock.Release()
	r.mu.Lock()
	r.current = nil
	r.mu.Unlock()
	job.finish(summary)
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
	for _, id := range r.opts.Retention.Expired(infos, r.opts.Now(), r.opts.Location) {
		if err := r.opts.Store.Delete(id); err != nil {
			r.opts.Logger.Warn("deleting an expired backup run failed", "run", id, "error", err)
			continue
		}
		r.opts.Logger.Info("deleted an expired backup run", "run", id)
	}
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
	// LastSuccess is the latest successful run of every database.
	LastSuccess *Summary
	Running     *Progress
	Runs        int
	TotalBytes  int64
	FreeBytes   int64
	SizeBytes   int64
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
			status.LastSuccess = &run
			break
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
	if status.TotalBytes, err = r.opts.Store.Usage(); err != nil {
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
