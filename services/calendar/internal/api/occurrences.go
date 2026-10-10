package api

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Recurring events are stored once and expanded here, in the same order and
// with the same keyset cursors as the non-recurring rows the queries return,
// so Home, MCP, and the Calendar UI see the same occurrences.

// series is a stored recurring event as a recurrence.Series. A timed event
// recurs on the wall clock of its timeZone, else the owner's; an all-day
// event's days are taken in zone (the request's), as for listing.
func (h *Handler) series(value store.Event, zone *time.Location) (recurrence.Series, error) {
	if value.RecurrenceFrequency == nil || value.RecurrenceInterval == nil {
		return recurrence.Series{}, fmt.Errorf("event %x does not recur", value.ID.Bytes)
	}
	rule, err := recurrence.SimpleRule(*value.RecurrenceFrequency, int(*value.RecurrenceInterval), nullableTime(value.RecurrenceUntil))
	if err != nil {
		return recurrence.Series{}, err
	}
	if value.AllDay {
		days := int(value.EndDate.Time.Sub(value.StartDate.Time).Hours()/24) + 1
		return recurrence.Series{Rule: rule, AllDay: true, Start: value.StartDate.Time, Days: max(days, 1), Zone: recurrence.LocationZone{Location: zone}}, nil
	}
	clock := h.ownerLocation
	if value.TimeZone != nil {
		if loc, err := time.LoadLocation(*value.TimeZone); err == nil {
			clock = loc
		}
	}
	return recurrence.Series{
		Rule: rule, Start: recurrence.Wall(value.StartAt.Time, clock), Duration: value.EndAt.Time.Sub(value.StartAt.Time),
		Zone: recurrence.LocationZone{Location: clock},
	}, nil
}

// occurrenceItem is the listed form of one occurrence: the recurring event
// with the occurrence's start and end, and occurrence set.
func occurrenceItem(base generated.CalendarEvent, occurrence recurrence.Occurrence) generated.CalendarEvent {
	item := base
	key := occurrence.Start.UTC().Format(time.RFC3339)
	if base.AllDay {
		first, last := types.Date{Time: occurrence.Wall}, types.Date{Time: occurrence.LastDay}
		item.StartDate, item.EndDate = &first, &last
		key = occurrence.Wall.Format(time.DateOnly)
	} else {
		start, end := occurrence.Start.UTC(), occurrence.End.UTC()
		item.StartAt, item.EndAt = &start, &end
	}
	item.Occurrence = &generated.EventOccurrence{SeriesId: base.Id, RecurrenceId: key}
	return item
}

// listed is an item in list order: start instant, then id.
type listed struct {
	startsAt time.Time
	id       [16]byte
	item     generated.CalendarEvent
}

func listedBefore(a, b listed) bool {
	if !a.startsAt.Equal(b.startsAt) {
		return a.startsAt.Before(b.startsAt)
	}
	return bytes.Compare(a.id[:], b.id[:]) < 0
}

// listOccurrences expands recurring events into the occurrences overlapping
// [from, to) that come after the cursor position (if any), at most limit per
// event.
func (h *Handler) listOccurrences(rows []store.ListRecurringEventsRow, from, to time.Time, zone *time.Location, after *listPosition, limit int) ([]listed, error) {
	var out []listed
	for _, row := range rows {
		series, err := h.series(row.Event, zone)
		if err != nil {
			return nil, err
		}
		base, err := event(row.Event)
		if err != nil {
			return nil, err
		}
		id := row.Event.ID.Bytes
		occurrences, err := series.Between(from, to, limit, func(occurrence recurrence.Occurrence) bool {
			return after == nil || listedBefore(listed{startsAt: after.StartsAt, id: after.ID}, listed{startsAt: occurrence.Start, id: id})
		})
		if err != nil {
			return nil, err
		}
		for _, occurrence := range occurrences {
			out = append(out, listed{startsAt: occurrence.Start, id: id, item: occurrenceItem(base, occurrence)})
		}
	}
	return out, nil
}

func sortListed(items []listed) {
	sort.SliceStable(items, func(a, b int) bool { return listedBefore(items[a], items[b]) })
}

// ranked is an item in search order: relevance (descending), then upcoming
// before past (bucket), then sort key, then id.
type ranked struct {
	relevance int32
	bucket    int32
	sortKey   int64
	id        [16]byte
	item      generated.CalendarEvent
}

