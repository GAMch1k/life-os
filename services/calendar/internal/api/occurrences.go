package api

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Recurring events are stored once and expanded here, in the same order and
// with the same keyset cursors as the non-recurring rows the queries return,
// so Home, MCP, and the Calendar UI see the same occurrences. Each
// occurrence's own changes and skips (its exception) apply on the way out.

// ruleSpec is a stored recurring event's rule.
func ruleSpec(value store.Event) recurrence.Spec {
	spec := recurrence.Spec{Frequency: *value.RecurrenceFrequency, Interval: int(*value.RecurrenceInterval), ByWeekday: value.RecurrenceByWeekday, Until: nullableTime(value.RecurrenceUntil)}
	if value.RecurrenceCount != nil {
		spec.Count = int(*value.RecurrenceCount)
	}
	return spec
}

// series is a stored recurring event as a recurrence.Series. A timed event
// recurs on the wall clock of its timeZone, else the owner's; an all-day
// event's days are taken in zone (the request's), and its until is compared
// as a date, until's date in the owner's timezone, so the same days repeat
// whatever the zone.
func (h *Handler) series(value store.Event, zone *time.Location) (recurrence.Series, error) {
	if value.RecurrenceFrequency == nil || value.RecurrenceInterval == nil {
		return recurrence.Series{}, fmt.Errorf("event %x does not recur", value.ID.Bytes)
	}
	rule, err := ruleSpec(value).Rule()
	if err != nil {
		return recurrence.Series{}, err
	}
	if value.AllDay {
		if rule.Until != nil {
			last := untilDate(*rule.Until, h.ownerLocation)
			rule.Until, rule.UntilWall = nil, &last
		}
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

// recurrenceKey is an occurrence's recurrence id: its original start, an
// RFC 3339 instant in UTC for a timed series, a date for an all-day one.
func recurrenceKey(occurrence recurrence.Occurrence, allDay bool) string {
	if allDay {
		return occurrence.Wall.Format(time.DateOnly)
	}
	return occurrence.Start.UTC().Format(time.RFC3339)
}

// canonicalKey is rid in the form recurrenceKey gives (any RFC 3339 offset
// is accepted for a timed series), and its moment: the instant, or the date
// at midnight UTC. ok is false when rid is no recurrence id of that kind.
func canonicalKey(rid string, allDay bool) (string, time.Time, bool) {
	if allDay {
		day, err := time.Parse(time.DateOnly, rid)
		if err != nil {
			return "", time.Time{}, false
		}
		return day.Format(time.DateOnly), day, true
	}
	instant, err := time.Parse(time.RFC3339, rid)
	if err != nil {
		return "", time.Time{}, false
	}
	instant = instant.UTC().Truncate(time.Second)
	return instant.Format(time.RFC3339), instant, true
}

// keyMoment is what an occurrence's recurrence id names: its start instant
// (to the second) or its first day.
func keyMoment(occurrence recurrence.Occurrence, allDay bool) time.Time {
	if allDay {
		return occurrence.Wall
	}
	return occurrence.Start.UTC().Truncate(time.Second)
}

// expansion is one recurring event ready to be expanded: its series, its
// rendered form, and its exceptions by recurrence id.
type expansion struct {
	row              store.Event
	series           recurrence.Series
	base             generated.CalendarEvent
	id               [16]byte
	exceptions       map[string]*exception
	includeCancelled bool
}

func (h *Handler) expand(row store.Event, zone *time.Location, exceptions map[string]*exception, includeCancelled bool) (expansion, error) {
	series, err := h.series(row, zone)
	if err != nil {
		return expansion{}, err
	}
	base, err := event(row)
	if err != nil {
		return expansion{}, err
	}
	return expansion{row: row, series: series, base: base, id: row.ID.Bytes, exceptions: exceptions, includeCancelled: includeCancelled}, nil
}

// loadExpansion expands one recurring event with its stored exceptions, its
// all-day days taken in the owner's timezone.
func (h *Handler) loadExpansion(ctx context.Context, queries *store.Queries, row store.Event) (expansion, error) {
	rows, err := queries.ListEventExceptions(ctx, []pgtype.UUID{row.ID})
	if err != nil {
		return expansion{}, err
	}
	grouped, err := exceptionsBySeries(rows)
	if err != nil {
		return expansion{}, err
	}
	return h.expand(row, h.ownerLocation, grouped[row.ID.Bytes], true)
}

// seriesOccurrence is one occurrence of an expansion: its recurrence id, its
// original times, its times after its own changes (at), and its exception.
type seriesOccurrence struct {
	key     string
	natural recurrence.Occurrence
	at      recurrence.Occurrence
	ex      *exception
}

// moved reports whether the occurrence has its own start and end.
func (o seriesOccurrence) moved() bool { return o.ex != nil && o.ex.changes.moves() }

// visible reports whether listing shows the occurrence: a skipped one only
// when cancelled events are asked for.
func (e expansion) visible(ex *exception) bool {
	return e.includeCancelled || ex == nil || ex.cancelledAt == nil
}

func (e expansion) occurrence(natural recurrence.Occurrence) seriesOccurrence {
	key := recurrenceKey(natural, e.series.AllDay)
	o := seriesOccurrence{key: key, natural: natural, at: natural, ex: e.exceptions[key]}
	if o.moved() {
		o.at = e.movedTimes(natural, o.ex.changes)
	}
	return o
}

// movedTimes are an occurrence's times after its own move: an all-day move
// is a pair of dates, taken in the expansion's zone like the series' days.
func (e expansion) movedTimes(natural recurrence.Occurrence, changes occurrenceChanges) recurrence.Occurrence {
	if e.series.AllDay {
		if changes.StartDate == nil || changes.EndDate == nil {
			return natural
		}
		first, last := changes.StartDate.Time, changes.EndDate.Time
		return recurrence.Occurrence{Wall: first, LastDay: last, Start: e.series.Zone.Instant(first), End: e.series.Zone.Instant(last.AddDate(0, 0, 1))}
	}
	if changes.StartAt == nil || changes.EndAt == nil {
		return natural
	}
	return recurrence.Occurrence{Wall: natural.Wall, Start: changes.StartAt.UTC(), End: changes.EndAt.UTC()}
}

// naturalAt is the occurrence whose recurrence id is key, as the series
// would place it; whether the rule produces it is not checked here (writes
// check it, and series changes remove exceptions that lose their
// occurrence).
func (e expansion) naturalAt(key string) (recurrence.Occurrence, bool) {
	_, moment, ok := canonicalKey(key, e.series.AllDay)
	if !ok {
		return recurrence.Occurrence{}, false
	}
	if e.series.AllDay {
		return e.series.At(moment), true
	}
	return recurrence.Occurrence{Start: moment, End: moment.Add(e.series.Duration)}, true
}

func overlaps(occurrence recurrence.Occurrence, from, to *time.Time) bool {
	return (to == nil || occurrence.Start.Before(*to)) && (from == nil || occurrence.End.After(*from))
}

// walk visits, in order, the visible occurrences that keep their original
// time and overlap the optional bounds, until visit returns false. Moved
// occurrences come from movedWithin.
func (e expansion) walk(from, to *time.Time, visit func(seriesOccurrence) bool) error {
	next, err := e.series.Iterator()
	if err != nil {
		return err
	}
	for natural, ok := next(); ok; natural, ok = next() {
		if to != nil && !natural.Start.Before(*to) {
			return nil
		}
		if from != nil && !natural.End.After(*from) {
			continue
		}
		o := e.occurrence(natural)
		if o.moved() || !e.visible(o.ex) {
			continue
		}
		if !visit(o) {
			return nil
		}
	}
	return nil
}

// movedWithin returns the visible moved occurrences whose new times overlap
// the optional bounds, wherever they were originally, by recurrence id.
func (e expansion) movedWithin(from, to *time.Time) []seriesOccurrence {
	keys := make([]string, 0, len(e.exceptions))
	for key, ex := range e.exceptions {
		if ex.changes.moves() && e.visible(ex) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out []seriesOccurrence
	for _, key := range keys {
		natural, ok := e.naturalAt(key)
		if !ok {
			continue
		}
		ex := e.exceptions[key]
		o := seriesOccurrence{key: key, natural: natural, at: e.movedTimes(natural, ex.changes), ex: ex}
		if overlaps(o.at, from, to) {
			out = append(out, o)
		}
	}
	return out
}

// located is an occurrence found by its recurrence id: its place in the
// series (index 0 is the first) and the occurrence before it, if any.
type located struct {
	occurrence seriesOccurrence
	index      int
	previous   *recurrence.Occurrence
}

// find looks rid up among the series' occurrences (skipped and moved ones
// included); ok is false when rid is not one of them.
func (e expansion) find(rid string) (located, bool, error) {
	key, moment, ok := canonicalKey(rid, e.series.AllDay)
	if !ok {
		return located{}, false, nil
	}
	next, err := e.series.Iterator()
	if err != nil {
		return located{}, false, err
	}
	var previous *recurrence.Occurrence
	for index := 0; ; index++ {
		natural, ok := next()
		if !ok {
			return located{}, false, nil
		}
		at := keyMoment(natural, e.series.AllDay)
		if at.After(moment) {
			return located{}, false, nil
		}
		if at.Equal(moment) && recurrenceKey(natural, e.series.AllDay) == key {
			found := e.occurrence(natural)
			return located{occurrence: found, index: index, previous: previous}, true, nil
		}
		current := natural
		previous = &current
	}
}

// existing reports which of keys are recurrence ids of the series'
// occurrences, iterating no further than the latest of them.
func (e expansion) existing(keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	wanted := map[string]bool{}
	var latest time.Time
	for _, key := range keys {
		canonical, moment, ok := canonicalKey(key, e.series.AllDay)
		if !ok || canonical != key {
			continue
		}
		wanted[key] = true
		if moment.After(latest) {
			latest = moment
		}
	}
	if len(wanted) == 0 {
		return out, nil
	}
	next, err := e.series.Iterator()
	if err != nil {
		return nil, err
	}
	for natural, ok := next(); ok; natural, ok = next() {
		if keyMoment(natural, e.series.AllDay).After(latest) {
			break
		}
		if key := recurrenceKey(natural, e.series.AllDay); wanted[key] {
			out[key] = true
		}
	}
	return out, nil
}

// item is the listed form of an occurrence: the recurring event with the
// occurrence's times, its own changes and skip, and occurrence set.
func (e expansion) item(o seriesOccurrence) generated.CalendarEvent {
	item := e.base
	if e.series.AllDay {
		first, last := types.Date{Time: o.at.Wall}, types.Date{Time: o.at.LastDay}
		item.StartDate, item.EndDate = &first, &last
	} else {
		start, end := o.at.Start.UTC(), o.at.End.UTC()
		item.StartAt, item.EndAt = &start, &end
	}
	modified := false
	if o.ex != nil {
		changes := o.ex.changes
		modified = !changes.empty()
		if changes.Title != nil {
			item.Title = *changes.Title
		}
		if changes.Description.set {
			item.Description = changes.Description.value
		}
		if changes.Location.set {
			item.Location = changes.Location.value
		}
		if changes.Reminders != nil {
			item.Reminders = *changes.Reminders
		}
		if changes.UseDefaultReminders != nil {
			item.UseDefaultReminders = *changes.UseDefaultReminders
		}
		if changes.Priority != nil {
			item.Priority = *changes.Priority
		}
		if o.ex.cancelledAt != nil && item.Status == generated.Active {
			cancelledAt := *o.ex.cancelledAt
			item.Status, item.CancelledAt = generated.Cancelled, &cancelledAt
		}
	}
	item.Occurrence = &generated.EventOccurrence{SeriesId: e.base.Id, RecurrenceId: o.key, Modified: modified}
	return item
}

// naturalFields is the occurrence as its series has it, without its own
// changes: the series' fields with the occurrence's original times and no
// rule.
func (e expansion) naturalFields(o seriesOccurrence) (eventFields, error) {
	fields, err := fieldsFromStore(e.row)
	if err != nil {
		return eventFields{}, err
	}
	if e.series.AllDay {
		first, last := types.Date{Time: o.natural.Wall}, types.Date{Time: o.natural.LastDay}
		fields.StartDate, fields.EndDate = &first, &last
	} else {
		start, end := o.natural.Start.UTC(), o.natural.End.UTC()
		fields.StartAt, fields.EndAt = &start, &end
	}
	fields.Recurrence = nil
	fields.Status, fields.CancelledAt = generated.Active, nil
	return fields, nil
}

// fields is the occurrence with its own changes; Status is its own skip,
// whatever the series' status.
func (e expansion) fields(o seriesOccurrence) (eventFields, error) {
	fields, err := e.naturalFields(o)
	if err != nil {
		return eventFields{}, err
	}
	if o.ex == nil {
		return fields, nil
	}
	fields = o.ex.changes.applyTo(fields)
	if o.ex.cancelledAt != nil {
		cancelledAt := *o.ex.cancelledAt
		fields.Status, fields.CancelledAt = generated.Cancelled, &cancelledAt
	}
	return fields, nil
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
// [from, to) that come after the cursor position (if any), placed by their
// times after their own changes. Per event it takes at most limit of the
// occurrences that keep their time (they come in order, so later ones cannot
// make the page) and every moved one.
func (h *Handler) listOccurrences(rows []store.ListRecurringEventsRow, exceptions map[[16]byte]map[string]*exception, from, to time.Time, zone *time.Location, includeCancelled bool, after *listPosition, limit int) ([]listed, error) {
	var out []listed
	for _, row := range rows {
		e, err := h.expand(row.Event, zone, exceptions[row.Event.ID.Bytes], includeCancelled)
		if err != nil {
			return nil, err
		}
		passes := func(start time.Time) bool {
			return after == nil || listedBefore(listed{startsAt: after.StartsAt, id: after.ID}, listed{startsAt: start, id: e.id})
		}
		count := 0
		err = e.walk(&from, &to, func(o seriesOccurrence) bool {
			if !passes(o.at.Start) {
				return true
			}
			out = append(out, listed{startsAt: o.at.Start, id: e.id, item: e.item(o)})
			count++
			return count < limit
		})
		if err != nil {
			return nil, err
		}
		for _, o := range e.movedWithin(&from, &to) {
			if passes(o.at.Start) {
				out = append(out, listed{startsAt: o.at.Start, id: e.id, item: e.item(o)})
			}
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
func rank(occurrence recurrence.Occurrence, anchor time.Time) (int32, int64) {
	if occurrence.End.After(anchor) {
		return 0, occurrence.Start.UnixMicro()
	}
	return 1, -occurrence.Start.UnixMicro()
}

// searchOccurrences turns matching recurring events into occurrences: every
// occurrence in [from, to) when both bounds are given, otherwise one per
// event, its next occurrence not over at anchor (or its last one if it has
// ended), within whichever bound is given. Occurrences are placed by their
// times after their own changes; skipped ones are passed over unless
// includeCancelled.
func (h *Handler) searchOccurrences(rows []store.SearchRecurringEventsRow, exceptions map[[16]byte]map[string]*exception, from, to *time.Time, zone *time.Location, includeCancelled bool, anchor time.Time, after *searchPosition, limit int) ([]ranked, error) {
	var out []ranked
	for _, row := range rows {
		e, err := h.expand(row.Event, zone, exceptions[row.Event.ID.Bytes], includeCancelled)
		if err != nil {
			return nil, err
		}
		place := func(o seriesOccurrence) ranked {
			bucket, key := rank(o.at, anchor)
			return ranked{relevance: row.Relevance, bucket: bucket, sortKey: key, id: e.id}
		}
		afterCursor := func(o seriesOccurrence) bool {
			if after == nil {
				return true
			}
			cursor := ranked{relevance: after.Relevance, bucket: after.Bucket, sortKey: after.SortKey, id: after.ID}
			return rankedBefore(cursor, place(o))
		}
		var occurrences []seriesOccurrence
		if from != nil && to != nil {
			occurrences, err = bestInRange(e, *from, *to, limit, afterCursor, func(a, b seriesOccurrence) bool { return rankedBefore(place(a), place(b)) })
			if err != nil {
				return nil, err
			}
		} else if o, ok, err := representative(e, from, to, anchor); err != nil {
			return nil, err
		} else if ok && afterCursor(o) {
			occurrences = append(occurrences, o)
		}
		for _, o := range occurrences {
			item := place(o)
			item.item = e.item(o)
			out = append(out, item)
		}
	}
	return out, nil
}

// bestInRange returns the first limit visible occurrences overlapping
// [from, to) that keep accepts, in the order before defines. Search order is
// not start order (past occurrences come most recent first), so every
// occurrence in the range is considered, keeping only the best limit.
func bestInRange(e expansion, from, to time.Time, limit int, keep func(seriesOccurrence) bool, before func(a, b seriesOccurrence) bool) ([]seriesOccurrence, error) {
	best := make([]seriesOccurrence, 0, limit+1)
	consider := func(o seriesOccurrence) {
		if !keep(o) || (len(best) == limit && !before(o, best[limit-1])) {
			return
		}
		position := sort.Search(len(best), func(index int) bool { return before(o, best[index]) })
		best = append(best, seriesOccurrence{})
		copy(best[position+1:], best[position:])
		best[position] = o
		if len(best) > limit {
			best = best[:limit]
		}
	}
	err := e.walk(&from, &to, func(o seriesOccurrence) bool {
		consider(o)
		return true
	})
	if err != nil {
		return nil, err
	}
	for _, o := range e.movedWithin(&from, &to) {
		consider(o)
	}
	return best, nil
}

// representative is a series' next visible occurrence not over at anchor,
// else its last one, within the optional bounds, by its times after its own
// changes.
func representative(e expansion, from, to *time.Time, anchor time.Time) (seriesOccurrence, bool, error) {
	var upcoming, past *seriesOccurrence
	consider := func(o seriesOccurrence) {
		if o.at.End.After(anchor) {
			if upcoming == nil || o.at.Start.Before(upcoming.at.Start) {
				upcoming = &o
			}
		} else if past == nil || o.at.Start.After(past.at.Start) {
			past = &o
		}
	}
	err := e.walk(from, to, func(o seriesOccurrence) bool {
		consider(o)
		return !o.at.End.After(anchor)
	})
	if err != nil {
		return seriesOccurrence{}, false, err
	}
	for _, o := range e.movedWithin(from, to) {
		consider(o)
	}
	switch {
	case upcoming != nil:
		return *upcoming, true, nil
	case past != nil:
		return *past, true, nil
	}
	return seriesOccurrence{}, false, nil
}

func sortRanked(items []ranked) {
	sort.SliceStable(items, func(a, b int) bool { return rankedBefore(items[a], items[b]) })
}

// recurringExceptions loads the exceptions of the given recurring events.
func (h *Handler) recurringExceptions(ctx context.Context, ids []pgtype.UUID) (map[[16]byte]map[string]*exception, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := h.queries.ListEventExceptions(ctx, ids)
	if err != nil {
		return nil, err
	}
	return exceptionsBySeries(rows)
}
