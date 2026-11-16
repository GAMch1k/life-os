package banking

import (
	"context"
	"errors"
	"time"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// Run checks the connections every tick until ctx ends: consent reminders
// and expiry, archives Finance still owes, and the syncs that are due.
// First it marks history reads a stopped process left running as failed.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = time.Minute
	}
	s.failInterruptedHistory(ctx)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.Tick(ctx)
		timer.Reset(tick)
	}
}

// Tick runs one check of every connection.
func (s *Service) Tick(ctx context.Context) {
	connections, err := s.queries.ListConnections(ctx)
	if err != nil {
		s.logger.Warn("listing bank connections failed", "error", err)
		return
	}
	for _, conn := range connections {
		if ctx.Err() != nil {
			return
		}
		if conn.Status == StatusDisconnected {
			if conn.ArchivePending && s.finance != nil {
				s.archive(ctx, conn)
			}
			continue
		}
		conn = s.checkConsent(ctx, conn)
		if active(conn) && s.Configured() {
			s.syncIfDue(ctx, conn)
		}
	}
}

// syncIfDue runs a scheduled sync of conn when its time has come and no
// other sync runs.
func (s *Service) syncIfDue(ctx context.Context, conn store.BankConnection) {
	now := s.now()
	if !conn.NextSyncAt.Valid {
		next := s.opts.Schedule.Next(now)
		if err := s.queries.SetNextSync(ctx, store.SetNextSyncParams{NextSyncAt: timestamptz(next), Now: timestamptz(now), ID: conn.ID}); err != nil {
			s.logger.Warn("scheduling the bank sync failed", "connection", uuidString(conn.ID), "error", err)
		}
		return
	}
	if conn.NextSyncAt.Time.After(now) {
		return
	}
	if !s.syncMu.TryLock() {
		return
	}
	defer s.syncMu.Unlock()
	fresh, err := s.queries.GetConnection(ctx, conn.ID)
	if err != nil || !active(fresh) || !fresh.NextSyncAt.Valid || fresh.NextSyncAt.Time.After(now) {
		return
	}
	// A sync under way finishes (bounded) even when the scheduler stops, so
	// what was read is recorded.
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.SyncTimeout)
	defer cancel()
	if _, err := s.runSync(syncCtx, fresh, TriggerScheduled, nil); err != nil && !errors.Is(err, ErrDailyLimit) {
		s.logger.Debug("scheduled bank sync did not complete", "connection", uuidString(conn.ID), "error", diagnostics.ErrorCode(err))
	}
}