func rankedBefore(a, b ranked) bool {
	switch {
	case a.relevance != b.relevance:
		return a.relevance > b.relevance
	case a.bucket != b.bucket:
		return a.bucket < b.bucket
	case a.sortKey != b.sortKey:
		return a.sortKey < b.sortKey
	}
	return bytes.Compare(a.id[:], b.id[:]) < 0
}

// rank places an occurrence in search order the way SearchEvents places
// rows: upcoming (not over at anchor) soonest first, then past most recent
// first.
func rank(relevance int32, occurrence recurrence.Occurrence, anchor time.Time) (int32, int64) {
	if occurrence.End.After(anchor) {
		return 0, occurrence.Start.UnixMicro()
	}
	return 1, -occurrence.Start.UnixMicro()
}

// searchOccurrences turns matching recurring events into occurrences: every
// occurrence in [from, to) when both bounds are given, otherwise one per
// event, its next occurrence not over at anchor (or its last one if it has
// ended), within whichever bound is given.
func (h *Handler) searchOccurrences(rows []store.SearchRecurringEventsRow, from, to *time.Time, zone *time.Location, anchor time.Time, after *searchPosition, limit int) ([]ranked, error) {
	var out []ranked
	for _, row := range rows {
		series, err := h.series(row.Event, zone)
		if err != nil {
			return nil, err
		}
		base, err := event(row.Event)
		if err != nil {
			return nil, err
		}
		id := row.Event.ID.Bytes
		place := func(occurrence recurrence.Occurrence) ranked {
			bucket, key := rank(row.Relevance, occurrence, anchor)
			return ranked{relevance: row.Relevance, bucket: bucket, sortKey: key, id: id}
		}
		afterCursor := func(occurrence recurrence.Occurrence) bool {
			if after == nil {
				return true
			}
			cursor := ranked{relevance: after.Relevance, bucket: after.Bucket, sortKey: after.SortKey, id: after.ID}
			return rankedBefore(cursor, place(occurrence))
		}
		var occurrences []recurrence.Occurrence
		if from != nil && to != nil {
			occurrences, err = bestInRange(series, *from, *to, limit, afterCursor, func(a, b recurrence.Occurrence) bool { return rankedBefore(place(a), place(b)) })
			if err != nil {
				return nil, err
			}
		} else if occurrence, ok, err := representative(series, from, to, anchor); err != nil {
			return nil, err
		} else if ok && afterCursor(occurrence) {
			occurrences = append(occurrences, occurrence)
		}
		for _, occurrence := range occurrences {
			item := place(occurrence)
			item.item = occurrenceItem(base, occurrence)
			out = append(out, item)
		}
	}
	return out, nil
}

// bestInRange returns the first limit occurrences overlapping [from, to)
// that keep accepts, in the order before defines. Search order is not start
// order (past occurrences come most recent first), so every occurrence in
// the range is considered, keeping only the best limit.
func bestInRange(series recurrence.Series, from, to time.Time, limit int, keep func(recurrence.Occurrence) bool, before func(a, b recurrence.Occurrence) bool) ([]recurrence.Occurrence, error) {
	next, err := series.Iterator()
	if err != nil {
		return nil, err
	}
	best := make([]recurrence.Occurrence, 0, limit+1)
	for occurrence, ok := next(); ok; occurrence, ok = next() {
		if !occurrence.Start.Before(to) {
			break
		}
		if !occurrence.End.After(from) || !keep(occurrence) {
			continue
		}
		if len(best) == limit && !before(occurrence, best[limit-1]) {
			continue
		}
		position := sort.Search(len(best), func(index int) bool { return before(occurrence, best[index]) })
		best = append(best, recurrence.Occurrence{})
		copy(best[position+1:], best[position:])
		best[position] = occurrence
		if len(best) > limit {
			best = best[:limit]
		}
	}
	return best, nil
}

// representative is a series' next occurrence not over at anchor, else its
// last one, within the optional bounds.
func representative(series recurrence.Series, from, to *time.Time, anchor time.Time) (recurrence.Occurrence, bool, error) {
	next, err := series.Iterator()
	if err != nil {
		return recurrence.Occurrence{}, false, err
	}
	var last recurrence.Occurrence
	found := false
	for occurrence, ok := next(); ok; occurrence, ok = next() {
		if to != nil && !occurrence.Start.Before(*to) {
			break
		}
		if from != nil && !occurrence.End.After(*from) {
			continue
		}
		if occurrence.End.After(anchor) {
			return occurrence, true, nil
		}
		last, found = occurrence, true
	}
	return last, found, nil
}

func sortRanked(items []ranked) {
	sort.SliceStable(items, func(a, b int) bool { return rankedBefore(items[a], items[b]) })
}
