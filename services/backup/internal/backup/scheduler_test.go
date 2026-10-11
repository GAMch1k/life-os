package backup

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeStarter records the runs the scheduler starts; busy makes the next
// Start calls fail with ErrRunning.
type fakeStarter struct {
	mu          sync.Mutex
	lastSuccess *Summary
	lastCheck   *RestoreCheck
	busy        int
	started     []startedRun
}

type startedRun struct {
	trigger Trigger
	check   bool
	at      time.Time
}

func (f *fakeStarter) Start(request Request) (*Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy > 0 {
		f.busy--
		return nil, ErrRunning
	}
	f.started = append(f.started, startedRun{trigger: request.Trigger, check: request.ThenRestoreCheck})
	return &Job{}, nil
}

func (f *fakeStarter) LastRestoreCheck() (*RestoreCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCheck, nil
}

func (f *fakeStarter) LastFullSuccess() (*Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSuccess, nil
}

// fakeClock moves on by each wait; the loop ends after until.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	until time.Time
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) wait(_ context.Context, d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	return c.now.Before(c.until)
}

func runScheduler(t *testing.T, starter *fakeStarter, start, until time.Time) (*Scheduler, *fakeClock) {
	t.Helper()
	return runSchedulerWith(t, starter, start, until, false)
}

func runSchedulerWith(t *testing.T, starter *fakeStarter, start, until time.Time, checks bool) (*Scheduler, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: start, until: until}
	scheduler := NewScheduler(SchedulerOptions{
		Runner:          starter,
		Schedule:        Schedule{Minute: 195, Location: berlin(t)},
		CatchUpDelay:    time.Minute,
		Tick:            time.Minute,
		RestoreChecks:   checks,
		RestoreCheckDay: time.Sunday,
		Now:             clock.Now,
	})
	scheduler.wait = clock.wait
	recording := &recordingStarter{fakeStarter: starter, clock: clock}
	scheduler.opts.Runner = recording
	scheduler.Run(context.Background())
	return scheduler, clock
}

// recordingStarter stamps each started run with the fake time.
type recordingStarter struct {
	*fakeStarter
	clock *fakeClock
}

func (r *recordingStarter) Start(request Request) (*Job, error) {
	job, err := r.fakeStarter.Start(request)
	if err == nil {
		r.mu.Lock()
		r.started[len(r.started)-1].at = r.clock.Now()
		if request.ThenRestoreCheck {
			r.lastCheck = &RestoreCheck{StartedAt: r.clock.Now()}
		}
		r.mu.Unlock()
	}
	return job, err
}

func TestTheSchedulerRunsNightlyAtTheOwnersTime(t *testing.T) {
	location := berlin(t)
	// A fresh success at start: no catch-up.
	starter := &fakeStarter{lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 23, 3, 15, 0, 0, location)}}
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, location)
	scheduler, _ := runScheduler(t, starter, start, time.Date(2026, 10, 26, 12, 0, 0, 0, location))
	want := []time.Time{
		time.Date(2026, 10, 24, 3, 15, 0, 0, location),
		time.Date(2026, 10, 25, 3, 15, 0, 0, location), // the clocks went back that night
		time.Date(2026, 10, 26, 3, 15, 0, 0, location),
	}
	if len(starter.started) != len(want) {
		t.Fatalf("started %+v, want %d runs", starter.started, len(want))
	}
	for i, run := range starter.started {
		if run.trigger != TriggerSchedule || !run.at.Equal(want[i]) {
			t.Errorf("run %d = %s at %s, want schedule at %s", i, run.trigger, run.at, want[i])
		}
	}
	if next, trigger := scheduler.Next(); !next.Equal(time.Date(2026, 10, 27, 3, 15, 0, 0, location)) || trigger != TriggerSchedule {
		t.Errorf("Next = %s %s", next, trigger)
	}
}

func TestTheSchedulerCatchesUpAMissedNight(t *testing.T) {
	location := berlin(t)
	// The service was down at 03:15 and starts at 09:00.
	starter := &fakeStarter{lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 10, 3, 15, 0, 0, location)}}
	start := time.Date(2026, 10, 11, 9, 0, 0, 0, location)
	_, clock := runScheduler(t, starter, start, time.Date(2026, 10, 11, 12, 0, 0, 0, location))
	if len(starter.started) != 1 || starter.started[0].trigger != TriggerCatchUp || !starter.started[0].at.Equal(start.Add(time.Minute)) {
		t.Fatalf("started %+v, want one catch-up a minute after start", starter.started)
	}
	if clock.waits[0] != time.Minute {
		t.Errorf("first wait = %s", clock.waits[0])
	}
}

func TestAFirstStartCatchesUp(t *testing.T) {
	starter := &fakeStarter{}
	start := time.Date(2026, 10, 11, 9, 0, 0, 0, berlin(t))
	runScheduler(t, starter, start, start.Add(time.Hour))
	if len(starter.started) != 1 || starter.started[0].trigger != TriggerCatchUp {
		t.Fatalf("started %+v", starter.started)
	}
}

