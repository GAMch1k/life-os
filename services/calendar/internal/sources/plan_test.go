package sources

import (
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

var (
	berlin, _ = time.LoadLocation("Europe/Berlin")
	// Saturday 2026-10-10 12:00 Berlin.
	now = time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)
)

func ptr[T any](value T) *T { return &value }

func timedItem(uid, title, start string) ics.Item {
	begin, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	return ics.Item{ExternalUID: uid, Title: title, StartAt: begin, EndAt: begin.Add(time.Hour), TimeZone: ptr("Europe/Berlin")}
}

// stored is the event a previous refresh wrote for item.
func stored(item ics.Item) store.Event {
	uid := item.ExternalUID
	event := store.Event{
		ID: pgtype.UUID{Bytes: googleuuid.New(), Valid: true}, Title: item.Title, Description: item.Description, Location: item.Location,
		AllDay: item.AllDay, TimeZone: item.TimeZone, Status: "active", SourceKind: "ics", ExternalUid: &uid,
	}
	if item.AllDay {
		event.StartDate, event.EndDate = pgtype.Date{Time: item.StartDate, Valid: true}, pgtype.Date{Time: item.EndDate, Valid: true}
	} else {
		event.StartAt, event.EndAt = at(item.StartAt), at(item.EndAt)
	}
	if item.Cancelled {
		event.Status, event.CancelledAt = "cancelled", at(now.Add(-time.Hour))
	}
	return event
}

func removed(event store.Event) store.Event {
	event.Status, event.CancelledAt, event.RemovedFromSourceAt = "cancelled", at(now.Add(-24*time.Hour)), at(now.Add(-24*time.Hour))
	return event
}

func summary(p plan) string {
	var parts []string
	for _, item := range p.inserts {
		parts = append(parts, "insert "+item.ExternalUID)
	}
	for _, change := range p.updates {
		verb := "update "
		if change.restore {
			verb = "restore "
		}
		parts = append(parts, verb+change.item.ExternalUID)
	}
	for range p.removals {
		parts = append(parts, "remove")
	}
	return strings.Join(parts, ", ")
}

func TestReconcileTransitions(t *testing.T) {
	bio := timedItem("bio", "Bioabfall", "2026-10-19T04:00:00Z")
	paper := timedItem("paper", "Papier", "2026-10-15T04:00:00Z")
	pastPaper := timedItem("paper-old", "Papier", "2026-10-01T04:00:00Z")

	// Initial import: everything is new, past events included.
	first := reconcile(nil, []ics.Item{bio, paper, pastPaper}, now, berlin)
	if summary(first) != "insert bio, insert paper, insert paper-old" || first.counts.Inserted != 3 || first.counts.Imported != 3 {
		t.Fatalf("initial: %s %+v", summary(first), first.counts)
	}
	existing := []store.Event{stored(bio), stored(paper), stored(pastPaper)}

	// The same feed again changes nothing.
	same := reconcile(existing, []ics.Item{bio, paper, pastPaper}, now, berlin)
	if summary(same) != "" || same.counts.Unchanged != 3 {
		t.Fatalf("identical refresh: %s %+v", summary(same), same.counts)
	}

	// A moved pickup updates in place; a new description too.
	moved := bio
	moved.StartAt, moved.EndAt = moved.StartAt.Add(24*time.Hour), moved.EndAt.Add(24*time.Hour)
	described := paper
	described.Description = ptr("Tonnen und Container")
	changed := reconcile(existing, []ics.Item{moved, described, pastPaper}, now, berlin)
	if summary(changed) != "update bio, update paper" || changed.counts.Updated != 2 || changed.counts.Unchanged != 1 {
		t.Fatalf("changed: %s %+v", summary(changed), changed.counts)
	}

	// A future event missing from the feed is removed; the past one that
	// dropped out (feeds list only upcoming dates) stays as it was.
	gone := reconcile(existing, []ics.Item{bio}, now, berlin)
	if summary(gone) != "remove" || gone.removals[0] != existing[1].ID || gone.counts.Removed != 1 {
		t.Fatalf("removal: %s %+v", summary(gone), gone.counts)
	}

	// A removed event whose UID comes back is restored; one still missing
	// stays removed without being touched again.
	afterRemoval := []store.Event{stored(bio), removed(stored(paper)), stored(pastPaper)}
	back := reconcile(afterRemoval, []ics.Item{bio, paper}, now, berlin)
	if summary(back) != "restore paper" || back.counts.Restored != 1 || !back.updates[0].restore {
		t.Fatalf("restore: %s %+v", summary(back), back.counts)
	}
	if still := reconcile(afterRemoval, []ics.Item{bio}, now, berlin); summary(still) != "" {
		t.Fatalf("an already removed event is removed again: %s", summary(still))
	}

	// Past events are not rewritten by the feed, unless it moves them into
	// the future.
	editedPast := pastPaper
	editedPast.Title = "Papier (alt)"
	if past := reconcile(existing, []ics.Item{bio, paper, editedPast}, now, berlin); summary(past) != "" {
		t.Fatalf("past event rewritten: %s", summary(past))
	}
	rescheduled := pastPaper
	rescheduled.StartAt, rescheduled.EndAt = paper.StartAt.Add(48*time.Hour), paper.EndAt.Add(48*time.Hour)
	if future := reconcile(existing, []ics.Item{bio, paper, rescheduled}, now, berlin); summary(future) != "update paper-old" {
		t.Fatalf("past event moved into the future: %s", summary(future))
	}
}

