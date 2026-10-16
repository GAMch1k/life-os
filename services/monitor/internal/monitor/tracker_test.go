package monitor

import (
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/notify"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// minute is the start of the i-th evaluation, one minute apart.
func minute(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }

func processing(state State) Observation {
	return Observation{Key: "processing", Title: "Automatic processing", State: state, Summary: "summary " + string(state)}
}

// observe records one complete evaluation of the given checks.
func observe(tracker *Tracker, at time.Time, checks ...Observation) []Transition {
	tracker.Observe(at, append(checks, reachable()), true)
	return tracker.Due()
}

// known returns a tracker that already read an empty history for every check
// the given observations name.
func known(confirmations int, checks ...Observation) *Tracker {
	tracker := NewTracker(confirmations)
	tracker.Observe(t0.Add(-time.Hour), append(checks, reachable()), false)
	for _, key := range tracker.Unknown() {
		tracker.Learn(key, nil)
	}
	tracker.Observe(t0.Add(-time.Hour), nil, true) // forget the setup runs
	return tracker
}

// notifyAll records every due transition as notified and returns them.
func notifyAll(tracker *Tracker, due []Transition) []Transition {
	for _, transition := range due {
		tracker.Notified(transition)
	}
	return due
}

func expectOne(t *testing.T, due []Transition, key string, from, to State, since time.Time) Transition {
	t.Helper()
	if len(due) != 1 {
		t.Fatalf("due = %+v, want one transition of %s", due, key)
	}
	got := due[0]
	if got.Check.Key != key || got.From != from || got.Check.State != to || !got.Since.Equal(since) {
		t.Fatalf("transition = %s %s→%s since %s, want %s %s→%s since %s", got.Check.Key, got.From, got.Check.State, got.Since, key, from, to, since)
	}
	return got
}

func TestAProblemIsNotifiedOnlyAfterTheConfirmations(t *testing.T) {
	tracker := known(2, processing(OK))
	if due := observe(tracker, minute(0), processing(Problem)); len(due) != 0 {
		t.Fatalf("one evaluation must not notify: %+v", due)
	}
	got := expectOne(t, observe(tracker, minute(1), processing(Problem)), "processing", OK, Problem, minute(0))
	if key := got.DedupeKey(); key != "status:processing:problem:2026-10-10T12:00:00Z" {
		t.Fatalf("dedupe key = %q", key)
	}
	if got.Check.Summary != "summary problem" {
		t.Fatalf("the transition carries the latest observation: %+v", got.Check)
	}
	notifyAll(tracker, []Transition{got})
	for i := 2; i < 10; i++ {
		if due := observe(tracker, minute(i), processing(Problem)); len(due) != 0 {
			t.Fatalf("a problem that stays is notified once: %+v", due)
		}
	}
}

func TestARecoveryNeedsTheConfirmationsToo(t *testing.T) {
	tracker := known(2, processing(OK))
	observe(tracker, minute(0), processing(Problem))
	notifyAll(tracker, observe(tracker, minute(1), processing(Problem)))

	if due := observe(tracker, minute(10), processing(OK)); len(due) != 0 {
		t.Fatalf("one ok evaluation must not recover: %+v", due)
	}
	got := expectOne(t, observe(tracker, minute(11), processing(OK)), "processing", Problem, OK, minute(10))
	if !got.EpisodeSince.Equal(minute(0)) || got.DedupeKey() != "status:processing:ok:2026-10-10T12:10:00Z" {
		t.Fatalf("recovery = %+v, key %s", got, got.DedupeKey())
	}
	notifyAll(tracker, []Transition{got})
	if due := observe(tracker, minute(12), processing(OK)); len(due) != 0 {
		t.Fatalf("a recovery is notified once: %+v", due)
	}
}

func TestFlappingFasterThanTheConfirmationsNotifiesNothing(t *testing.T) {
	tracker := known(2, processing(OK))
	states := []State{Problem, OK, Problem, Warning, Problem, OK, Warning, OK, Problem, OK}
	for i, state := range states {
		if due := observe(tracker, minute(i), processing(state)); len(due) != 0 {
			t.Fatalf("evaluation %d (%s): due = %+v", i, state, due)
		}
	}
	// A problem that then holds is notified, from when it began holding.
	observe(tracker, minute(10), processing(Problem))
	expectOne(t, observe(tracker, minute(11), processing(Problem)), "processing", OK, Problem, minute(10))
}

func TestOneConfirmationNotifiesAtOnce(t *testing.T) {
	tracker := known(1, processing(OK))
	expectOne(t, observe(tracker, minute(0), processing(Warning)), "processing", OK, Warning, minute(0))
}

func TestWarningsAndProblemsEachNotifyTheirChange(t *testing.T) {
	tracker := known(2, processing(OK))
	steps := []struct {
		state State
		from  State
	}{{Warning, OK}, {Problem, Warning}, {Warning, Problem}, {OK, Warning}}
	for i, step := range steps {
		at := minute(10 * i)
		observe(tracker, at, processing(step.state))
		got := expectOne(t, observe(tracker, at.Add(time.Minute), processing(step.state)), "processing", step.from, step.state, at)
		if step.state == OK && !got.EpisodeSince.Equal(minute(0)) {
			t.Fatalf("the recovery ends the episode that began with the first warning: %+v", got)
		}
		notifyAll(tracker, []Transition{got})
	}
}

func TestAnUnreadableStatusKeepsTheChecksRunsAndIsItsOwnProblem(t *testing.T) {
	tracker := known(2, processing(OK))
	observe(tracker, minute(0), processing(Problem))

	// The status cannot be read: processing neither confirms nor breaks.
	tracker.Observe(minute(1), []Observation{unreachable(errTest)}, false)
	if due := tracker.Due(); len(due) != 0 {
		t.Fatalf("due = %+v", due)
	}
	tracker.Observe(minute(2), []Observation{unreachable(errTest)}, false)
	got := expectOne(t, tracker.Due(), dashboardKey, OK, Problem, minute(1))
	if got.DedupeKey() != "status:dashboard:problem:2026-10-10T12:01:00Z" {
		t.Fatalf("dedupe key = %s", got.DedupeKey())
	}
	notifyAll(tracker, []Transition{got})

	// Readable again: processing's run continues, the dashboard recovers
	// after its own confirmations.
	due := observe(tracker, minute(3), processing(Problem))
	expectOne(t, due, "processing", OK, Problem, minute(0))
	notifyAll(tracker, due)
	expectOne(t, observe(tracker, minute(4), processing(Problem)), dashboardKey, Problem, OK, minute(3))
}

func TestACheckThatDisappearsLosesItsRun(t *testing.T) {
	tracker := known(2, processing(OK))
	observe(tracker, minute(0), processing(Problem))
	observe(tracker, minute(1)) // the status no longer has the check
	if due := observe(tracker, minute(2), processing(Problem)); len(due) != 0 {
		t.Fatalf("the run restarted, so one evaluation is not enough: %+v", due)
	}
	expectOne(t, observe(tracker, minute(3), processing(Problem)), "processing", OK, Problem, minute(2))
}

func TestADueChangeKeepsItsDedupeKeyUntilNotified(t *testing.T) {
	tracker := known(2, processing(OK))
	observe(tracker, minute(0), processing(Problem))
	first := observe(tracker, minute(1), processing(Problem))
	second := observe(tracker, minute(2), processing(Problem))
	if len(first) != 1 || len(second) != 1 || first[0].DedupeKey() != second[0].DedupeKey() {
		t.Fatalf("an unsent change is retried with the same key: %+v then %+v", first, second)
	}
}

func TestNothingIsNotifiedBeforeTheHistoryIsKnown(t *testing.T) {
	tracker := NewTracker(2)
	observe(tracker, minute(0), processing(Problem))
	if due := observe(tracker, minute(1), processing(Problem)); len(due) != 0 {
		t.Fatalf("due before the history was read: %+v", due)
	}
	if unknown := tracker.Unknown(); len(unknown) != 2 || unknown[0] != "processing" || unknown[1] != dashboardKey {
		t.Fatalf("unknown = %v", unknown)
	}
	tracker.Learn("processing", nil)
	tracker.Learn(dashboardKey, nil)
	// The confirmations already happened; the change dates from the first.
	expectOne(t, tracker.Due(), "processing", OK, Problem, minute(0))
	if unknown := tracker.Unknown(); len(unknown) != 0 {
		t.Fatalf("unknown = %v", unknown)
	}
}

func history(key, importance string) *notify.Sent {
	return &notify.Sent{DedupeKey: key, Importance: notify.Importance(importance), OccurredAt: minute(-30)}
}

func restarted(sent *notify.Sent) *Tracker {
	tracker := NewTracker(2)
	tracker.Observe(minute(0), []Observation{processing(OK)}, true)
	tracker.Learn("processing", sent)
	tracker.Observe(minute(0), nil, true)
	return tracker
}

func TestARestartNeverRepeatsANotifiedEpisode(t *testing.T) {
	tracker := restarted(history("status:processing:problem:2026-10-10T11:00:00Z", "high"))
	for i := range 5 {
		tracker.Observe(minute(i), []Observation{processing(Problem)}, false)
		if due := tracker.Due(); len(due) != 0 {
			t.Fatalf("the problem was notified before the restart: %+v", due)
		}
	}
}

func TestARestartStillSendsTheRecoveryOfANotifiedEpisode(t *testing.T) {
	tracker := restarted(history("status:processing:problem:2026-10-10T11:00:00Z", "high"))
	tracker.Observe(minute(0), []Observation{processing(OK)}, false)
	tracker.Observe(minute(1), []Observation{processing(OK)}, false)
	got := expectOne(t, tracker.Due(), "processing", Problem, OK, minute(0))
	if !got.EpisodeSince.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("the episode began at the notified since: %+v", got)
	}
}

