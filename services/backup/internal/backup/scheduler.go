package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// starter is the part of Runner the scheduler uses.
type starter interface {
	Start(Request) (*Job, error)
	LastFullSuccess() (*Summary, error)
}

// SchedulerOptions configure a Scheduler.
type SchedulerOptions struct {
	Runner   starter
	Schedule Schedule
	// CatchUpDelay is how long a catch-up run waits after the service
	// starts, so a deployment can settle first.
	CatchUpDelay time.Duration
	// Tick is the longest the loop sleeps. A due run that cannot start
	// because another run is in progress is tried again every Tick.
	Tick   time.Duration
	Logger *slog.Logger
	Now    func() time.Time
}

// Scheduler starts the nightly run, and at start a catch-up run when the
// last nightly run was missed.
type Scheduler struct {
	opts SchedulerOptions
	// wait sleeps for d; false when ctx ends first. Tests replace it.
	wait func(ctx context.Context, d time.Duration) bool

	mu          sync.Mutex
	next        time.Time
	nextTrigger Trigger
	beat        time.Time
}

// NewScheduler creates a Scheduler.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Tick <= 0 {
		opts.Tick = time.Minute
	}
	now := opts.Now()
	return &Scheduler{
		opts:        opts,
		wait:        sleep,
		next:        opts.Schedule.Next(now),
		nextTrigger: TriggerSchedule,
		beat:        now,
	}
}

// Next is when the next run is due and why.
func (s *Scheduler) Next() (time.Time, Trigger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next, s.nextTrigger
}

// Ready fails when the loop has not run for three ticks and a minute.
func (s *Scheduler) Ready(context.Context) error {
	s.mu.Lock()
	beat := s.beat
	s.mu.Unlock()
	if since := s.opts.Now().Sub(beat); since > 3*s.opts.Tick+time.Minute {
		return fmt.Errorf("the backup schedule has not been checked for %s", since.Round(time.Second))
	}
	return nil
}

// Run checks the schedule until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	now := s.opts.Now()
	var catchUpAt time.Time
	if s.catchUpDue(now) {
		catchUpAt = now.Add(s.opts.CatchUpDelay)
		s.opts.Logger.Info("no backup since the last scheduled time: a catch-up run is due", "at", catchUpAt.UTC())
	}
	next := s.opts.Schedule.Next(now)
	for {
		now = s.opts.Now()
		retry := false
		switch {
		case !now.Before(next):
			started, stop := s.start(TriggerSchedule)
			if stop {
				return
			}
			if started {
				// A scheduled run also stands in for a catch-up.
				next, catchUpAt = s.opts.Schedule.Next(now), time.Time{}
			}
			retry = !started
		case !catchUpAt.IsZero() && !now.Before(catchUpAt):
			if !s.catchUpDue(now) {
				catchUpAt = time.Time{}
				break
			}
			started, stop := s.start(TriggerCatchUp)
			if stop {
				return
			}
			if started {
				catchUpAt = time.Time{}
			}
			retry = !started
		}
		due, trigger := next, TriggerSchedule
		if !catchUpAt.IsZero() && catchUpAt.Before(next) {
			due, trigger = catchUpAt, TriggerCatchUp
		}
		s.publish(due, trigger, now)
		wait := s.opts.Tick
		if until := due.Sub(now); !retry && until < wait {
			wait = max(until, 0)
		}
		if !s.wait(ctx, wait) {
			return
		}
	}
}

// start starts a run. started is false when another run is in progress (try
// again later); stop is true when the service is shutting down. A run that
// fails to start for another reason counts as started: it is recorded.
func (s *Scheduler) start(trigger Trigger) (started, stop bool) {
	_, err := s.opts.Runner.Start(Request{Trigger: trigger})
	switch {
	case errors.Is(err, ErrRunning):
		s.opts.Logger.Info("a backup is in progress: the due run starts after it", "trigger", trigger)
		return false, false
	case errors.Is(err, ErrStopped):
		return false, true
	}
	return true, false
}

func (s *Scheduler) catchUpDue(now time.Time) bool {
	last, err := s.opts.Runner.LastFullSuccess()
	if err != nil {
		s.opts.Logger.Warn("reading the backup runs failed", "error", err)
		return false
	}
	var startedAt time.Time
	if last != nil {
		startedAt = last.StartedAt
	}
	return s.opts.Schedule.CatchUpDue(startedAt, now)
}

func (s *Scheduler) publish(next time.Time, trigger Trigger, now time.Time) {
	s.mu.Lock()
	s.next, s.nextTrigger, s.beat = next, trigger, now
	s.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
