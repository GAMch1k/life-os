package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"sync"
	"time"
)

var (
	// ErrNoBackup means there is no successful full run to check yet.
	ErrNoBackup = errors.New("there is no successful backup of every database to check yet")
	// ErrNothingToCheck means the run named has no successful dump.
	ErrNothingToCheck = errors.New("the run has no dump to check")
	// ErrChecksUnavailable means the runner has no Restorer.
	ErrChecksUnavailable = errors.New("restore checks are not set up")
)

// UnknownRunError names a run id that is not a completed run on disk.
type UnknownRunError struct{ ID string }

func (e *UnknownRunError) Error() string { return "no completed run " + e.ID }

// DatabaseRestorer checks one database's dump; *Restorer is the real one.
type DatabaseRestorer interface {
	Check(ctx context.Context, dir string, database DatabaseResult) DatabaseCheck
	DropLeftovers(ctx context.Context, databases []string) ([]string, error)
}

// CheckRequest asks for a restore check.
type CheckRequest struct {
	// RunID is the run to check; empty means the newest successful full
	// run.
	RunID   string
	Trigger Trigger
	Actor   string
}

// CheckProgress is a restore check in progress.
type CheckProgress struct {
	ID        string
	RunID     string
	Trigger   Trigger
	Actor     string
	StartedAt time.Time
	Databases []string
	Completed int
	Current   string
}

// CheckJob is a started restore check.
type CheckJob struct {
	done     chan struct{}
	mu       sync.Mutex
	progress CheckProgress
	result   RestoreCheck
}

// ID is the check's id.
func (j *CheckJob) ID() string { return j.progress.ID }

// Progress is how far the check is.
func (j *CheckJob) Progress() CheckProgress {
	j.mu.Lock()
	defer j.mu.Unlock()
	progress := j.progress
	progress.Databases = slices.Clone(progress.Databases)
	return progress
}

// Done is closed when the check has finished.
func (j *CheckJob) Done() <-chan struct{} { return j.done }

// Result is the finished check; valid once Done is closed.
func (j *CheckJob) Result() RestoreCheck {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result
}

func (j *CheckJob) set(update func(*CheckProgress)) {
	j.mu.Lock()
	update(&j.progress)
	j.mu.Unlock()
}

// StartRestoreCheck begins a restore check and returns without waiting for
// it. It returns ErrRunning while a run or a check is in progress,
// ErrNoBackup or *UnknownRunError when there is nothing to check.
func (r *Runner) StartRestoreCheck(request CheckRequest) (*CheckJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return nil, ErrStopped
	}
	if r.opts.Restorer == nil {
		return nil, ErrChecksUnavailable
	}
	if r.current != nil || r.checking != nil {
		return nil, ErrRunning
	}
	run, err := r.checkTarget(request.RunID)
	if err != nil {
		return nil, err
	}
	lock, err := r.opts.Store.Lock()
	if errors.Is(err, ErrLocked) {
		return nil, ErrRunning
	}
	if err != nil {
		return nil, err
	}
	job := r.newCheckJob(request, run)
	r.checking = job
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		result := r.runCheck(job, run)
		lock.Release()
		r.finishCheck(job, result)
	}()
	return job, nil
}

// checkTarget is the run a check covers: the one named, or the newest
// successful full run.
func (r *Runner) checkTarget(id string) (Run, error) {
	if id == "" {
		runs, err := r.opts.Store.Runs()
		if err != nil {
			return Run{}, err
		}
		for _, run := range runs {
			if run.Err == nil && run.Manifest.Result == ResultSucceeded && run.Manifest.Scope == ScopeAll {
				return run, nil
			}
		}
		return Run{}, ErrNoBackup
	}
	run, err := r.opts.Store.Run(id)
	if err != nil || errors.Is(run.Err, fs.ErrNotExist) {
		return Run{}, &UnknownRunError{ID: id}
	}
	if run.Err != nil {
		return Run{}, fmt.Errorf("run %s has no readable manifest: %w", id, run.Err)
	}
	if !slices.ContainsFunc(run.Manifest.Databases, func(d DatabaseResult) bool { return d.Result == ResultSucceeded }) {
		return Run{}, fmt.Errorf("run %s: %w", id, ErrNothingToCheck)
	}
	return run, nil
}

