package feeds

import (
	"context"
	"errors"
	"log/slog"
	"time"

	googleuuid "github.com/google/uuid"

	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

// Scheduler refreshes due feeds: each feed's next refresh time is kept in the
// database (daily at the quiet hour, sooner after a failure, right away for a
// new or reconfigured feed), so the schedule survives restarts. It checks
// once a minute and when woken, and refreshes one feed at a time. Each check
// first mirrors what the owner set in Calendar (Refresher.Mirror), so a
// source switched back on there is refreshed within a minute.
type Scheduler struct {
	refresher *Refresher
	queries   *store.Queries
	interval  time.Duration
	wake      chan struct{}
	logger    *slog.Logger
	// calendarDown: the last mirror failed, so the next failure is not
	// logged again until Calendar answers.
	calendarDown bool
}

// NewScheduler polls for due feeds every interval (default one minute).
func NewScheduler(refresher *Refresher, interval time.Duration, logger *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{refresher: refresher, queries: refresher.queries, interval: interval, wake: make(chan struct{}, 1), logger: logger}
}

// Wake makes the scheduler look for due feeds now, such as after a feed was
// added. It never blocks.
func (s *Scheduler) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run refreshes due feeds until ctx ends, starting with any that came due
// while the service was down.
func (s *Scheduler) Run(ctx context.Context) {
	s.logger.Info("calendar feed refresh scheduled", "daily", s.refresher.schedule.String())
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

// RunDue mirrors Calendar, then refreshes every feed due now, one after
// another.
func (s *Scheduler) RunDue(ctx context.Context) {
	if err := s.refresher.Mirror(ctx); err != nil {
		if !s.calendarDown && ctx.Err() == nil {
			s.logger.Warn("calendar feed sources not mirrored from Calendar; retried every minute", "error", calendarMessage(err))
		}
		s.calendarDown = true
	} else if s.calendarDown {
		s.logger.Info("calendar feed sources mirrored from Calendar again")
		s.calendarDown = false
	}
	due, err := s.queries.ListDueFeeds(ctx, at(s.refresher.now().UTC()))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("calendar feeds due for refresh not listed", "error", err)
		}
		return
	}
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		feed, err := s.refresher.Refresh(ctx, googleuuid.UUID(id.Bytes), Trigger{})
		switch {
		case errors.Is(err, ErrInProgress), errors.Is(err, ErrNotFound), errors.Is(err, ErrDisabled), errors.Is(err, ErrSuperseded):
			s.logger.Info("calendar feed refresh skipped", "feed", googleuuid.UUID(id.Bytes), "reason", err.Error())
		case err != nil:
			s.logger.Warn("calendar feed refresh failed", "feed", googleuuid.UUID(id.Bytes), "error", err)
		default:
			s.logger.Info("calendar feed refreshed", "feed", googleuuid.UUID(id.Bytes), "outcome", deref(feed.LastOutcome))
		}
	}
}
