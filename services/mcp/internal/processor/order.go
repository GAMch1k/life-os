package processor

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// Capture order (P5-22). A follow-up ("actually, make it 6") makes sense
// only once the capture it follows is done: planned before, it finds no
// trace of it (the recent captures list only finished ones) and may change
// whatever was done last instead. Run plans the due captures oldest first
// and one at a time, but an earlier capture can still be on its way when a
// later one's turn comes: its attempt ended retryable, so it waits for its
// retry while the poll goes on, or it is still processing (a processor that
// stopped, another instance). So a capture is planned only after every
// capture of its context window (ContextMinutes before it) has finished:
// resolved, waiting for Human Review, failed, or discarded.
//
// Inbox names the unfinished ones in the recent captures read the capture
// needs anyway (unfinished), so waiting costs no extra read and no Codex
// run. A capture that waits is left raw, without an attempt, and the poll
// goes on with the next one; a later capture of the same window then waits
// too, without a read. The next poll looks again. The wait is bounded
// (CaptureOrder.MaxWait, the processor's stale-after time): after it the
// capture is planned anyway, Codex sees which captures are still on their
// way, and the log and the attempt say so.

// defaultOrderWait bounds a capture's wait when no bound is configured.
const defaultOrderWait = 2 * time.Minute

// orderMemory is how long a capture that stopped coming up (discarded or
// resolved by hand while it waited) is remembered.
const orderMemory = time.Hour

// waitingReason is the proposal reason of a capture left for a later poll.
const waitingReason = "waiting for earlier captures"

// CaptureOrder remembers since when each capture has waited for the
// captures before it. A nil CaptureOrder never waits.
type CaptureOrder struct {
	// MaxWait bounds a capture's wait (default 2 minutes).
	MaxWait time.Duration
	// Now is the clock; tests replace it.
	Now func() time.Time

	mu    sync.Mutex
	since map[string]time.Time
}

// NewCaptureOrder makes captures wait at most maxWait for the earlier
// captures of their window.
func NewCaptureOrder(maxWait time.Duration) *CaptureOrder {
	return &CaptureOrder{MaxWait: maxWait}
}

// orderDecision is what a capture does about the unfinished captures
// before it.
type orderDecision struct {
	// wait leaves the capture raw for a later poll.
	wait bool
	// first is the first time it waits (logged once).
	first bool
	// overdue is how long it waited when it is planned anyway; zero when
	// it did not wait or the captures before it have finished.
	overdue time.Duration
}

// decide is whether a capture with that many unfinished captures before it
// waits. A capture that does not wait is forgotten.
func (o *CaptureOrder) decide(itemID string, unfinished int) orderDecision {
	if o == nil {
		return orderDecision{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	for id, at := range o.since {
		if now.Sub(at) > orderMemory {
			delete(o.since, id)
		}
	}
	since, seen := o.since[itemID]
	if unfinished == 0 {
		delete(o.since, itemID)
		return orderDecision{}
	}
	if !seen {
		if o.since == nil {
			o.since = map[string]time.Time{}
		}
		since = now
		o.since[itemID] = now
	}
	if waited := now.Sub(since); waited >= o.maxWait() {
		delete(o.since, itemID)
		return orderDecision{overdue: waited}
	}
	return orderDecision{wait: true, first: !seen}
}

// hold makes a capture wait behind a capture of the same poll that waits,
// without a read. It reports false when the capture already waited its
// time (or nothing waits): it is then read, and planned, as usual.
func (o *CaptureOrder) hold(itemID string) (orderDecision, bool) {
	if o == nil {
		return orderDecision{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	since, seen := o.since[itemID]
	if !seen {
		if o.since == nil {
			o.since = map[string]time.Time{}
		}
		o.since[itemID] = now
		return orderDecision{wait: true, first: true}, true
	}
	if now.Sub(since) >= o.maxWait() {
		return orderDecision{}, false
	}
	return orderDecision{wait: true}, true
}

func (o *CaptureOrder) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *CaptureOrder) maxWait() time.Duration {
	if o.MaxWait > 0 {
		return o.MaxWait
	}
	return defaultOrderWait
}

// behindWaiting reports whether item was captured within the context
// window after a capture of this poll that waits: it waits too.
func (r Runner) behindWaiting(item inboxItem, waiting []time.Time) bool {
	window := time.Duration(r.ContextMinutes) * time.Minute
	for _, at := range waiting {
		if at.Before(item.CreatedAt) && item.CreatedAt.Sub(at) <= window {
			return true
		}
	}
	return false
}

// unfinishedNote names the captures still on their way, for the log and
// the attempt: when each was made, its text, and its state.
func unfinishedNote(captures []unfinishedCapture, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	parts := make([]string, 0, len(captures))
	for _, capture := range captures {
		state := capture.State
		if capture.NextRetryAt != nil {
			state += ", retry at " + capture.NextRetryAt.In(location).Format("15:04:05")
		}
		parts = append(parts, fmt.Sprintf("%s “%s” (%s)", capture.CreatedAt.In(location).Format("15:04:05"), activity.Preview(capture.Content, 60), state))
	}
	return strings.Join(parts, "; ")
}

// overdueNote is what the attempt of a capture planned without waiting
// longer says.
func overdueNote(captures []unfinishedCapture, waited time.Duration, location *time.Location) string {
	n := len(captures)
	noun := "capture"
	if n != 1 {
		noun = "captures"
	}
	return fmt.Sprintf("planned without waiting longer: %d earlier %s still on the way after %s: %s", n, noun, waited.Round(time.Second), unfinishedNote(captures, location))
}

func (r Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}