func (r *Runner) newCheckJob(request CheckRequest, run Run) *CheckJob {
	started := r.opts.Now().UTC()
	var names []string
	for _, database := range run.Manifest.Databases {
		if database.Result == ResultSucceeded {
			names = append(names, database.Name)
		}
	}
	return &CheckJob{done: make(chan struct{}), progress: CheckProgress{
		ID:        started.Truncate(time.Second).Format(RunIDLayout),
		RunID:     run.ID,
		Trigger:   request.Trigger,
		Actor:     request.Actor,
		StartedAt: started,
		Databases: names,
	}}
}

// runCheck restores each of the run's dumps, one at a time, and records
// the check. The caller holds the backup lock.
func (r *Runner) runCheck(job *CheckJob, run Run) RestoreCheck {
	ctx := r.ctx
	progress := job.Progress()
	logger := r.opts.Logger.With("restore_check", progress.ID, "run", run.ID)
	logger.Info("restore check started", "trigger", progress.Trigger, "databases", len(progress.Databases))
	check := RestoreCheck{
		ID:        progress.ID,
		RunID:     run.ID,
		Trigger:   progress.Trigger,
		Actor:     progress.Actor,
		StartedAt: progress.StartedAt,
		Databases: []DatabaseCheck{},
	}
	r.dropScratch(ctx, logger)
	dir := r.opts.Store.RunDir(run.ID)
	for _, database := range run.Manifest.Databases {
		if database.Result != ResultSucceeded {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		job.set(func(p *CheckProgress) { p.Current = database.Name })
		result := r.opts.Restorer.Check(ctx, dir, database)
		check.Databases = append(check.Databases, result)
		job.set(func(p *CheckProgress) { p.Completed++; p.Current = "" })
		if !result.OK && ctx.Err() == nil {
			logger.Warn("restore check of a database failed", "database", database.Name, "mismatches", len(result.Mismatches), "error", result.Error)
		}
	}
	check.FinishedAt = r.opts.Now().UTC()
	check.DurationSeconds = check.FinishedAt.Sub(check.StartedAt).Round(time.Millisecond).Seconds()
	failed := 0
	for _, database := range check.Databases {
		if !database.OK {
			failed++
		}
	}
	switch {
	case ctx.Err() != nil:
		check.Error = "stopped before it finished: the backup service shut down"
	case len(check.Databases) == 0:
		check.Error = "the run has no dump to check"
	case failed > 0:
		check.Error = fmt.Sprintf("%d of %d databases did not restore as their manifest says", failed, len(check.Databases))
	default:
		check.OK = true
	}
	if err := r.opts.Store.RecordCheck(check); err != nil {
		logger.Warn("recording the restore check failed", "error", err)
	}
	logger.Info("restore check finished", "ok", check.OK, "duration_seconds", check.DurationSeconds, "error", check.Error)
	return check
}

func (r *Runner) finishCheck(job *CheckJob, result RestoreCheck) {
	r.mu.Lock()
	r.checking = nil
	r.mu.Unlock()
	job.mu.Lock()
	job.result = result
	job.mu.Unlock()
	close(job.done)
}

// dropScratch drops the scratch databases interrupted checks left behind.
// The caller holds the lock.
func (r *Runner) dropScratch(ctx context.Context, logger *slog.Logger) {
	if r.opts.Restorer == nil {
		return
	}
	registry, err := r.opts.Registry()
	if err != nil || len(registry) == 0 {
		return
	}
	names := make([]string, 0, len(registry))
	for _, database := range registry {
		names = append(names, database.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dropped, err := r.opts.Restorer.DropLeftovers(ctx, names)
	for _, name := range dropped {
		logger.Warn("dropped a scratch database an interrupted restore check left behind", "database", name)
	}
	if err != nil {
		logger.Warn("dropping leftover scratch databases failed", "error", err)
	}
}

// CurrentCheck is the restore check in progress, or nil.
func (r *Runner) CurrentCheck() *CheckJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checking
}

// LastRestoreCheck is the newest restore check, or nil.
func (r *Runner) LastRestoreCheck() (*RestoreCheck, error) {
	checks, err := r.opts.Store.RestoreChecks()
	if err != nil || len(checks) == 0 {
		return nil, err
	}
	return &checks[0], nil
}