func TestADueRunWaitsForTheRunInProgress(t *testing.T) {
	location := berlin(t)
	// A manual run is going at 03:15: the nightly run starts once it ends.
	starter := &fakeStarter{lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 11, 3, 15, 0, 0, location)}, busy: 2}
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, location)
	runScheduler(t, starter, start, time.Date(2026, 10, 12, 4, 0, 0, 0, location))
	if len(starter.started) != 1 || starter.started[0].trigger != TriggerSchedule {
		t.Fatalf("started %+v", starter.started)
	}
	if want := time.Date(2026, 10, 12, 3, 17, 0, 0, location); !starter.started[0].at.Equal(want) {
		t.Errorf("started at %s, want %s (two ticks after 03:15)", starter.started[0].at, want)
	}
}

func TestANightlyRunStandsInForAPendingCatchUp(t *testing.T) {
	location := berlin(t)
	// Started a few seconds before 03:15 with the previous night missed: the
	// scheduled run comes first and no catch-up follows.
	starter := &fakeStarter{lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 9, 3, 15, 0, 0, location)}}
	start := time.Date(2026, 10, 11, 3, 14, 30, 0, location)
	runScheduler(t, starter, start, time.Date(2026, 10, 11, 6, 0, 0, 0, location))
	if len(starter.started) != 1 || starter.started[0].trigger != TriggerSchedule {
		t.Fatalf("started %+v", starter.started)
	}
}

func TestSchedulerReadiness(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	scheduler := NewScheduler(SchedulerOptions{Runner: &fakeStarter{}, Schedule: Schedule{Minute: 195}, Tick: time.Minute, Now: clock.Now})
	if err := scheduler.Ready(context.Background()); err != nil {
		t.Fatalf("Ready at start = %v", err)
	}
	clock.now = clock.now.Add(5 * time.Minute)
	if err := scheduler.Ready(context.Background()); err == nil {
		t.Fatal("Ready after five minutes without a check")
	}
}

func TestTheSundayNightlyRunIsFollowedByARestoreCheck(t *testing.T) {
	location := berlin(t)
	// Saturday noon, with a check last Sunday: Sunday's run is followed by
	// one, Monday's and the next days' are not.
	starter := &fakeStarter{
		lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 10, 3, 15, 0, 0, location)},
		lastCheck:   &RestoreCheck{StartedAt: time.Date(2026, 10, 4, 3, 20, 0, 0, location)},
	}
	scheduler, _ := runSchedulerWith(t, starter, time.Date(2026, 10, 10, 12, 0, 0, 0, location), time.Date(2026, 10, 14, 12, 0, 0, 0, location), true)
	var checks []bool
	for _, run := range starter.started {
		checks = append(checks, run.check)
	}
	if want := []bool{true, false, false, false}; !slices.Equal(checks, want) {
		t.Fatalf("checks after runs %v, want %v", checks, want)
	}
	if got := scheduler.NextRestoreCheck(); !got.Equal(time.Date(2026, 10, 18, 3, 15, 0, 0, location)) {
		t.Errorf("NextRestoreCheck = %s, want Sunday 18 October 03:15", got)
	}
}

func TestAMissedRestoreCheckFollowsTheNextNightlyRun(t *testing.T) {
	location := berlin(t)
	// The last check is 8 days old on Monday: Monday's run catches it up.
	starter := &fakeStarter{
		lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 11, 3, 15, 0, 0, location)},
		lastCheck:   &RestoreCheck{StartedAt: time.Date(2026, 10, 4, 3, 20, 0, 0, location)},
	}
	scheduler, _ := runSchedulerWith(t, starter, time.Date(2026, 10, 11, 12, 0, 0, 0, location), time.Date(2026, 10, 13, 12, 0, 0, 0, location), true)
	if len(starter.started) != 2 || !starter.started[0].check || starter.started[1].check {
		t.Fatalf("started %+v", starter.started)
	}
	if got := scheduler.NextRestoreCheck(); !got.Equal(time.Date(2026, 10, 18, 3, 15, 0, 0, location)) {
		t.Errorf("NextRestoreCheck = %s", got)
	}
	// Never checked: the next run is followed by a check, whatever the day.
	never := &fakeStarter{lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 12, 3, 15, 0, 0, location)}}
	runSchedulerWith(t, never, time.Date(2026, 10, 12, 12, 0, 0, 0, location), time.Date(2026, 10, 13, 12, 0, 0, 0, location), true)
	if len(never.started) != 1 || !never.started[0].check {
		t.Fatalf("never checked: started %+v", never.started)
	}
}

func TestRestoreChecksCanBeOff(t *testing.T) {
	location := berlin(t)
	starter := &fakeStarter{lastSuccess: &Summary{StartedAt: time.Date(2026, 10, 10, 3, 15, 0, 0, location)}}
	scheduler, _ := runSchedulerWith(t, starter, time.Date(2026, 10, 10, 12, 0, 0, 0, location), time.Date(2026, 10, 12, 12, 0, 0, 0, location), false)
	for _, run := range starter.started {
		if run.check {
			t.Fatalf("a check with checks off: %+v", starter.started)
		}
	}
	if !scheduler.NextRestoreCheck().IsZero() {
		t.Error("NextRestoreCheck with checks off")
	}
}
