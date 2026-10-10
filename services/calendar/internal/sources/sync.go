package sources

import (
	"context"
	"errors"
	"time"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Reconciling the batches integrations send through the integration API
// (P11-06): a snapshot for every event of the source, a delta only for the
// events it names.

// ErrEmptySnapshot: a snapshot without items, guarded against removing the
// source's upcoming events.
var ErrEmptySnapshot = errors.New("the snapshot has no events while the source still has upcoming ones; nothing was changed")

// AuthoritativeRules are a source's that speaks for every event it ever
// gave: one it no longer lists is removed, a changed one rewritten.
func AuthoritativeRules() Rules {
	return Rules{
		keep:   func(store.Event) bool { return false },
		frozen: func(store.Event, Item) bool { return false },
	}
}

// ReconcileSnapshot makes a source's stored events match items, inside the
// caller's transaction (which holds the source row).
// With guardEmpty, no items while the source has an active event still to
// come (at now, in the owner's timezone) is ErrEmptySnapshot and changes
// nothing, as a feed's refresh treats an empty feed.
func ReconcileSnapshot(ctx context.Context, queries *store.Queries, source store.CalendarSource, items []Item, rules Rules, guardEmpty bool, now time.Time, owner *time.Location) (Counts, error) {
	existing, err := queries.ListSourceEvents(ctx, source.ID)
	if err != nil {
		return Counts{}, err
	}
	if guardEmpty && len(items) == 0 && hasActiveFuture(existing, now, owner) {
		return Counts{}, ErrEmptySnapshot
	}
	return reconcileStored(ctx, queries, source, existing, items, rules, now)
}

// ReconcileDelta applies a delta: items are inserted, updated, or restored
// as in a snapshot, and the stored events whose external uid is in deleted
// are removed (cancelled and marked removed, unless rules keep them); every
// other stored event stays as it is. Inside the caller's transaction, which
// holds the source row.
func ReconcileDelta(ctx context.Context, queries *store.Queries, source store.CalendarSource, items []Item, deleted []string, rules Rules, now time.Time) (Counts, error) {
	existing, err := queries.ListSourceEvents(ctx, source.ID)
	if err != nil {
		return Counts{}, err
	}
	named := make(map[string]bool, len(items)+len(deleted))
	for _, item := range items {
		named[item.ExternalUID] = true
	}
	for _, uid := range deleted {
		named[uid] = true
	}
	subset := existing[:0:0]
	for _, event := range existing {
		if event.ExternalUid != nil && named[*event.ExternalUid] {
			subset = append(subset, event)
		}
	}
	return reconcileStored(ctx, queries, source, subset, items, rules, now)
}