func TestReconcileCancellationsAndAllDay(t *testing.T) {
	pickup := timedItem("pickup", "Gelber Sack", "2026-10-22T04:00:00Z")
	cancelled := pickup
	cancelled.Cancelled = true
	// The feed cancels an event (STATUS:CANCELLED): an update to cancelled,
	// not a removal.
	p := reconcile([]store.Event{stored(pickup)}, []ics.Item{cancelled}, now, berlin)
	if summary(p) != "update pickup" || p.updates[0].cancelledAt.Valid {
		t.Fatalf("feed cancellation: %s %+v", summary(p), p.updates)
	}
	// Still cancelled next time: unchanged, its cancellation moment kept.
	if again := reconcile([]store.Event{stored(cancelled)}, []ics.Item{cancelled}, now, berlin); summary(again) != "" {
		t.Fatalf("cancelled twice: %s", summary(again))
	}
	retitled := cancelled
	retitled.Title = "Gelber Sack (entfällt)"
	if kept := reconcile([]store.Event{stored(cancelled)}, []ics.Item{retitled}, now, berlin); !kept.updates[0].cancelledAt.Valid {
		t.Fatalf("a changed cancelled event loses its cancellation moment: %+v", kept.updates)
	}

	// All-day events count as past from midnight of their first day in the
	// owner's zone: today's is under way and left alone.
	day := func(uid, date string) ics.Item {
		value, _ := time.Parse(time.DateOnly, date)
		return ics.Item{ExternalUID: uid, Title: "Holiday", AllDay: true, StartDate: value, EndDate: value}
	}
	today, tomorrow := day("today", "2026-10-10"), day("tomorrow", "2026-10-11")
	p = reconcile([]store.Event{stored(today), stored(tomorrow)}, nil, now, berlin)
	if summary(p) != "remove" || p.removals[0] == stored(today).ID {
		t.Fatalf("all-day removal: %s", summary(p))
	}
	moved := tomorrow
	moved.EndDate = moved.EndDate.AddDate(0, 0, 1)
	if longer := reconcile([]store.Event{stored(tomorrow)}, []ics.Item{moved}, now, berlin); summary(longer) != "update tomorrow" {
		t.Fatalf("all-day change: %s", summary(longer))
	}
	if !hasActiveFuture([]store.Event{stored(tomorrow)}, now, berlin) || hasActiveFuture([]store.Event{stored(today), removed(stored(tomorrow))}, now, berlin) {
		t.Error("hasActiveFuture")
	}
}

