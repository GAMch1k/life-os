package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/notify"
)

// dashboardKey names the pseudo-check for the status endpoint itself: it is
// a problem while the runtime status cannot be read.
const dashboardKey = "dashboard"

// Observation is one check's state in one evaluation. Key is the check's
// part of the notification category (processing for status.processing).
type Observation struct {
	Key     string
	Title   string
	State   State
	Summary string
	Action  string
}

// Transition is a confirmed change of a check's state, to be notified.
type Transition struct {
	// Check is the latest observation of the check, in its new state.
	Check Observation
	// From is the state last notified for the check.
	From State
	// Since is when the monitor first saw the new state.
	Since time.Time
	// EpisodeSince is, for a recovery, when the episode that ends began;
	// zero when unknown.
	EpisodeSince time.Time
}

// DedupeKey names this transition for the Notifications service: the check,
// its new state, and when that state began. Sending it again, after a failed
// attempt or a restart, never notifies twice.
func (t Transition) DedupeKey() string {
	return fmt.Sprintf("status:%s:%s:%s", t.Check.Key, t.Check.State, t.Since.UTC().Format(time.RFC3339))
}

// run is a sequence of evaluations that saw a check in the same state.
type run struct {
	state State
	since time.Time
	count int
}

type tracked struct {
	latest Observation
	run    run
	// known is set once the state last notified for the check is known:
	// read from the Notifications history, or set by notifying.
	known    bool
	notified State
	// episodeSince is when the not-ok episode that was notified began.
	episodeSince time.Time
}

// Tracker holds what the monitor knows about each check and decides which
// state changes to notify. It does no I/O and is not safe for concurrent use.
//
// A check's state is notified once it has been seen in that many
// evaluations in a row (the confirmations) and differs from the state last
// notified for the check. The last notified state is read from the
// Notifications history (Learn) before anything is notified for a check, so
// a restart neither repeats an episode nor loses one.
type Tracker struct {
	confirmations int
	checks        map[string]*tracked
	order         []string
}

// NewTracker returns a Tracker that needs confirmations evaluations in a row
// (at least 1) before it notifies a new state.
func NewTracker(confirmations int) *Tracker {
	return &Tracker{confirmations: max(confirmations, 1), checks: map[string]*tracked{}}
}

// Observe records one evaluation at now. complete says the observations
// cover every check (the status was read): a check missing from them loses
// its run. When the status could not be read, only the dashboard pseudo-check
// is observed and every other check keeps its run: an evaluation without
// data neither confirms nor interrupts it.
func (t *Tracker) Observe(now time.Time, observations []Observation, complete bool) {
	seen := map[string]bool{}
	for _, observation := range observations {
		if seen[observation.Key] {
			continue
		}
		seen[observation.Key] = true
		check := t.checks[observation.Key]
		if check == nil {
			check = &tracked{notified: OK}
			t.checks[observation.Key] = check
			t.order = append(t.order, observation.Key)
		}
		check.latest = observation
		if check.run.count > 0 && check.run.state == observation.State {
			check.run.count++
		} else {
			check.run = run{state: observation.State, since: now, count: 1}
		}
	}
	if complete {
		for key, check := range t.checks {
			if !seen[key] {
				check.run = run{}
			}
		}
	}
}

// Unknown lists the checks whose last notified state is not known yet.
func (t *Tracker) Unknown() []string {
	var keys []string
	for _, key := range t.order {
		if !t.checks[key].known {
			keys = append(keys, key)
		}
	}
	return keys
}

// Learn sets the state last notified for a check from the newest
// notification the monitor sent for it, or nil when it never sent one (ok).
func (t *Tracker) Learn(key string, sent *notify.Sent) {
	check := t.checks[key]
	if check == nil {
		return
	}
	check.known = true
	check.notified, check.episodeSince = OK, time.Time{}
	if sent == nil {
		return
	}
	state, since := notifiedState(*sent)
	check.notified = state
	if state != OK {
		check.episodeSince = since
	}
}

// notifiedState reads the state and its start from a status notification's
// dedupe key (status:<check>:<state>:<since>), falling back to its
// importance and occurredAt.
func notifiedState(sent notify.Sent) (State, time.Time) {
	parts := strings.SplitN(sent.DedupeKey, ":", 4)
	if len(parts) == 4 && parts[0] == "status" {
		state := State(parts[2])
		since, err := time.Parse(time.RFC3339, parts[3])
		if (state == OK || state == Warning || state == Problem) && err == nil {
			return state, since
		}
	}
	switch sent.Importance {
	case notify.High:
		return Problem, sent.OccurredAt
	case notify.Medium:
		return Warning, sent.OccurredAt
	}
	return OK, sent.OccurredAt
}

// Due lists the confirmed changes to notify now, in the order the checks
// were first seen. A change stays due until Notified records it, so a failed
// send is retried at the next evaluation with the same dedupe key.
func (t *Tracker) Due() []Transition {
	var due []Transition
	for _, key := range t.order {
		check := t.checks[key]
		if !check.known || check.run.count < t.confirmations || check.run.state == check.notified {
			continue
		}
		transition := Transition{Check: check.latest, From: check.notified, Since: check.run.since}
		if check.run.state == OK {
			transition.EpisodeSince = check.episodeSince
		}
		due = append(due, transition)
	}
	return due
}

// Notified records that transition was notified.
func (t *Tracker) Notified(transition Transition) {
	check := t.checks[transition.Check.Key]
	if check == nil {
		return
	}
	check.known = true
	switch {
	case transition.Check.State == OK:
		check.episodeSince = time.Time{}
	case transition.From == OK:
		check.episodeSince = transition.Since
	}
	check.notified = transition.Check.State
}
