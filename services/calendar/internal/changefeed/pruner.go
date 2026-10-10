// Package changefeed keeps the change feed of the events integrations
// follow (see the api package's ListChanges): its entries are written by
// triggers in the transaction of each change (migration 000014), and the
// Pruner drops those older than the retention.
package changefeed

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// DefaultRetention is how long entries are kept
// (CALENDAR_CHANGE_RETENTION_DAYS); a cursor older than that has expired.
const DefaultRetention = 30 * 24 * time.Hour

// pruneEvery is how often the Pruner looks for expired entries.
const pruneEvery = time.Hour

// Pruner deletes the change feed's entries older than its retention, at
// start and then hourly. A cursor before the pruned entries gets 410
// cursor_expired (the feed's pruned_through says how far it was pruned).
type Pruner struct {
	queries   *store.Queries
	retention time.Duration
	logger    *slog.Logger
	now       func() time.Time
}

// NewPruner prunes the feed stored in pool; retention defaults to
// DefaultRetention.
func NewPruner(pool *pgxpool.Pool, retention time.Duration, logger *slog.Logger) *Pruner {
	if retention <= 0 {
		retention = DefaultRetention
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Pruner{queries: store.New(pool), retention: retention, logger: logger, now: time.Now}
}

// WithClock replaces the clock (tests).
func (p *Pruner) WithClock(now func() time.Time) *Pruner {
	p.now = now
	return p
}

// Prune deletes the entries recorded more than the retention ago and
// returns how many went and how far the feed is pruned now.
func (p *Pruner) Prune(ctx context.Context) (store.PruneEventChangesRow, error) {
	now := p.now().UTC()
	return p.queries.PruneEventChanges(ctx, store.PruneEventChangesParams{
		Before: pgtype.Timestamptz{Time: now.Add(-p.retention), Valid: true},
		Now:    pgtype.Timestamptz{Time: now, Valid: true},
	})
}

// Run prunes at start and every hour until ctx ends.
func (p *Pruner) Run(ctx context.Context) {
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	for {
		result, err := p.Prune(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			p.logger.Warn("calendar change feed not pruned", "error", err)
		case err == nil && result.Pruned > 0:
			p.logger.Info("calendar change feed pruned", "entries", result.Pruned, "prunedThrough", result.PrunedThrough)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
