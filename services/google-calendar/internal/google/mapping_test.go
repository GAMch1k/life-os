package google

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

var (
	berlin, _ = time.LoadLocation("Europe/Berlin")
	// Saturday 2026-10-10 12:00 in Berlin.
	mappingNow = time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)
)

func testMapper() mapper {
	return mapper{owner: berlin, zone: berlin, now: mappingNow, windowStart: mappingNow.AddDate(-1, 0, 0)}
}

func timed(dateTime, zone string) *EventTime { return &EventTime{DateTime: dateTime, TimeZone: zone} }
func allDay(date string) *EventTime          { return &EventTime{Date: date} }

func byUID(items []sources.Item) map[string]sources.Item {
	out := map[string]sources.Item{}
	for _, item := range items {
		out[item.ExternalUID] = item
	}
	return out
}

func derive(t *testing.T, events ...Event) []sources.Item {
	t.Helper()
	items, skipped := testMapper().derive(events)
	if skipped != 0 {
		t.Fatalf("skipped %d", skipped)
	}
	return items
}

func TestTimedAndAllDayEvents(t *testing.T) {
	items := byUID(derive(t,
		Event{ID: "dentist", Status: "confirmed", Summary: "  Dentist\n appointment ", Location: "Praxis Dr. Weber", Description: "Bring the card",
			Start: timed("2026-10-12T09:30:00+02:00", "Europe/Berlin"), End: timed("2026-10-12T10:15:00+02:00", "Europe/Berlin"),
			Reminders: &Reminders{UseDefault: false, Overrides: []Reminder{{Method: "popup", Minutes: 30}, {Method: "email", Minutes: 30}, {Method: "email", Minutes: 1440}}}},
		// No timeZone of its own: the calendar's.
		Event{ID: "call", Summary: "Call", Start: timed("2026-10-13T16:00:00Z", ""), End: timed("2026-10-13T16:00:00Z", ""), Reminders: &Reminders{UseDefault: true}},
		Event{ID: "trip", Summary: "Trip", Start: allDay("2026-10-20"), End: allDay("2026-10-23"), Reminders: &Reminders{UseDefault: true}},
		Event{ID: "holiday", Start: allDay("2026-10-26"), End: allDay("2026-10-27")},
		Event{ID: "deleted", Status: "cancelled"},
		Event{ID: "office", EventType: "workingLocation", Summary: "Office", Start: allDay("2026-10-12"), End: allDay("2026-10-13")},
	))
	if len(items) != 4 {
		t.Fatalf("items = %v", items)
	}
	dentist := items["dentist"]
	if dentist.Title != "Dentist appointment" || *dentist.Location != "Praxis Dr. Weber" || *dentist.Description != "Bring the card" || dentist.AllDay ||
		!dentist.StartAt.Equal(time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC)) || !dentist.EndAt.Equal(time.Date(2026, 10, 12, 8, 15, 0, 0, time.UTC)) ||
		*dentist.TimeZone != "Europe/Berlin" || dentist.Recurrence != nil {
		t.Fatalf("dentist = %+v", dentist)
	}
	// Overrides become relative reminders, each lead time once.
	if got := remindersText(dentist.Reminders); got != "own 30,1440" {
		t.Fatalf("dentist reminders = %s", got)
	}
	// A zero-length event lasts a minute; the calendar's zone is its hint.
	call := items["call"]
	if !call.EndAt.Equal(call.StartAt.Add(time.Minute)) || *call.TimeZone != "Europe/Berlin" || remindersText(call.Reminders) != "defaults" {
		t.Fatalf("call = %+v %s", call, remindersText(call.Reminders))
	}
	// All-day end dates are exclusive at Google, inclusive in Calendar.
	trip := items["trip"]
	if !trip.AllDay || trip.StartDate.Format(time.DateOnly) != "2026-10-20" || trip.EndDate.Format(time.DateOnly) != "2026-10-22" || trip.TimeZone != nil {
		t.Fatalf("trip = %+v", trip)
	}
	// Google's all-day defaults are not readable: none rather than the
	// timed defaults.
	if remindersText(trip.Reminders) != "own " {
		t.Fatalf("trip reminders = %s", remindersText(trip.Reminders))
	}
	holiday := items["holiday"]
	if holiday.Title != untitled || holiday.EndDate.Format(time.DateOnly) != "2026-10-26" {
		t.Fatalf("holiday = %+v", holiday)
	}
}