func TestARestartNeverLosesAProblemThatWasNotNotified(t *testing.T) {
	for name, sent := range map[string]*notify.Sent{
		"no history":         nil,
		"recovered":          history("status:processing:ok:2026-10-10T11:00:00Z", "low"),
		"an earlier warning": history("status:processing:warning:2026-10-10T11:00:00Z", "medium"),
	} {
		t.Run(name, func(t *testing.T) {
			tracker := restarted(sent)
			tracker.Observe(minute(0), []Observation{processing(Problem)}, false)
			tracker.Observe(minute(1), []Observation{processing(Problem)}, false)
			from := OK
			if sent != nil && sent.Importance == notify.Medium {
				from = Warning
			}
			expectOne(t, tracker.Due(), "processing", from, Problem, minute(0))
		})
	}
}

func TestHistoryWithoutAReadableKeyFallsBackToItsImportance(t *testing.T) {
	tracker := restarted(history("something-else", "high"))
	tracker.Observe(minute(0), []Observation{processing(OK)}, false)
	tracker.Observe(minute(1), []Observation{processing(OK)}, false)
	got := expectOne(t, tracker.Due(), "processing", Problem, OK, minute(0))
	if !got.EpisodeSince.Equal(minute(-30)) {
		t.Fatalf("the episode began at occurredAt: %+v", got)
	}
}

func TestAProvisionalOkNeitherConfirmsNorInterruptsARun(t *testing.T) {
	provisional := processing(OK)
	provisional.Provisional = true
	tracker := known(2, processing(OK))
	notifyAll(tracker, observe(tracker, minute(0), processing(Warning)))
	expectOne(t, notifyAll(tracker, observe(tracker, minute(1), processing(Warning))), "processing", OK, Warning, minute(0))
	for i := 2; i < 20; i++ {
		if due := observe(tracker, minute(i), provisional); len(due) != 0 {
			t.Fatalf("a provisional ok is no recovery: %+v", due)
		}
	}
	// It does not interrupt a run either: a warning seen before and after
	// it is confirmed.
	observe(tracker, minute(20), processing(Problem))
	observe(tracker, minute(21), provisional)
	expectOne(t, observe(tracker, minute(22), processing(Problem)), "processing", Warning, Problem, minute(20))

	// Only an ok can be provisional.
	warning := processing(Warning)
	warning.Provisional = true
	fresh := known(1, processing(OK))
	expectOne(t, observe(fresh, minute(0), warning), "processing", OK, Warning, minute(0))
}
