package sources

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Counts summarizes a reconciling refresh.
type Counts struct {
	FeedEvents int `json:"feedEvents"`
	Imported   int `json:"imported"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Removed    int `json:"removed"`
	Restored   int `json:"restored"`
	Unchanged  int `json:"unchanged"`
	Skipped    int `json:"skipped"`
}

// update rewrites a stored event from its feed item.
type update struct {
	id      pgtype.UUID
	item    ics.Item
	restore bool
	// cancelledAt is kept when the event stays cancelled.
	cancelledAt pgtype.Timestamptz
}

// plan is what one refresh changes.
type plan struct {
	inserts  []ics.Item
	updates  []update
	removals []pgtype.UUID
	counts   Counts
}

// reconcile compares a source's stored events with the items of its feed
// (already filtered) at now:
//
//   - an item whose UID is new is inserted;
//   - a stored event whose item changed is updated in place, and one the
//     source had removed is restored;
//   - a stored future event whose UID is missing from the feed is cancelled
//     and marked removed (never deleted, never kept active);
//   - past events are left alone: one that started before now is neither
//     removed nor rewritten unless the feed moves it into the future.
//
// It is idempotent: reconciling the same items again changes nothing.
func reconcile(existing []store.Event, items []ics.Item, now time.Time, owner *time.Location) plan {
	var result plan
	result.counts.Imported = len(items)
	stored := make(map[string]store.Event, len(existing))
	for _, event := range existing {
		if event.ExternalUid != nil {
			stored[*event.ExternalUid] = event
		}
	}
	inFeed := make(map[string]bool, len(items))
	for _, item := range items {
		inFeed[item.ExternalUID] = true
		event, found := stored[item.ExternalUID]
		if !found {
			result.inserts = append(result.inserts, item)
			result.counts.Inserted++
			continue
		}
		if startOf(event, owner).Before(now) && item.Start(owner).Before(now) {
			result.counts.Unchanged++
			continue
		}
		removed := event.RemovedFromSourceAt.Valid
		if !removed && matches(event, item) {
			result.counts.Unchanged++
			continue
		}
		change := update{id: event.ID, item: item, restore: removed}
		if item.Cancelled && event.Status == "cancelled" && !removed {
			change.cancelledAt = event.CancelledAt
		}
		result.updates = append(result.updates, change)
		if removed {
			result.counts.Restored++
		} else {
			result.counts.Updated++
		}
	}
	for _, event := range existing {
		if event.ExternalUid == nil || inFeed[*event.ExternalUid] || event.RemovedFromSourceAt.Valid {
			continue
		}
		if startOf(event, owner).Before(now) {
			continue // past (or under way): history stays as it was
		}
		result.removals = append(result.removals, event.ID)
		result.counts.Removed++
	}
	return result
}

// startOf is a stored event's start instant; an all-day event starts at
// midnight of its first day in owner.
func startOf(event store.Event, owner *time.Location) time.Time {
	if event.AllDay {
		day := event.StartDate.Time
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, owner)
	}
	return event.StartAt.Time
}

// matches reports whether the stored event already says what item says.
func matches(event store.Event, item ics.Item) bool {
	status := "active"
	if item.Cancelled {
		status = "cancelled"
	}
	if event.Title != item.Title || !sameText(event.Description, item.Description) || !sameText(event.Location, item.Location) ||
		event.AllDay != item.AllDay || event.Status != status {
		return false
	}
	if item.AllDay {
		return sameDay(event.StartDate, item.StartDate) && sameDay(event.EndDate, item.EndDate)
	}
	return event.StartAt.Valid && event.StartAt.Time.Equal(item.StartAt) && event.EndAt.Valid && event.EndAt.Time.Equal(item.EndAt) &&
		sameText(event.TimeZone, item.TimeZone)
}

func sameText(stored, incoming *string) bool {
	if stored == nil || incoming == nil {
		return stored == nil && incoming == nil
	}
	return *stored == *incoming
}

func sameDay(stored pgtype.Date, incoming time.Time) bool {
	return stored.Valid && stored.Time.Format(time.DateOnly) == incoming.Format(time.DateOnly)
}

// hasActiveFuture reports whether any stored event is active and upcoming.
func hasActiveFuture(existing []store.Event, now time.Time, owner *time.Location) bool {
	for _, event := range existing {
		if event.Status == "active" && !startOf(event, owner).Before(now) {
			return true
		}
	}
	return false
}