func remindersText(r *sources.Reminders) string {
	if r == nil {
		return "nil"
	}
	var minutes []string
	for _, reminder := range r.Reminders {
		minutes = append(minutes, itoa(*reminder.MinutesBefore))
	}
	if r.UseDefault {
		if len(minutes) == 0 {
			return "defaults"
		}
		return "defaults+" + strings.Join(minutes, ",")
	}
	return "own " + strings.Join(minutes, ",")
}

func itoa(value int) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestHTMLDescriptionsBecomeText(t *testing.T) {
	for in, want := range map[string]string{
		"Plain text\nsecond line":                                                                         "Plain text\nsecond line",
		"<b>Agenda</b><br>1. Budget<br/>2. Hiring &amp; onboarding":                                       "Agenda\n1. Budget\n2. Hiring & onboarding",
		`Join: <a href="https://meet.example.com/abc">https://meet.example.com/abc</a>`:                   "Join: https://meet.example.com/abc",
		`<p>See <a href="https://example.com/doc?a=1&amp;b=2">the doc</a></p><p></p><p></p><p>Thanks</p>`: "See the doc (https://example.com/doc?a=1&b=2)\n\nThanks",
		"5 < 6 and 7 > 3": "5 < 6 and 7 > 3",
	} {
		if got := notes(in); got != want {
			t.Errorf("notes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecurringEventsThatFitBecomeSeries(t *testing.T) {
	master := Event{
		ID: "standup", Summary: "Standup", Location: "Room 1",
		Start: timed("2026-10-12T09:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-12T09:15:00+02:00", "Europe/Berlin"),
		Recurrence: []string{"RRULE:FREQ=WEEKLY;WKST=SU;UNTIL=20261231T225959Z;BYDAY=MO,WE", "EXDATE;TZID=Europe/Berlin:20261014T090000"},
		Reminders:  &Reminders{UseDefault: true},
	}
	moved := Event{
		ID: "standup_20261019T070000Z", RecurringEventID: "standup", OriginalStartTime: timed("2026-10-19T09:00:00+02:00", "Europe/Berlin"),
		Summary: "Standup (long)", Location: "Room 1", Start: timed("2026-10-19T10:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-19T11:00:00+02:00", "Europe/Berlin"),
		Reminders: &Reminders{UseDefault: false, Overrides: []Reminder{{Method: "popup", Minutes: 5}}},
	}
	// After the change to winter time the series keeps 09:00 Berlin.
	skipped := Event{ID: "standup_20261028T080000Z", RecurringEventID: "standup", Status: "cancelled", OriginalStartTime: timed("2026-10-28T09:00:00+01:00", "Europe/Berlin")}
	// Google lists an instance whose original start the rule does not
	// produce (a Tuesday): kept as an event of its own.
	odd := Event{ID: "standup_20261020T070000Z", RecurringEventID: "standup", OriginalStartTime: timed("2026-10-20T09:00:00+02:00", "Europe/Berlin"),
		Summary: "Standup", Start: timed("2026-10-20T09:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-20T09:15:00+02:00", "Europe/Berlin")}
	items := byUID(derive(t, master, moved, skipped, odd))
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	series := items["standup"]
	rule := series.Recurrence
	if rule == nil || rule.Frequency != "weekly" || rule.Interval != 1 || strings.Join(rule.ByWeekday, ",") != "MO,WE" || rule.Count != 0 ||
		rule.Until == nil || !rule.Until.Equal(time.Date(2026, 12, 31, 22, 59, 59, 0, time.UTC)) {
		t.Fatalf("rule = %+v", rule)
	}
	if !series.StartAt.Equal(time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)) || *series.TimeZone != "Europe/Berlin" {
		t.Fatalf("series = %+v", series)
	}
	if len(series.Exceptions) != 3 {
		t.Fatalf("exceptions = %+v", series.Exceptions)
	}
	if exdate := series.Exceptions["2026-10-14T07:00:00Z"]; !exdate.Cancelled || !exdate.Changes.Empty() {
		t.Fatalf("EXDATE = %+v", exdate)
	}
	if skip := series.Exceptions["2026-10-28T08:00:00Z"]; !skip.Cancelled {
		t.Fatalf("cancelled instance = %+v", skip)
	}
	change := series.Exceptions["2026-10-19T07:00:00Z"]
	raw, _ := change.Changes.JSON()
	want := `{"endAt":"2026-10-19T09:00:00Z","reminders":[{"kind":"relative","minutesBefore":5}],"startAt":"2026-10-19T08:00:00Z","title":"Standup (long)","useDefaultReminders":false}`
	if change.Cancelled || string(raw) != want {
		t.Fatalf("changed instance = %s", raw)
	}
	if own := items["standup_20261020T070000Z"]; own.Recurrence != nil || !own.StartAt.Equal(time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("odd instance = %+v", own)
	}

	// Count rules, plain weekly days, monthly and yearly repeats of the
	// start's day fit too.
	for _, test := range []struct {
		rule, start string
		want        sources.Recurrence
	}{
		{"RRULE:FREQ=DAILY;INTERVAL=2;COUNT=10", "2026-10-12T09:00:00+02:00", sources.Recurrence{Frequency: "daily", Interval: 2, Count: 10}},
		{"RRULE:FREQ=WEEKLY;BYDAY=MO", "2026-10-12T09:00:00+02:00", sources.Recurrence{Frequency: "weekly", Interval: 1}},
		{"RRULE:FREQ=MONTHLY;BYMONTHDAY=12", "2026-10-12T09:00:00+02:00", sources.Recurrence{Frequency: "monthly", Interval: 1}},
		{"RRULE:FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=12", "2026-10-12T09:00:00+02:00", sources.Recurrence{Frequency: "yearly", Interval: 1}},
		{"RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", "2026-10-12T09:00:00+02:00", sources.Recurrence{Frequency: "weekly", Interval: 2, ByWeekday: []string{"MO", "FR"}}},
	} {
		event := master
		event.Recurrence, event.Start, event.End = []string{test.rule}, timed(test.start, "Europe/Berlin"), timed(test.start, "Europe/Berlin")
		found := derive(t, event)
		got := found[0].Recurrence
		if len(found) != 1 || got == nil || got.Frequency != test.want.Frequency || got.Interval != test.want.Interval || got.Count != test.want.Count ||
			strings.Join(got.ByWeekday, ",") != strings.Join(test.want.ByWeekday, ",") || got.Until != nil {
			t.Errorf("%s: %+v", test.rule, got)
		}
	}
}

func TestAllDaySeries(t *testing.T) {
	// A birthday: yearly, all day, until a date.
	birthday := Event{ID: "birthday", Summary: "Anna's birthday", Start: allDay("2026-11-03"), End: allDay("2026-11-04"),
		Recurrence: []string{"RRULE:FREQ=YEARLY;UNTIL=20301103", "EXDATE;VALUE=DATE:20281103"}}
	moved := Event{ID: "birthday_20271103", RecurringEventID: "birthday", OriginalStartTime: allDay("2027-11-03"), Summary: "Anna's birthday party",
		Start: allDay("2027-11-06"), End: allDay("2027-11-07")}
	items := derive(t, birthday, moved)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	series := items[0]
	// The until date is kept as noon in the owner's timezone.
	if rule := series.Recurrence; rule == nil || rule.Frequency != "yearly" || rule.Until == nil ||
		!rule.Until.Equal(time.Date(2030, 11, 3, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("rule = %+v", series.Recurrence)
	}
	if !series.Exceptions["2028-11-03"].Cancelled {
		t.Fatalf("exceptions = %+v", series.Exceptions)
	}
	raw, _ := series.Exceptions["2027-11-03"].Changes.JSON()
	if string(raw) != `{"endDate":"2027-11-06","startDate":"2027-11-06","title":"Anna's birthday party"}` {
		t.Fatalf("moved = %s", raw)
	}
}

func TestRecurringEventsOutsideTheModelAreExpanded(t *testing.T) {
	// Monthly on the second Tuesday at 18:00 Berlin, across the change to
	// winter time.
	master := Event{ID: "club", Summary: "Book club", Start: timed("2026-10-13T18:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-13T20:00:00+02:00", "Europe/Berlin"),
		Recurrence: []string{"RRULE:FREQ=MONTHLY;BYDAY=2TU"}, Reminders: &Reminders{UseDefault: true}}
	moved := Event{ID: "club_20261110T170000Z", RecurringEventID: "club", OriginalStartTime: timed("2026-11-10T18:00:00+01:00", "Europe/Berlin"),
		Summary: "Book club at Anna's", Start: timed("2026-11-11T18:00:00+01:00", "Europe/Berlin"), End: timed("2026-11-11T20:00:00+01:00", "Europe/Berlin")}
	skipped := Event{ID: "club_20261208T170000Z", RecurringEventID: "club", Status: "cancelled", OriginalStartTime: timed("2026-12-08T18:00:00+01:00", "Europe/Berlin")}
	items := derive(t, master, moved, skipped)
	uids := byUID(items)
	// From its start until 400 days ahead (14 months, the last on 9
	// November 2027), minus the skipped one.
	if len(items) != 13 || uids["club_20271109T170000Z"].Title != "Book club" {
		t.Fatalf("%d occurrences", len(items))
	}
	october := uids["club_20261013T160000Z"]
	if october.Recurrence != nil || october.Title != "Book club" || !october.StartAt.Equal(time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC)) ||
		!october.EndAt.Equal(time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)) || remindersText(october.Reminders) != "defaults" {
		t.Fatalf("october = %+v", october)
	}
	if november := uids["club_20261110T170000Z"]; november.Title != "Book club at Anna's" || !november.StartAt.Equal(time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("november = %+v", november)
	}
	if _, found := uids["club_20261208T170000Z"]; found {
		t.Fatal("the cancelled occurrence is imported")
	}
	// January: 18:00 in winter time is 17:00 UTC.
	if january := uids["club_20270112T170000Z"]; !january.StartAt.Equal(time.Date(2027, 1, 12, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("january = %+v", january)
	}

	// RDATE, several RRULEs, BYSETPOS, and a WKST that changes weekly
	// grouping are expanded too.
	for _, recurrence := range [][]string{
		{"RRULE:FREQ=WEEKLY;BYDAY=TU", "RDATE;TZID=Europe/Berlin:20261016T180000"},
		{"RRULE:FREQ=WEEKLY;BYDAY=TU", "RRULE:FREQ=MONTHLY;BYMONTHDAY=1"},
		{"RRULE:FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1"},
		{"RRULE:FREQ=WEEKLY;INTERVAL=2;WKST=SU;BYDAY=TU,SU"},
		{"RRULE:FREQ=MONTHLY;BYMONTHDAY=-1"},
	} {
		event := master
		event.Recurrence = recurrence
		found := derive(t, event)
		if len(found) < 2 || found[0].Recurrence != nil || !strings.HasPrefix(found[0].ExternalUID, "club_") {
			t.Errorf("%v: %d items, first %+v", recurrence, len(found), found[0])
		}
	}
}

func TestOrphanInstancesAndUnreadableEvents(t *testing.T) {
	orphan := Event{ID: "gone_20261019T070000Z", RecurringEventID: "gone", OriginalStartTime: timed("2026-10-19T09:00:00+02:00", "Europe/Berlin"),
		Summary: "Moved", Start: timed("2026-10-19T10:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-19T11:00:00+02:00", "Europe/Berlin")}
	cancelledOrphan := Event{ID: "gone_20261026T080000Z", RecurringEventID: "gone", Status: "cancelled"}
	items, skipped := testMapper().derive([]Event{orphan, cancelledOrphan, {ID: "broken", Summary: "No start"}})
	if len(items) != 1 || items[0].ExternalUID != "gone_20261019T070000Z" || skipped != 1 {
		t.Fatalf("items = %+v, skipped %d", items, skipped)
	}
}

func TestUnreadableInstancesAreCounted(t *testing.T) {
	master := Event{ID: "standup", Summary: "Standup", Start: timed("2026-10-12T09:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-12T09:15:00+02:00", "Europe/Berlin"),
		Recurrence: []string{"RRULE:FREQ=WEEKLY"}}
	broken := Event{ID: "standup_20261019T070000Z", RecurringEventID: "standup", OriginalStartTime: timed("2026-10-19T09:00:00+02:00", "Europe/Berlin"), Summary: "Moved"}
	items, skipped := testMapper().derive([]Event{master, broken})
	if len(items) != 1 || items[0].Recurrence == nil || len(items[0].Exceptions) != 0 || skipped != 1 {
		t.Fatalf("fitting series: %+v, skipped %d", items, skipped)
	}
	master.Recurrence = []string{"RRULE:FREQ=MONTHLY;BYDAY=3MO"}
	broken.OriginalStartTime = timed("2026-10-19T09:00:00+02:00", "Europe/Berlin")
	items, skipped = testMapper().derive([]Event{master, broken})
	october := byUID(items)["standup_20261019T070000Z"]
	if skipped != 1 || october.Title != "Standup" || !october.StartAt.Equal(time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("expanded series: %+v, skipped %d", october, skipped)
	}
}