func TestScheduleAndBackoff(t *testing.T) {
	schedule, err := ParseSchedule("04:30", berlin)
	if err != nil {
		t.Fatal(err)
	}
	// 04:30 CEST is 02:30 UTC; after the change on 25 October, 03:30 UTC.
	for after, want := range map[string]string{
		"2026-10-10T10:00:00Z": "2026-10-11T02:30:00Z",
		"2026-10-11T02:29:00Z": "2026-10-11T02:30:00Z",
		"2026-10-11T02:30:00Z": "2026-10-12T02:30:00Z",
		"2026-10-24T12:00:00Z": "2026-10-25T03:30:00Z",
	} {
		from, _ := time.Parse(time.RFC3339, after)
		if got := schedule.Next(from).Format(time.RFC3339); got != want {
			t.Errorf("Next(%s) = %s, want %s", after, got, want)
		}
	}
	for failures, want := range map[int]time.Duration{1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 5: 4 * time.Hour, 6: 6 * time.Hour, 20: 6 * time.Hour} {
		if got := backoff(failures); got != want {
			t.Errorf("backoff(%d) = %v, want %v", failures, got, want)
		}
	}
	// Never later than the next daily slot.
	late := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	if got := schedule.retryAt(late, 10); !got.Equal(time.Date(2026, 10, 11, 2, 30, 0, 0, time.UTC)) {
		t.Errorf("retryAt = %v", got)
	}
	if got := schedule.retryAt(now, 1); !got.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("first retry = %v", got)
	}
	for _, bad := range []string{"4:30", "24:00", "04:60", "", "noon"} {
		if _, err := ParseSchedule(bad, berlin); err == nil {
			t.Errorf("ParseSchedule(%q) accepted", bad)
		}
	}
}

func TestSourceActor(t *testing.T) {
	for name, want := range map[string]string{
		"ZAW": "ics-zaw", "Müllabfuhr Großstadt": "ics-muellabfuhr-grossstadt", "  Public holidays (DE) ": "ics-public-holidays-de",
		"日本": "ics-source", strings.Repeat("a", 80): "ics-" + strings.Repeat("a", 40),
	} {
		if got := SourceActor(name); got.Kind != "system" || got.Name != want {
			t.Errorf("SourceActor(%q) = %+v, want system:%s", name, got, want)
		}
	}
}

// seriesItem is a weekly series (such as Google's) with one skipped occurrence.
func seriesItem() Item {
	item := Item{Item: timedItem("standup", "Standup", "2026-10-12T07:00:00Z"), Recurrence: &Recurrence{Frequency: "weekly", Interval: 1, ByWeekday: []string{"MO", "WE"}}}
	item.Reminders = &Reminders{UseDefault: true, Reminders: []Reminder{}}
	item.Exceptions = map[string]Exception{"2026-10-14T07:00:00Z": {Cancelled: true}}
	return item
}

// storedSeries is what a sync wrote for item: the event and its exceptions.
func storedSeries(item Item) (store.Event, []store.EventException) {
	event := stored(item.Item)
	event.SourceKind = "external"
	frequency, interval := item.Recurrence.Frequency, int32(item.Recurrence.Interval)
	event.RecurrenceFrequency, event.RecurrenceInterval, event.RecurrenceByWeekday = &frequency, &interval, item.Recurrence.ByWeekday
	own, whole, _ := item.Reminders.encode()
	event.Reminders, event.UseDefaultReminders, event.SourceReminders = own, item.Reminders.UseDefault, whole
	var exceptions []store.EventException
	for key, exception := range item.Exceptions {
		changes, _ := exception.Changes.JSON()
		row := store.EventException{EventID: event.ID, RecurrenceID: key, Changes: changes}
		if exception.Cancelled {
			row.CancelledAt = at(now.Add(-time.Hour))
		}
		exceptions = append(exceptions, row)
	}
	return event, exceptions
}

