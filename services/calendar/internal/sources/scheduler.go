package sources

import (
	"context"
	"errors"
	"log/slog"
	"time"

	googleuuid "github.com/google/uuid"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Scheduler refreshes due sources: each source's next refresh time is kept
// in the database (daily at the quiet hour, sooner after a failure, right
// away for a new or reconfigured source), so the schedule survives restarts.
// It checks once a minute and when woken, and refreshes one source at a time.
type Scheduler struct {
	refresher *Refresher
	queries   *store.Queries
	interval  time.Duration
	wake      chan struct{}
	logger    *slog.Logger
}

// NewScheduler polls for due sources every interval (default one minute).
func NewScheduler(refresher *Refresher, interval time.Duration, logger *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{refresher: refresher, queries: refresher.queries, interval: interval, wake: make(chan struct{}, 1), logger: logger}
}

// Wake makes the scheduler look for due sources now, such as after a source
// was added. It never blocks.
func (s *Scheduler) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run refreshes due sources until ctx ends, starting with any that came due
// while the service was down.
func (s *Scheduler) Run(ctx context.Context) {
	s.logger.Info("calendar source refresh scheduled", "daily", s.refresher.schedule.String())
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		s.RunDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// RunDue refreshes every source due now, one after another.
func (s *Scheduler) RunDue(ctx context.Context) {
	due, err := s.queries.ListDueSources(ctx, at(s.refresher.now().UTC()))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("calendar sources due for refresh not listed", "error", err)
		}
		return
	}
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		source, err := s.refresher.Refresh(ctx, googleuuid.UUID(id.Bytes), Trigger{})
		switch {
		case errors.Is(err, ErrInProgress), errors.Is(err, ErrNotFound), errors.Is(err, ErrDisabled), errors.Is(err, ErrSuperseded):
			s.logger.Info("calendar source refresh skipped", "source", googleuuid.UUID(id.Bytes), "reason", err.Error())
		case err != nil:
			s.logger.Warn("calendar source refresh failed", "source", googleuuid.UUID(id.Bytes), "error", err)
		default:
			s.logger.Info("calendar source refreshed", "source", googleuuid.UUID(id.Bytes), "outcome", deref(source.LastOutcome))
		}
	}
}
