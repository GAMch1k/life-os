package api

import (
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func weekdays(days ...generated.Weekday) *[]generated.Weekday { return &days }

// The rule is validated against the event it repeats.
func TestRecurrenceValidation(t *testing.T) {
	owner := berlin(t)
	weekly := func(change func(*generated.Recurrence)) func(*eventFields) {
		return func(f *eventFields) {
			rule := generated.Recurrence{Frequency: generated.Weekly, Interval: 1}
			change(&rule)
			f.Recurrence = &rule
		}
	}
	cases := []struct {
		name   string
		base   func() eventFields
		change func(*eventFields)
		want   string // "" means valid
	}{
		// The timed event starts on Tuesday 17 November, 06:00 Berlin.
		{"weekly on its day and Thursday", timedFields, weekly(func(r *generated.Recurrence) { r.ByWeekday = weekdays(generated.TH, generated.TU) }), ""},
		{"no weekdays", timedFields, weekly(func(r *generated.Recurrence) { r.ByWeekday = weekdays() }), ""},
		{"weekdays without the start's", timedFields, weekly(func(r *generated.Recurrence) { r.ByWeekday = weekdays(generated.MO, generated.WE) }),
			"recurrence byWeekday must include the weekday the event starts on (Tuesday)"},
		{"weekdays on a daily rule", timedFields, weekly(func(r *generated.Recurrence) { r.Frequency, r.ByWeekday = generated.Daily, weekdays(generated.TU) }),
			"recurrence byWeekday is only for weekly recurrence"},
		{"an unknown weekday", timedFields, weekly(func(r *generated.Recurrence) { r.ByWeekday = weekdays("TU", "XX") }),
			"recurrence byWeekday days must be MO, TU, WE, TH, FR, SA, or SU"},
		{"count", timedFields, weekly(func(r *generated.Recurrence) { r.Count = ptr(10) }), ""},
		{"count zero", timedFields, weekly(func(r *generated.Recurrence) { r.Count = ptr(0) }), "recurrence count must be between 1 and 1000"},
		{"count too large", timedFields, weekly(func(r *generated.Recurrence) { r.Count = ptr(1001) }), "recurrence count must be between 1 and 1000"},
		{"count and until", timedFields, weekly(func(r *generated.Recurrence) { r.Count, r.Until = ptr(3), instant("2027-01-01T00:00:00Z") }),
			"recurrence takes until or count, not both"},
		{"until at the start", timedFields, weekly(func(r *generated.Recurrence) { r.Until = instant("2026-11-17T05:00:00Z") }), ""},
		{"until before the start", timedFields, weekly(func(r *generated.Recurrence) { r.Until = instant("2026-11-17T04:59:59Z") }),
			"recurrence until must not be before the event starts"},
		// 23:30 UTC on Tuesday is Wednesday in the event's zone.
		{"the start's weekday in the event's zone", func() eventFields {
			f := timedFields()
			f.StartAt, f.EndAt, f.TimeZone = instant("2026-11-17T23:30:00Z"), instant("2026-11-18T00:30:00Z"), ptr("Europe/Berlin")
			return f
		}, weekly(func(r *generated.Recurrence) { r.ByWeekday = weekdays(generated.TU) }), "(Wednesday)"},
		// The all-day event starts on Thursday 24 December; its until is
		// compared as the owner's date.
		{"all-day on its weekday", allDayFields, weekly(func(r *generated.Recurrence) { r.ByWeekday = weekdays(generated.TH) }), ""},
		{"all-day until on its first day", allDayFields, weekly(func(r *generated.Recurrence) { r.Until = instant("2026-12-23T23:00:00Z") }), ""},
		{"all-day until the day before", allDayFields, weekly(func(r *generated.Recurrence) { r.Until = instant("2026-12-23T22:59:59Z") }),
			"recurrence until must not be before the event starts"},
		{"yearly", allDayFields, func(f *eventFields) { f.Recurrence = &generated.Recurrence{Frequency: generated.Yearly, Interval: 1} }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := tc.base()
			tc.change(&fields)
			err := fields.validate(owner)
			if tc.want == "" && err != nil {
				t.Fatalf("validate() = %v, want valid", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
	// Weekdays are stored in week order, each once.
	rule := recurrenceColumns(&generated.Recurrence{Frequency: generated.Weekly, Interval: 1, ByWeekday: weekdays(generated.TH, generated.TU, generated.TH), Count: ptr(4)})
	if strings.Join(rule.byWeekday, ",") != "TU,TH" || *rule.count != 4 {
		t.Fatalf("columns = %+v", rule)
	}
}

// An occurrence's changes keep explicit nulls apart from absent fields.
func TestOccurrenceChangesRoundTrip(t *testing.T) {
	changes := occurrenceChanges{
		Title: ptr("Gym with Anna"), Description: optionalText{set: true}, Location: optionalText{set: true, value: ptr("Park")},
		StartAt: instant("2026-10-17T07:00:00Z"), EndAt: instant("2026-10-17T08:00:00Z"),
		Reminders: &[]generated.Reminder{{Kind: generated.Relative, MinutesBefore: ptr(15)}}, UseDefaultReminders: ptr(false),
		Priority: ptr(generated.High),
	}
	raw, err := changes.encode()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"title":"Gym with Anna","description":null,"location":"Park","startAt":"2026-10-17T07:00:00Z","endAt":"2026-10-17T08:00:00Z","reminders":[{"kind":"relative","minutesBefore":15}],"useDefaultReminders":false,"priority":"high"}`; string(raw) != want {
		t.Fatalf("encoded %s\nwant    %s", raw, want)
	}
	back, err := decodeChanges(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Description.set || back.Description.value != nil || *back.Location.value != "Park" || !back.moves() || back.empty() || len(*back.Reminders) != 1 ||
		*back.Priority != generated.High {
		t.Fatalf("decoded %+v", back)
	}
	if only, err := decodeChanges([]byte(`{"priority":"low"}`)); err != nil || only.empty() || *only.Priority != generated.Low {
		t.Fatalf("a priority alone = %+v, %v", only, err)
	}
	if empty, _ := decodeChanges([]byte(`{}`)); !empty.empty() || empty.moves() {
		t.Fatalf("empty changes = %+v", empty)
	}
	if _, err := decodeChanges([]byte(`{"startAt":"2026-10-17T07:00:00Z"}`)); err == nil {
		t.Fatal("a move without its end decoded")
	}
	days, _ := occurrenceChanges{StartDate: day("2026-10-18"), EndDate: day("2026-10-18")}.encode()
	if back, err := decodeChanges(days); err != nil || back.StartDate.String() != "2026-10-18" || !back.moves() {
		t.Fatalf("all-day move = %s %+v %v", days, back, err)
	}
}

// What an occurrence overrides is what differs from its series; a field set
// back to the series' value is no change of its own.
func TestDiffOccurrence(t *testing.T) {
	natural := timedFields()
	natural.Description, natural.Reminders = ptr("Bring card"), []generated.Reminder{}
	next := natural
	if changes := diffOccurrence(natural, next, defaultPriority); !changes.empty() {
		t.Fatalf("no change = %+v", changes)
	}
	next.Title, next.Description = "Dentist (late)", nil
	next.StartAt, next.EndAt = instant("2026-11-17T07:00:00+01:00"), natural.EndAt
	changes := diffOccurrence(natural, next, defaultPriority)
	if *changes.Title != "Dentist (late)" || !changes.Description.set || changes.Description.value != nil || changes.Location.set {
		t.Fatalf("changes = %+v", changes)
	}
	// A move overrides both ends, so a later change of the series' length
	// does not stretch the moved occurrence.
	if !changes.StartAt.Equal(*next.StartAt) || !changes.EndAt.Equal(*natural.EndAt) {
		t.Fatalf("move = %v .. %v", changes.StartAt, changes.EndAt)
	}
	applied := changes.applyTo(natural)
	if applied.Title != "Dentist (late)" || applied.Description != nil || !applied.StartAt.Equal(*next.StartAt) {
		t.Fatalf("applied = %+v", applied)
	}
	// A split replaces the occurrence's own changes to the fields it sets.
	rest := changes.without(map[string]bool{"title": true, "startAt": true})
	if rest.Title != nil || rest.moves() || !rest.Description.set {
		t.Fatalf("without title and times = %+v", rest)
	}
}

// An occurrence's own priority is one that differs from the series' as it
// applies (a series without its own has the Life OS default, medium).
func TestDiffOccurrencePriority(t *testing.T) {
	natural := timedFields()
	natural.Reminders = []generated.Reminder{}
	next := natural
	next.Priority = ptr(generated.Medium)
	if changes := diffOccurrence(natural, next, defaultPriority); !changes.empty() {
		t.Fatalf("medium on a series without its own = %+v, want no change", changes)
	}
	next.Priority = ptr(generated.High)
	changes := diffOccurrence(natural, next, defaultPriority)
	if changes.Priority == nil || *changes.Priority != generated.High || changes.Title != nil {
		t.Fatalf("high = %+v", changes)
	}
	if applied := changes.applyTo(natural); *applied.Priority != generated.High {
		t.Fatalf("applied = %+v", applied.Priority)
	}
	// On a high series, medium is the occurrence's own and high is none.
	natural.Priority = ptr(generated.High)
	next.Priority = ptr(generated.Medium)
	if changes := diffOccurrence(natural, next, defaultPriority); changes.Priority == nil || *changes.Priority != generated.Medium {
		t.Fatalf("medium on a high series = %+v", changes)
	}
	next.Priority = ptr(generated.High)
	if changes := diffOccurrence(natural, next, defaultPriority); !changes.empty() {
		t.Fatalf("high on a high series = %+v, want no change", changes)
	}
	if rest := changes.without(map[string]bool{"priority": true}); rest.Priority != nil {
		t.Fatalf("without priority = %+v", rest)
	}
}

// seriesRow is a stored recurring event for expansion tests.
func seriesRow(fields eventFields) store.Event {
	params, err := fields.createParams()
	if err != nil {
		panic(err)
	}
	return store.Event{
		ID: pgtype.UUID{Bytes: googleuuid.New(), Valid: true}, Title: params.Title, AllDay: params.AllDay,
		StartAt: params.StartAt, EndAt: params.EndAt, StartDate: params.StartDate, EndDate: params.EndDate, TimeZone: params.TimeZone,
		RecurrenceFrequency: params.RecurrenceFrequency, RecurrenceInterval: params.RecurrenceInterval, RecurrenceUntil: params.RecurrenceUntil,
		RecurrenceByWeekday: params.RecurrenceByWeekday, RecurrenceCount: params.RecurrenceCount,
		Reminders: params.Reminders, UseDefaultReminders: params.UseDefaultReminders, Status: params.Status, SourceKind: params.SourceKind,
	}
}

// keys lists the recurrence ids of a series' occurrences overlapping the
// window, with all-day days taken in zone.
func keys(t *testing.T, h *Handler, row store.Event, zone *time.Location) []string {
	t.Helper()
	e, err := h.expand(row, zone, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	from, to := *instant("2025-01-01T00:00:00Z"), *instant("2029-01-01T00:00:00Z")
	var out []string
	if err := e.walk(&from, &to, func(o seriesOccurrence) bool { out = append(out, o.key); return true }); err != nil {
		t.Fatal(err)
	}
	return out
}

// Splitting a series before an occurrence shares its occurrences out
// exactly: the ended series keeps those before, the new one the rest, also
// across daylight saving changes, for count rules, and for all-day series
// read in another timezone.
func TestSplitSharesOccurrencesOut(t *testing.T) {
	owner := berlin(t)
	h := NewHandler(nil, "Europe/Berlin")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	honolulu, _ := time.LoadLocation("Pacific/Honolulu")
	cases := []struct {
		name   string
		fields eventFields
		split  string
	}{
		{"weekly 09:00 Berlin across the end of summer time", eventFields{Title: "Planning", StartAt: instant("2026-10-05T07:00:00Z"), EndAt: instant("2026-10-05T08:00:00Z"),
			TimeZone: ptr("Europe/Berlin"), Recurrence: &generated.Recurrence{Frequency: generated.Weekly, Interval: 1, Until: instant("2027-06-01T00:00:00Z")}}, "2026-11-02T08:00:00Z"},
		{"weekly on Monday and Thursday, 10 times", eventFields{Title: "Gym", StartAt: instant("2026-10-19T05:00:00Z"), EndAt: instant("2026-10-19T06:00:00Z"),
			Recurrence: &generated.Recurrence{Frequency: generated.Weekly, Interval: 1, ByWeekday: weekdays(generated.MO, generated.TH), Count: ptr(10)}}, "2026-10-29T06:00:00Z"},
		{"monthly on the 31st, 5 times", eventFields{Title: "Rent", StartAt: instant("2027-01-31T09:00:00Z"), EndAt: instant("2027-01-31T09:30:00Z"),
			Recurrence: &generated.Recurrence{Frequency: generated.Monthly, Interval: 1, Count: ptr(5)}}, "2027-05-31T08:00:00Z"},
		{"all-day daily until a date", eventFields{Title: "Trip", AllDay: true, StartDate: day("2026-10-10"), EndDate: day("2026-10-10"),
			Recurrence: &generated.Recurrence{Frequency: generated.Daily, Interval: 1, Until: instant("2026-10-20T12:00:00Z")}}, "2026-10-15"},
		{"all-day yearly birthday", eventFields{Title: "Anna", AllDay: true, StartDate: day("2020-09-17"), EndDate: day("2020-09-17"),
			Recurrence: &generated.Recurrence{Frequency: generated.Yearly, Interval: 1}}, "2027-09-17"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fields.Status, tc.fields.Reminders, tc.fields.UseDefaultReminders = generated.Active, []generated.Reminder{}, true
			if err := tc.fields.validate(owner); err != nil {
				t.Fatal(err)
			}
			row := seriesRow(tc.fields)
			e, err := h.expand(row, owner, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			found, ok, err := e.find(tc.split)
			if err != nil || !ok || found.index == 0 || found.previous == nil {
				t.Fatalf("find %s = %+v %v %v", tc.split, found, ok, err)
			}
			ended, started, err := h.splitAt(e, found)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []eventFields{ended, started} {
				if err := part.validate(owner); err != nil {
					t.Fatalf("split part invalid: %v", err)
				}
			}
			if ended.Recurrence.Count != nil || ended.Recurrence.Until == nil {
				t.Fatalf("ended rule = %+v", ended.Recurrence)
			}
			if tc.fields.Recurrence.Count != nil && *started.Recurrence.Count != *tc.fields.Recurrence.Count-found.index {
				t.Fatalf("started count = %d, want %d", *started.Recurrence.Count, *tc.fields.Recurrence.Count-found.index)
			}
			for _, zone := range []*time.Location{owner, tokyo, honolulu} {
				all := keys(t, h, row, zone)
				before, after := keys(t, h, seriesRow(ended), zone), keys(t, h, seriesRow(started), zone)
				if before[len(before)-1] != recurrenceKey(*found.previous, row.AllDay) || after[0] != tc.split {
					t.Fatalf("%s: ended %v, started %v", zone, before, after)
				}
				if strings.Join(append(before, after...), " ") != strings.Join(all, " ") {
					t.Fatalf("%s: split\n %v + %v\nwant %v", zone, before, after, all)
				}
			}
		})
	}
}

// Listing applies each occurrence's own changes: a skipped occurrence is
// left out unless asked for, a moved one is listed at its new time (also
// when it moved in from outside the range), and search's representative
// passes a skipped occurrence over.
func TestExpansionAppliesExceptions(t *testing.T) {
	owner := berlin(t)
	h := NewHandler(nil, "Europe/Berlin")
	fields := eventFields{Title: "Gym", StartAt: instant("2026-10-03T05:00:00Z"), EndAt: instant("2026-10-03T06:00:00Z"), TimeZone: ptr("Europe/Berlin"),
		Recurrence: &generated.Recurrence{Frequency: generated.Weekly, Interval: 1}, Status: generated.Active, Reminders: []generated.Reminder{}, UseDefaultReminders: true}
	row := seriesRow(fields)
	cancelledAt := *instant("2026-10-09T10:00:00Z")
	exceptions := map[string]*exception{
		// Saturday 10 October is skipped.
		"2026-10-10T05:00:00Z": {key: "2026-10-10T05:00:00Z", cancelledAt: &cancelledAt},
		// 17 October moves to Sunday 18 October 09:00 and gets a title.
		"2026-10-17T05:00:00Z": {key: "2026-10-17T05:00:00Z", changes: occurrenceChanges{Title: ptr("Gym with Anna"), StartAt: instant("2026-10-18T07:00:00Z"), EndAt: instant("2026-10-18T08:00:00Z")}},
		// 7 November (after the window) moves into it, on 12 October.
		"2026-11-07T06:00:00Z": {key: "2026-11-07T06:00:00Z", changes: occurrenceChanges{StartAt: instant("2026-10-12T16:00:00Z"), EndAt: instant("2026-10-12T17:00:00Z")}},
		// 24 October moves out of the window.
		"2026-10-24T05:00:00Z": {key: "2026-10-24T05:00:00Z", changes: occurrenceChanges{StartAt: instant("2026-12-24T16:00:00Z"), EndAt: instant("2026-12-24T17:00:00Z")}},
	}
	rows := []store.ListRecurringEventsRow{{Event: row}}
	grouped := map[[16]byte]map[string]*exception{row.ID.Bytes: exceptions}
	from, to := *instant("2026-10-05T00:00:00Z"), *instant("2026-10-26T00:00:00Z")
	list := func(includeCancelled bool) []string {
		items, err := h.listOccurrences(rows, grouped, from, to, owner, includeCancelled, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		sortListed(items)
		var out []string
		for _, entry := range items {
			item := entry.item
			label := item.Title + " " + item.StartAt.Format(time.RFC3339) + " " + item.Occurrence.RecurrenceId
			if item.Occurrence.Modified {
				label += " modified"
			}
			if item.Status == generated.Cancelled {
				label += " cancelled"
			}
			out = append(out, label)
		}
		return out
	}
	equal(t, "listing", list(false), []string{
		"Gym 2026-10-12T16:00:00Z 2026-11-07T06:00:00Z modified",
		"Gym with Anna 2026-10-18T07:00:00Z 2026-10-17T05:00:00Z modified",
	})
	equal(t, "with cancelled", list(true), []string{
		"Gym 2026-10-10T05:00:00Z 2026-10-10T05:00:00Z cancelled",
		"Gym 2026-10-12T16:00:00Z 2026-11-07T06:00:00Z modified",
		"Gym with Anna 2026-10-18T07:00:00Z 2026-10-17T05:00:00Z modified",
	})

	// Now is Saturday 10 October 12:00 Berlin: today's occurrence is over and
	// skipped anyway, so the next is the moved one on 12 October.
	e, err := h.expand(row, owner, exceptions, false)
	if err != nil {
		t.Fatal(err)
	}
	next, ok, err := representative(e, nil, nil, fixedNow)
	if err != nil || !ok || next.key != "2026-11-07T06:00:00Z" {
		t.Fatalf("representative = %+v %v %v", next, ok, err)
	}
	// Today's occurrence, as "cancel gym today" searches it, is skipped.
	dayStart, dayEnd := *instant("2026-10-09T22:00:00Z"), *instant("2026-10-10T22:00:00Z")
	if today, ok, _ := representative(e, &dayStart, &dayEnd, fixedNow); ok {
		t.Fatalf("today's skipped occurrence represented: %+v", today)
	}
	e.includeCancelled = true
	if today, ok, _ := representative(e, &dayStart, &dayEnd, fixedNow); !ok || today.key != "2026-10-10T05:00:00Z" || e.item(today).Status != generated.Cancelled {
		t.Fatalf("today with cancelled = %+v %v", today, ok)
	}
	// Recurrence ids are found whatever their offset; others are not.
	if found, ok, _ := e.find("2026-10-17T07:00:00+02:00"); !ok || found.index != 2 || found.occurrence.at.Start.Format(time.RFC3339) != "2026-10-18T07:00:00Z" {
		t.Fatalf("find by offset = %+v %v", found, ok)
	}
	for _, rid := range []string{"2026-10-17T05:30:00Z", "2026-10-17", "2026-09-26T05:00:00Z", "soon"} {
		if _, ok, _ := e.find(rid); ok {
			t.Errorf("%s found", rid)
		}
	}
	exists, err := e.existing([]string{"2026-10-17T05:00:00Z", "2026-10-17T06:00:00Z", "2027-01-02T06:00:00Z"})
	if err != nil || !exists["2026-10-17T05:00:00Z"] || exists["2026-10-17T06:00:00Z"] || !exists["2027-01-02T06:00:00Z"] {
		t.Fatalf("existing = %v %v", exists, err)
	}
}
