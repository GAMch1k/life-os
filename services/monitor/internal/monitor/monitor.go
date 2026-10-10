package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/GAMch1k/life-os/pkg/notify"
)

// Notifier sends notifications and reads the monitor's own history from the
// Notifications service; *notify.Client implements it.
type Notifier interface {
	Send(ctx context.Context, n notify.Notification) error
	Latest(ctx context.Context, category string) (*notify.Sent, error)
}

// Options configures a Monitor.
type Options struct {
	Status   StatusSource
	Notifier Notifier
	// Interval is the time between two evaluations.
	Interval time.Duration
	// Confirmations is how many evaluations in a row must agree on a new
	// state before it is notified.
	Confirmations int
	Logger        *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Monitor evaluates the runtime status on an interval and notifies confirmed
// changes. Its state is in memory; the Notifications history restores what
// was last notified after a restart.
type Monitor struct {
	status   StatusSource
	notifier Notifier
	sending  bool
	interval time.Duration
	tracker  *Tracker
	logger   *slog.Logger
	now      func() time.Time
	// lastEvaluation is when the latest evaluation finished (Unix ns).
	lastEvaluation atomic.Int64
}

// New returns a Monitor; Run starts it.
func New(options Options) *Monitor {
	m := &Monitor{
		status:   options.Status,
		notifier: options.Notifier,
		sending:  true,
		interval: options.Interval,
		tracker:  NewTracker(options.Confirmations),
		logger:   options.Logger,
		now:      options.Now,
	}
	if enabled, ok := options.Notifier.(interface{ Enabled() bool }); ok {
		m.sending = enabled.Enabled()
	}
	if m.interval <= 0 {
		m.interval = time.Minute
	}
	if m.logger == nil {
		m.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.lastEvaluation.Store(m.now().UnixNano())
	return m
}

// Run evaluates at once and then every interval until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		m.Evaluate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Evaluate reads the runtime status once, records each check's state, and
// sends the notifications that became due.
func (m *Monitor) Evaluate(ctx context.Context) {
	now := m.now()
	status, err := m.status.Fetch(ctx)
	if ctx.Err() != nil {
		return // shutting down: this evaluation saw nothing
	}
	if err != nil {
		m.logger.WarnContext(ctx, "runtime status unreachable", "error", err)
		m.tracker.Observe(now, []Observation{unreachable(err)}, false)
	} else {
		m.tracker.Observe(now, observations(status), true)
	}

	// Nothing is notified for a check before its history is known. When
	// Notifications cannot be read, it could not be sent to either: the
	// remaining checks are tried at the next evaluation.
	for _, key := range m.tracker.Unknown() {
		sent, err := m.notifier.Latest(ctx, category(key))
		if err != nil {
			m.logger.WarnContext(ctx, "notification history unavailable; notifying waits for it", "check", key, "error", err)
			break
		}
		m.tracker.Learn(key, sent)
	}

	for _, transition := range m.tracker.Due() {
		m.notify(ctx, transition)
	}
	m.lastEvaluation.Store(m.now().UnixNano())
}

func (m *Monitor) notify(ctx context.Context, transition Transition) {
	n := notification(transition)
	attrs := []any{
		"check", transition.Check.Key,
		"from", transition.From,
		"to", transition.Check.State,
		"since", transition.Since.UTC().Format(time.RFC3339),
		"dedupe_key", n.DedupeKey,
	}
	if err := m.notifier.Send(ctx, n); err != nil {
		var status *notify.StatusError
		if errors.As(err, &status) && !status.Temporary() {
			// Sending it again cannot succeed: move on rather than retry
			// every evaluation.
			m.logger.ErrorContext(ctx, "status notification rejected", append(attrs, "error", err)...)
			m.tracker.Notified(transition)
			return
		}
		m.logger.WarnContext(ctx, "status notification not sent; retried at the next evaluation", append(attrs, "error", err)...)
		return
	}
	m.tracker.Notified(transition)
	m.logger.InfoContext(ctx, "status changed", append(attrs, "notified", m.sending)...)
}

// Ready reports whether evaluations are running: the latest one finished
// less than three intervals (plus a minute for slow dependencies) ago.
func (m *Monitor) Ready(context.Context) error {
	last := time.Unix(0, m.lastEvaluation.Load())
	if age := m.now().Sub(last); age > 3*m.interval+time.Minute {
		return fmt.Errorf("no status evaluation for %s", age.Round(time.Second))
	}
	return nil
}
