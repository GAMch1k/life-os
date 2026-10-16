package health

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
)

// EventSource opens the stream of container events: docker.Client, or a
// fixture.
type EventSource interface {
	Events(ctx context.Context, since time.Time) (docker.EventStream, error)
}

// eventsDownFor is how long the event stream may stay closed before restart
// loops count as not watched, which is a finding.
const eventsDownFor = 5 * time.Minute

// replayMargin is how far before a broken stream's end a new one asks the
// engine to start again; the tracker ignores an event it already took.
const replayMargin = 5 * time.Second

// Watcher keeps the gateway's event stream open and feeds every event to a
// Tracker, opening the stream again when it breaks.
//
// Restart counts cannot be rebuilt from the past: the Docker Engine answers
// a since query only from its last 256 events of any kind, which on this
// server reach back about two minutes (each health check logs three exec
// events), whatever the filter. So the counts are complete only once one
// stream has been open for the whole restart-loop window; until then a
// count may be too low (Watch).
type Watcher struct {
	source  EventSource
	tracker *Tracker
	window  time.Duration
	now     func() time.Time
	logger  *slog.Logger
	// retry is the first wait before opening the stream again; it doubles
	// up to maxRetry while the stream cannot be opened.
	retry    time.Duration
	maxRetry time.Duration

	mu    sync.Mutex
	watch Watch
	// closedAt is when the last open stream broke: the next one asks for
	// the events from then on.
	closedAt time.Time
}

// Watch is what the event stream has seen.
type Watch struct {
	// Since is when the open stream began to see every event; zero while
	// none is open.
	Since time.Time
	// DownSince is when the stream was last found closed: at the start, or
	// when it broke; zero while it is open.
	DownSince time.Time
	// Error is why the stream is closed.
	Error string
}

// NewWatcher feeds tracker from source's events.
func NewWatcher(source EventSource, tracker *Tracker, window time.Duration, now func() time.Time, logger *slog.Logger) *Watcher {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Watcher{
		source: source, tracker: tracker, window: window, now: now, logger: logger,
		retry: time.Second, maxRetry: 30 * time.Second,
		watch: Watch{DownSince: now().UTC(), Error: "the container events are not followed yet"},
	}
}

// Watch is the stream's state now.
func (w *Watcher) Watch() Watch {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watch
}

// Start opens the stream (the gateway bounds the wait for it), so that the
// first sample already knows whether it is open, and then follows it and
// every later one until ctx ends.
func (w *Watcher) Start(ctx context.Context) {
	stream := w.open(ctx)
	go w.run(ctx, stream)
}

func (w *Watcher) run(ctx context.Context, stream docker.EventStream) {
	retry := w.retry
	for {
		if stream != nil {
			w.follow(ctx, stream)
			retry = w.retry
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if stream = w.open(ctx); stream == nil {
			retry = min(2*retry, w.maxRetry)
		}
	}
}

// open opens a stream, bound to ctx; nil when it cannot be opened. The
// first one asks for the events of the whole window, a later one for those
// since the last one broke: whatever the engine still holds of them.
func (w *Watcher) open(ctx context.Context) docker.EventStream {
	since := w.now().UTC().Add(-w.window)
	w.mu.Lock()
	if resume := w.closedAt.Add(-replayMargin); !w.closedAt.IsZero() && resume.After(since) {
		since = resume
	}
	w.mu.Unlock()
	stream, err := w.source.Events(ctx, since)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if w.watch.Error != err.Error() {
			w.logger.Warn("the container events cannot be followed", "error", err)
		}
		w.watch.Error = err.Error()
		return nil
	}
	w.watch = Watch{Since: stream.Since().UTC()}
	return stream
}

// follow feeds the stream's events to the tracker until it breaks or ctx
// ends.
func (w *Watcher) follow(ctx context.Context, stream docker.EventStream) {
	defer stream.Close()
	for {
		event, err := stream.Next()
		if err != nil && ctx.Err() != nil {
			return
		}
		if err != nil {
			now := w.now().UTC()
			w.mu.Lock()
			w.watch = Watch{DownSince: now, Error: "the event stream broke: " + err.Error()}
			w.closedAt = now
			w.mu.Unlock()
			w.logger.Warn("the container event stream broke; opening it again", "error", err)
			return
		}
		w.tracker.Events([]docker.Event{event})
	}
}