func TestReconcileSeriesExceptionsAndSourceReminders(t *testing.T) {
	item := seriesItem()
	event, exceptions := storedSeries(item)
	grouped := map[[16]byte][]store.EventException{event.ID.Bytes: exceptions}
	rules := SyncRules(now.AddDate(-1, 0, 0), berlin)

	if same := reconcileItems([]store.Event{event}, grouped, []Item{item}, rules); summary(same) != "" || same.counts.Unchanged != 1 {
		t.Fatalf("identical series: %s", summary(same))
	}
	// A new skip and a changed occurrence are written; a skip that went is
	// dropped; the skip that stays keeps its moment.
	changed := seriesItem()
	title := "Standup (planning)"
	changed.Exceptions = map[string]Exception{"2026-10-19T07:00:00Z": {Changes: Changes{Title: &title}}, "2026-10-21T07:00:00Z": {Cancelled: true}}
	p := reconcileItems([]store.Event{event}, grouped, []Item{changed}, rules)
	if summary(p) != "update standup" || len(p.updates[0].exceptions) != 2 || strings.Join(p.updates[0].dropExceptions, ",") != "2026-10-14T07:00:00Z" {
		t.Fatalf("exceptions: %s %+v", summary(p), p.updates)
	}
	kept := seriesItem()
	kept.Exceptions["2026-10-21T07:00:00Z"] = Exception{Cancelled: true}
	p = reconcileItems([]store.Event{event}, grouped, []Item{kept}, rules)
	if len(p.updates) != 1 || len(p.updates[0].exceptions) != 1 || p.updates[0].exceptions["2026-10-21T07:00:00Z"].cancelledAt.Valid {
		t.Fatalf("a new skip: %+v", p.updates)
	}
	// A changed rule is an update; so is a series becoming a single event.
	every2 := seriesItem()
	every2.Recurrence = &Recurrence{Frequency: "weekly", Interval: 2, ByWeekday: []string{"MO", "WE"}}
	if p := reconcileItems([]store.Event{event}, grouped, []Item{every2}, rules); summary(p) != "update standup" {
		t.Fatalf("rule change: %s", summary(p))
	}
	single := seriesItem()
	single.Recurrence, single.Exceptions = nil, nil
	if p := reconcileItems([]store.Event{event}, grouped, []Item{single}, rules); summary(p) != "update standup" || len(p.updates[0].dropExceptions) != 1 {
		t.Fatalf("no longer recurring: %s", summary(p))
	}

	// Google changes the reminders: the event follows while the owner has
	// not changed its own, and keeps the owner's once they have.
	reminded := seriesItem()
	reminded.Reminders = &Reminders{UseDefault: false, Reminders: []Reminder{Relative(15)}}
	p = reconcileItems([]store.Event{event}, grouped, []Item{reminded}, rules)
	if summary(p) != "update standup" || !p.updates[0].ownReminders {
		t.Fatalf("source reminders: %s %+v", summary(p), p.updates)
	}
	overridden := event
	overridden.Reminders, overridden.UseDefaultReminders = []byte(`[{"kind":"relative","minutesBefore":120}]`), true
	p = reconcileItems([]store.Event{overridden}, grouped, []Item{reminded}, rules)
	if summary(p) != "update standup" || p.updates[0].ownReminders {
		t.Fatalf("owner's reminders: %s %+v", summary(p), p.updates)
	}
	// The owner's own reminders alone change nothing.
	if p := reconcileItems([]store.Event{overridden}, grouped, []Item{item}, rules); summary(p) != "" {
		t.Fatalf("owner's reminders only: %s", summary(p))
	}
}

func TestSyncRulesRemoveWhatTheWindowCovers(t *testing.T) {
	windowStart := now.AddDate(-1, 0, 0)
	rules := SyncRules(windowStart, berlin)
	recent := stored(timedItem("recent", "Party", "2026-05-02T17:00:00Z"))
	ancient := stored(timedItem("ancient", "Old party", "2025-05-02T17:00:00Z"))
	series, _ := storedSeries(seriesItem())
	series.StartAt, series.EndAt = at(time.Date(2020, 1, 6, 8, 0, 0, 0, time.UTC)), at(time.Date(2020, 1, 6, 8, 15, 0, 0, time.UTC))
	endedUntil := series
	endedUntil.ID = pgtype.UUID{Bytes: googleuuid.New(), Valid: true}
	endedUntil.ExternalUid = ptr("ended")
	endedUntil.RecurrenceUntil = at(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	endedCount := series
	endedCount.ID = pgtype.UUID{Bytes: googleuuid.New(), Valid: true}
	endedCount.ExternalUid = ptr("counted")
	endedCount.RecurrenceCount = ptr(int32(5))
	// Missing from the listing: removed when the window covers them, past
	// or not (a series running on is removed too); history from before the
	// window stays.
	p := reconcileItems([]store.Event{recent, ancient, series, endedUntil, endedCount}, nil, nil, rules)
	if p.counts.Removed != 2 || p.removals[0] != recent.ID || p.removals[1] != series.ID {
		t.Fatalf("removals: %+v", p.counts)
	}
	// Past events are rewritten when the source changes them.
	renamed := timedItem("recent", "Spring party", "2026-05-02T17:00:00Z")
	if p := reconcileItems([]store.Event{recent}, nil, []Item{{Item: renamed}}, rules); summary(p) != "update recent" {
		t.Fatalf("past rename: %s", summary(p))
	}
}
