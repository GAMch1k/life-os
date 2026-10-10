package api

import (
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

func ptr[T any](value T) *T { return &value }

func day(value string) *types.Date {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		panic(err)
	}
	return &types.Date{Time: parsed}
}

func instant(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return &parsed
}

func timedFields() eventFields {
	return eventFields{Title: "Dentist", StartAt: instant("2026-11-17T06:00:00+01:00"), EndAt: instant("2026-11-17T07:00:00+01:00"), Status: generated.Active}
}

func allDayFields() eventFields {
	return eventFields{Title: "Holiday", AllDay: true, StartDate: day("2026-12-24"), EndDate: day("2026-12-26"), Status: generated.Active}
}

func TestEventValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*eventFields)
		base   func() eventFields
		want   string // "" means valid
	}{
		{"timed", func(*eventFields) {}, timedFields, ""},
		{"all-day", func(*eventFields) {}, allDayFields, ""},
		{"one-day all-day", func(f *eventFields) { f.EndDate = f.StartDate }, allDayFields, ""},
		{"timed in a zone", func(f *eventFields) { f.TimeZone = ptr("Europe/Berlin") }, timedFields, ""},
		{"blank title", func(f *eventFields) { f.Title = "  " }, timedFields, "title"},
		{"end before start", func(f *eventFields) { f.EndAt = f.StartAt }, timedFields, "endAt must be after startAt"},
		{"timed with dates", func(f *eventFields) { f.StartDate = day("2026-11-17") }, timedFields, "timed events require startAt and endAt only"},
		{"timed without end", func(f *eventFields) { f.EndAt = nil }, timedFields, "timed events require"},
		{"all-day with instants", func(f *eventFields) { f.StartAt = instant("2026-12-24T00:00:00Z") }, allDayFields, "all-day events require"},
		{"all-day ending before it starts", func(f *eventFields) { f.EndDate = day("2026-12-23") }, allDayFields, "endDate must not precede startDate"},
		{"all-day with a zone", func(f *eventFields) { f.TimeZone = ptr("Europe/Berlin") }, allDayFields, "all-day events take no timeZone"},
		{"unknown zone", func(f *eventFields) { f.TimeZone = ptr("Mars/Olympus") }, timedFields, "IANA"},
		{"process-local zone", func(f *eventFields) { f.TimeZone = ptr("Local") }, timedFields, "IANA"},
		{"bad recurrence frequency", func(f *eventFields) { f.Recurrence = &generated.Recurrence{Frequency: "hourly", Interval: 1} }, timedFields, "frequency"},
		{"bad recurrence interval", func(f *eventFields) { f.Recurrence = &generated.Recurrence{Frequency: generated.Weekly} }, timedFields, "interval"},
		{"bad status", func(f *eventFields) { f.Status = "deleted" }, timedFields, "status"},
		{"too many reminders", func(f *eventFields) {
			reminders := make([]generated.Reminder, maxReminders+1)
			for index := range reminders {
				reminders[index] = generated.Reminder{Kind: generated.Relative, MinutesBefore: ptr(index)}
			}
			f.Reminders = reminders
		}, timedFields, "at most 10 reminders"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := tc.base()
			tc.change(&fields)
			err := fields.validate()
			if tc.want == "" && err != nil {
				t.Fatalf("validate() = %v, want valid", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestReminderValidation(t *testing.T) {
	valid := []generated.Reminder{
		{Kind: generated.Relative, MinutesBefore: ptr(0)},
		{Kind: generated.Relative, MinutesBefore: ptr(40320)},
		// The waste pickup case: the evening before, owner-local.
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("19:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(0), LocalTime: ptr("00:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(28), LocalTime: ptr("23:59")},
	}
	for _, reminder := range valid {
		if err := validateReminder(reminder); err != nil {
			t.Errorf("%+v: %v", reminder, err)
		}
	}
	invalid := []generated.Reminder{
		{Kind: generated.Relative},
		{Kind: generated.Relative, MinutesBefore: ptr(-1)},
		{Kind: generated.Relative, MinutesBefore: ptr(40321)},
		{Kind: generated.Relative, MinutesBefore: ptr(30), LocalTime: ptr("19:00")},
		{Kind: generated.LocalTime, LocalTime: ptr("19:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(1)},
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("19:00"), MinutesBefore: ptr(5)},
		{Kind: generated.LocalTime, DaysBefore: ptr(29), LocalTime: ptr("19:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(-1), LocalTime: ptr("19:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("7:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("24:00")},
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("19:60")},
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("19:00:00")},
		{Kind: "absolute"},
	}
	for _, reminder := range invalid {
		if err := validateReminder(reminder); err == nil {
			t.Errorf("%+v: valid, want an error", reminder)
		}
	}
}

func TestRemindersStoredFormRoundTrips(t *testing.T) {
	if raw, err := encodeReminders(nil); err != nil || raw != nil {
		t.Fatalf("nil reminders stored as %q, %v; want NULL (follow the defaults)", raw, err)
	}
	empty, err := encodeReminders(&[]generated.Reminder{})
	if err != nil || string(empty) != "[]" {
		t.Fatalf("no reminders stored as %q, %v; want []", empty, err)
	}
	if decoded, err := decodeReminders(empty); err != nil || decoded == nil || len(*decoded) != 0 {
		t.Fatalf("[] decoded as %v, %v; want an explicit empty list", decoded, err)
	}
	reminders := []generated.Reminder{
		{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("19:00")},
		{Kind: generated.Relative, MinutesBefore: ptr(30)},
	}
	raw, err := encodeReminders(&reminders)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"},{"kind":"relative","minutesBefore":30}]`; string(raw) != want {
		t.Fatalf("stored %s, want %s", raw, want)
	}
	decoded, err := decodeReminders(raw)
	if err != nil || len(*decoded) != 2 || *(*decoded)[0].LocalTime != "19:00" || *(*decoded)[1].MinutesBefore != 30 {
		t.Fatalf("decoded %+v, %v", decoded, err)
	}
}

func TestPatchDecoding(t *testing.T) {
	for body, want := range map[string]string{
		`not json`:                   "invalid event request",
		`[]`:                         "invalid event request",
		`null`:                       "invalid event request",
		`{}`:                         "names no event field",
		`{"source":{"kind":"ics"}}`:  "names no event field",
		`{"title":null}`:             "title cannot be null",
		`{"allDay":null}`:            "allDay cannot be null",
		`{"status":null}`:            "status cannot be null",
		`{"startAt":"tomorrow"}`:     "invalid event request",
		`{"reminders":{"kind":"x"}}`: "invalid event request",
	} {
		if _, err := decodePatch([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}

func mustPatch(t *testing.T, body string) eventPatch {
	t.Helper()
	patch, err := decodePatch([]byte(body))
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return patch
}

func TestPatchChangesOnlyNamedFieldsAndNullClears(t *testing.T) {
	current := timedFields()
	current.Description, current.Location, current.TimeZone = ptr("Bring card"), ptr("Main St 1"), ptr("Europe/Berlin")
	current.Reminders = []generated.Reminder{{Kind: generated.Relative, MinutesBefore: ptr(30)}}
	current.UseDefaultReminders = false
	current.Recurrence = &generated.Recurrence{Frequency: generated.Yearly, Interval: 1}

	next, err := mustPatch(t, `{"title":"Dentist (moved)","description":null,"recurrence":null,"reminders":[]}`).apply(current, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if next.Title != "Dentist (moved)" || next.Description != nil || next.Recurrence != nil {
		t.Errorf("named fields not applied: %+v", next)
	}
	if next.Reminders == nil || len(next.Reminders) != 0 || next.UseDefaultReminders {
		t.Errorf("reminders = %v (defaults %v), want none of its own and the defaults still off", next.Reminders, next.UseDefaultReminders)
	}
	if next.Location == nil || *next.Location != "Main St 1" || next.TimeZone == nil || !next.StartAt.Equal(*current.StartAt) {
		t.Errorf("unnamed fields changed: %+v", next)
	}

	next, err = mustPatch(t, `{"reminders":null}`).apply(current, time.Now())
	if err != nil || next.Reminders == nil || len(next.Reminders) != 0 || !next.UseDefaultReminders {
		t.Errorf("reminders:null = %v (defaults %v), %v; want back to the defaults alone", next.Reminders, next.UseDefaultReminders, err)
	}
	next, err = mustPatch(t, `{"reminders":null,"useDefaultReminders":false}`).apply(current, time.Now())
	if err != nil || len(next.Reminders) != 0 || next.UseDefaultReminders {
		t.Errorf("reminders:null with the defaults off = %v (defaults %v), %v; want no reminders at all", next.Reminders, next.UseDefaultReminders, err)
	}
}

func TestPatchTurnsDefaultRemindersOnAndOffAndKeepsOwn(t *testing.T) {
	current := timedFields()
	current.Reminders = []generated.Reminder{{Kind: generated.Relative, MinutesBefore: ptr(180)}}
	current.UseDefaultReminders = true

	next, err := mustPatch(t, `{"useDefaultReminders":false}`).apply(current, time.Now())
	if err != nil || next.UseDefaultReminders || len(next.Reminders) != 1 {
		t.Fatalf("defaults off = %+v, %v; want own reminders kept", next, err)
	}
	next, err = mustPatch(t, `{"useDefaultReminders":true,"reminders":[{"kind":"relative","minutesBefore":30},{"kind":"relative","minutesBefore":1440}]}`).apply(next, time.Now())
	if err != nil || !next.UseDefaultReminders || len(next.Reminders) != 2 {
		t.Fatalf("defaults on with two own = %+v, %v", next, err)
	}
	if _, err := decodePatch([]byte(`{"useDefaultReminders":null}`)); err == nil || !strings.Contains(err.Error(), "useDefaultReminders cannot be null") {
		t.Errorf("useDefaultReminders:null = %v, want refused", err)
	}
	for body, want := range map[string]bool{
		`{"reminders":[]}`:                                               true,
		`{"useDefaultReminders":false}`:                                  true,
		`{"reminders":null,"useDefaultReminders":true}`:                  true,
		`{"reminders":[],"title":"Moved"}`:                               false,
		`{"useDefaultReminders":false,"startAt":"2026-11-17T09:00:00Z"}`: false,
	} {
		if got := mustPatch(t, body).onlyReminders(); got != want {
			t.Errorf("onlyReminders(%s) = %v, want %v", body, got, want)
		}
	}
}

func TestEffectiveRemindersAddOwnToDefaultsOnce(t *testing.T) {
	dayBefore := generated.Reminder{Kind: generated.LocalTime, DaysBefore: ptr(1), LocalTime: ptr("19:00")}
	threeHours := generated.Reminder{Kind: generated.Relative, MinutesBefore: ptr(180)}
	halfHour := generated.Reminder{Kind: generated.Relative, MinutesBefore: ptr(30)}
	defaults := []generated.Reminder{dayBefore, halfHour}

	got := effectiveReminders(true, defaults, []generated.Reminder{threeHours, {Kind: generated.Relative, MinutesBefore: ptr(30)}})
	want := []generated.Reminder{dayBefore, halfHour, threeHours}
	if len(got) != len(want) {
		t.Fatalf("effective = %d reminders, want %d (the repeated 30 minutes once): %+v", len(got), len(want), got)
	}
	for index := range want {
		if !sameReminder(got[index], want[index]) {
			t.Errorf("effective[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
	if got := effectiveReminders(false, defaults, []generated.Reminder{threeHours}); len(got) != 1 || !sameReminder(got[0], threeHours) {
		t.Errorf("defaults off = %+v, want only the event's own", got)
	}
	if got := effectiveReminders(false, defaults, []generated.Reminder{}); got == nil || len(got) != 0 {
		t.Errorf("nothing = %+v, want an empty list", got)
	}
}

func TestPatchSwitchesBetweenTimedAndAllDay(t *testing.T) {
	current := timedFields()
	current.TimeZone = ptr("Europe/Berlin")
	next, err := mustPatch(t, `{"allDay":true,"startDate":"2026-11-17","endDate":"2026-11-17"}`).apply(current, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !next.AllDay || next.StartAt != nil || next.EndAt != nil || next.TimeZone != nil || next.StartDate.String() != "2026-11-17" {
		t.Fatalf("to all-day: %+v", next)
	}
	// Without the new kind's dates the switch is incomplete.
	if _, err := mustPatch(t, `{"allDay":true}`).apply(current, time.Now()); err == nil {
		t.Fatal("switch to all-day without dates was accepted")
	}
	back, err := mustPatch(t, `{"allDay":false,"startAt":"2026-11-17T09:00:00Z","endAt":"2026-11-17T10:00:00Z"}`).apply(next, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if back.AllDay || back.StartDate != nil || back.EndDate != nil || !back.StartAt.Equal(*instant("2026-11-17T09:00:00Z")) {
		t.Fatalf("back to timed: %+v", back)
	}
	// Without a switch, the other kind's fields are refused, not dropped.
	if _, err := mustPatch(t, `{"startDate":"2026-11-18"}`).apply(current, time.Now()); err == nil {
		t.Fatal("a date on a timed event was accepted")
	}
	// Moving the start past the end is refused: the result is validated.
	if _, err := mustPatch(t, `{"startAt":"2026-11-18T09:00:00Z"}`).apply(current, time.Now()); err == nil {
		t.Fatal("start after end was accepted")
	}
}

func TestPatchCancelsAndRestores(t *testing.T) {
	now := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	cancelled, err := mustPatch(t, `{"status":"cancelled"}`).apply(timedFields(), now)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != generated.Cancelled || cancelled.CancelledAt == nil || !cancelled.CancelledAt.Equal(now) || cancelled.CancelledAt.Location() != time.UTC {
		t.Fatalf("cancelled: %+v", cancelled)
	}
	// Cancelling again keeps the original moment.
	again, err := mustPatch(t, `{"status":"cancelled"}`).apply(cancelled, now.Add(time.Hour))
	if err != nil || !again.CancelledAt.Equal(now) {
		t.Fatalf("re-cancelled at %v, %v; want %v", again.CancelledAt, err, now)
	}
	removed := cancelled
	removed.RemovedFromSourceAt = ptr(now)
	restored, err := mustPatch(t, `{"status":"active"}`).apply(removed, now)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != generated.Active || restored.CancelledAt != nil || restored.RemovedFromSourceAt != nil {
		t.Fatalf("restored: %+v", restored)
	}
	if _, err := mustPatch(t, `{"status":"archived"}`).apply(timedFields(), now); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestCursorsRoundTripAndDoNotMix(t *testing.T) {
	id := googleuuid.New()
	list := listPosition{StartsAt: time.Date(2026, time.November, 16, 23, 0, 0, 123456000, time.UTC), ID: id}
	decoded, err := decodeListCursor(encodeListCursor(list))
	if err != nil || !decoded.StartsAt.Equal(list.StartsAt) || decoded.ID != id {
		t.Fatalf("list cursor: %+v, %v", decoded, err)
	}
	search := searchPosition{Anchor: time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC), Relevance: 7, Bucket: 1, SortKey: -1794870000000000, ID: id}
	got, err := decodeSearchCursor(encodeSearchCursor(search))
	if err != nil || got != search {
		t.Fatalf("search cursor: %+v, %v; want %+v", got, err, search)
	}
	if _, err := decodeSearchCursor(encodeListCursor(list)); err == nil {
		t.Error("a list cursor was accepted by search")
	}
	if _, err := decodeListCursor(encodeSearchCursor(search)); err == nil {
		t.Error("a search cursor was accepted by list")
	}
	for _, bad := range []string{"", "!!!", encodeCursor("list", "yesterday", id.String()), encodeCursor("search", "2026-10-10T08:00:00Z", "1", "2", "3", id.String())} {
		if _, err := decodeListCursor(bad); err == nil {
			t.Errorf("list cursor %q accepted", bad)
		}
		if _, err := decodeSearchCursor(bad); err == nil {
			t.Errorf("search cursor %q accepted", bad)
		}
	}
}

func TestSearchPatterns(t *testing.T) {
	patterns, err := searchPatterns("  Papier  gelber papier ")
	if err != nil || strings.Join(patterns, " ") != "%Papier% %gelber%" {
		t.Fatalf("patterns = %q, %v", patterns, err)
	}
	// LIKE wildcards are literal characters in a search.
	patterns, err = searchPatterns(`100% a_b c\d`)
	if err != nil || strings.Join(patterns, " ") != `%100\%% %a\_b% %c\\d%` {
		t.Fatalf("escaped patterns = %q, %v", patterns, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 201), "a b c d e f g h i j k"} {
		if _, err := searchPatterns(bad); err == nil {
			t.Errorf("query %q accepted", bad)
		}
	}
	if _, err := searchPatterns(strings.Repeat("ü", 200)); err != nil {
		t.Errorf("200 characters refused: %v", err)
	}
}
