package sources

import (
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Counts summarizes a reconcile: the items received and what they changed.
type Counts struct {
	Imported  int
	Inserted  int
	Updated   int
	Removed   int
	Restored  int
	Unchanged int
}

// update rewrites a stored event from its item.
type update struct {
	id      pgtype.UUID
	item    Item
	restore bool
	// cancelledAt is kept when the event stays cancelled.
	cancelledAt pgtype.Timestamptz
	// ownReminders: the event still follows its source's reminders, so the
	// item's become its own (else the owner's stay).
	ownReminders bool
	// exceptions are the series' occurrence exceptions to write, and
	// dropExceptions those to delete, by recurrence id.
	exceptions     map[string]exceptionWrite
	dropExceptions []string
}

// exceptionWrite is one occurrence exception to store; cancelledAt keeps
// the moment of a skip that stays.
type exceptionWrite struct {
	exception   Exception
	cancelledAt pgtype.Timestamptz
}

// plan is what one refresh changes.
type plan struct {
	inserts  []Item
	updates  []update
	removals []pgtype.UUID
	counts   Counts
}

// Rules say which stored events a reconcile leaves alone.
type Rules struct {
	// keep reports whether a stored event missing from the items stays as
	// it is instead of being removed.
	keep func(store.Event) bool
	// frozen reports whether a stored event whose item changed stays as it
	// is.
	frozen func(store.Event, Item) bool
	// defaultPriority is the source's default priority (set by Reconcile):
	// an item's priority equal to it is none of its own.
	defaultPriority string
}

// FeedRules are a feed's (a sync with window upcomingOnly): feeds often
// list only upcoming dates, so an event that started before now is neither
// removed nor rewritten unless the feed moves it into the future.
func FeedRules(now time.Time, owner *time.Location) Rules {
	return Rules{
		keep: func(event store.Event) bool { return startOf(event, owner).Before(now) },
		frozen: func(event store.Event, item Item) bool {
			return startOf(event, owner).Before(now) && item.Start(owner).Before(now)
		},
	}
}

// SyncRules are a synced calendar's (such as Google's), which is authoritative for
// every event it sends, past ones included, back to windowStart, the start
// of the time window its last full listing covered: an event that is no
// longer there is removed unless it ended before windowStart (history the
// listing did not cover stays), and a changed one is always rewritten.
func SyncRules(windowStart time.Time, owner *time.Location) Rules {
	return Rules{
		keep: func(event store.Event) bool {
			end, ends := lastEnd(event, owner)
			return ends && end.Before(windowStart)
		},
		frozen: func(store.Event, Item) bool { return false },
	}
}

// reconcileItems compares a source's stored events (with the occurrence
// exceptions of its series, by event id) with its items:
//
//   - an item whose UID is new is inserted;
//   - a stored event whose item changed is updated in place, and one the
//     source had removed is restored;
//   - a stored event whose UID is missing from the items is cancelled and
//     marked removed (never deleted, never kept active), unless rules keep
//     it;
//   - a changed event that rules freeze is left as it is.
//
// It is idempotent: reconciling the same items again changes nothing.
func reconcileItems(existing []store.Event, exceptions map[[16]byte][]store.EventException, items []Item, rules Rules) plan {
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
		if rules.frozen(event, item) {
			result.counts.Unchanged++
			continue
		}
		writes, drops := exceptionChanges(exceptions[event.ID.Bytes], item.Exceptions)
		removed := event.RemovedFromSourceAt.Valid
		if !removed && matches(event, item, rules.defaultPriority) && len(writes) == 0 && len(drops) == 0 {
			result.counts.Unchanged++
			continue
		}
		change := update{id: event.ID, item: item, restore: removed, exceptions: writes, dropExceptions: drops}
		if item.Cancelled && event.Status == "cancelled" && !removed {
			change.cancelledAt = event.CancelledAt
		}
		if item.Reminders != nil {
			change.ownReminders = followsSource(event)
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
		if rules.keep(event) {
			continue // history the source no longer covers stays as it was
		}
		result.removals = append(result.removals, event.ID)
		result.counts.Removed++
	}
	return result
}

// exceptionChanges compares a series' stored occurrence exceptions with the
// item's: those to write (new or changed) and those to delete.
func exceptionChanges(stored []store.EventException, wanted map[string]Exception) (map[string]exceptionWrite, []string) {
	writes := map[string]exceptionWrite{}
	have := make(map[string]store.EventException, len(stored))
	for _, row := range stored {
		have[row.RecurrenceID] = row
	}
	for key, exception := range wanted {
		row, found := have[key]
		encoded, err := exception.Changes.JSON()
		if err != nil {
			continue
		}
		if found && row.CancelledAt.Valid == exception.Cancelled && sameChanges(row.Changes, encoded) {
			continue
		}
		write := exceptionWrite{exception: exception}
		if found && exception.Cancelled && row.CancelledAt.Valid {
			write.cancelledAt = row.CancelledAt
		}
		writes[key] = write
	}
	var drops []string
	for _, row := range stored {
		if _, keep := wanted[row.RecurrenceID]; !keep {
			drops = append(drops, row.RecurrenceID)
		}
	}
	slices.Sort(drops)
	return writes, drops
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

// endOf is a stored event's end instant; an all-day event ends at midnight
// after its last day in owner.
func endOf(event store.Event, owner *time.Location) time.Time {
	if event.AllDay {
		day := event.EndDate.Time.AddDate(0, 0, 1)
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, owner)
	}
	return event.EndAt.Time
}

// lastEnd is when a stored event ends for good: its end, or for a series the
// end of its last occurrence; ends is false for a series without an end.
func lastEnd(event store.Event, owner *time.Location) (time.Time, bool) {
	if event.RecurrenceFrequency == nil || event.RecurrenceInterval == nil {
		return endOf(event, owner), true
	}
	span := endOf(event, owner).Sub(startOf(event, owner))
	switch {
	case event.RecurrenceUntil.Valid:
		// An all-day series' until is a date in the owner's timezone; a day
		// more covers it.
		return event.RecurrenceUntil.Time.Add(span + 24*time.Hour), true
	case event.RecurrenceCount != nil:
		spec := recurrence.Spec{Frequency: *event.RecurrenceFrequency, Interval: int(*event.RecurrenceInterval), ByWeekday: event.RecurrenceByWeekday, Count: int(*event.RecurrenceCount)}
		rule, err := spec.Rule()
		if err != nil {
			return time.Time{}, false
		}
		series := recurrence.Series{Rule: rule, Zone: recurrence.LocationZone{Location: owner}}
		if event.AllDay {
			series.AllDay, series.Start = true, event.StartDate.Time
			series.Days = int(event.EndDate.Time.Sub(event.StartDate.Time).Hours()/24) + 1
		} else {
			clock := owner
			if event.TimeZone != nil {
				if loc, err := time.LoadLocation(*event.TimeZone); err == nil {
					clock = loc
				}
			}
			series.Zone, series.Start, series.Duration = recurrence.LocationZone{Location: clock}, recurrence.Wall(event.StartAt.Time, clock), span
		}
		next, err := series.Iterator()
		if err != nil {
			return time.Time{}, false
		}
		last := endOf(event, owner)
		for occurrence, ok := next(); ok; occurrence, ok = next() {
			last = occurrence.End
		}
		return last, true
	}
	return time.Time{}, false
}

// matches reports whether the stored event already says what item says
// (occurrence exceptions are compared separately); defaultPriority is the
// source's (see ownPriority).
func matches(event store.Event, item Item, defaultPriority string) bool {
	status := "active"
	if item.Cancelled {
		status = "cancelled"
	}
	if event.Title != item.Title || !sameText(event.Description, item.Description) || !sameText(event.Location, item.Location) ||
		event.AllDay != item.AllDay || event.Status != status || !sameRecurrence(event, item.Recurrence) {
		return false
	}
	if item.Reminders != nil {
		if stored, ok := sourceReminders(event); !ok || !stored.Equal(*item.Reminders) {
			return false
		}
	}
	if item.Priority != nil && !sameText(event.Priority, ownPriority(item, defaultPriority)) {
		return false
	}
	if !sameText(event.ReadOnlyReason, item.ReadOnlyReason) {
		return false
	}
	if item.AllDay {
		return sameDay(event.StartDate, item.StartDate) && sameDay(event.EndDate, item.EndDate)
	}
	return event.StartAt.Valid && event.StartAt.Time.Equal(item.StartAt) && event.EndAt.Valid && event.EndAt.Time.Equal(item.EndAt) &&
		sameText(event.TimeZone, item.TimeZone)
}

// sameRecurrence reports whether the stored event repeats by rule (or, both
// nil, does not repeat).
func sameRecurrence(event store.Event, rule *Recurrence) bool {
	if rule == nil || event.RecurrenceFrequency == nil {
		return rule == nil && event.RecurrenceFrequency == nil
	}
	count := 0
	if event.RecurrenceCount != nil {
		count = int(*event.RecurrenceCount)
	}
	sameUntil := (rule.Until == nil) == !event.RecurrenceUntil.Valid && (rule.Until == nil || rule.Until.Equal(event.RecurrenceUntil.Time))
	return *event.RecurrenceFrequency == rule.Frequency && event.RecurrenceInterval != nil && int(*event.RecurrenceInterval) == rule.Interval &&
		slices.Equal(event.RecurrenceByWeekday, rule.ByWeekday) && count == rule.Count && sameUntil
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
