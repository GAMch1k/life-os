package ics

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	berlin, _ = time.LoadLocation("Europe/Berlin")
	// Saturday 2026-10-10 12:00 Berlin.
	now = time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)
)

func options() Options { return Options{Owner: berlin, Now: now} }

func calendar(body ...string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + strings.Join(body, "\r\n") + "\r\nEND:VCALENDAR\r\n")
}

func vevent(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT"
}

func parse(t *testing.T, body []byte) Feed {
	t.Helper()
	feed, err := Parse(body, options())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return feed
}

func byUID(feed Feed) map[string]Item {
	out := map[string]Item{}
	for _, item := range feed.Items {
		out[item.ExternalUID] = item
	}
	return out
}

func uids(feed Feed) []string {
	var out []string
	for _, item := range feed.Items {
		out = append(out, item.ExternalUID)
	}
	sort.Strings(out)
	return out
}

func utc(item Item) string {
	return item.StartAt.UTC().Format("2006-01-02T15:04Z") + "/" + item.EndAt.UTC().Format("2006-01-02T15:04Z")
}

// The sanitized fixture mirrors the real ZAW (MyMüll) feed: a VTIMEZONE for
// Europe/Berlin, TZID-qualified 06:00-07:00 pickups, folded UTF-8 lines,
// escaped commas, and Microsoft X- properties.
func TestParsesTheZAWStyleFeed(t *testing.T) {
	body, err := os.ReadFile("testdata/zaw-sample.ics")
	if err != nil {
		t.Fatal(err)
	}
	feed := parse(t, body)
	if feed.Components != 29 || len(feed.Items) != 29 || feed.Skipped != 0 {
		t.Fatalf("components=%d items=%d skipped=%d", feed.Components, len(feed.Items), feed.Skipped)
	}
	first := feed.Items[0]
	// 06:00 CEST on 14 October is 04:00 UTC.
	if first.AllDay || utc(first) != "2026-10-14T04:00Z/2026-10-14T05:00Z" || *first.TimeZone != "Europe/Berlin" {
		t.Fatalf("first = %+v", first)
	}
	if first.Title != "Restmüll Container wöchentlich - Musterstadt, Beispielweg" || *first.Location != "Musterstadt, Beispielweg" || first.Description != nil {
		t.Fatalf("text: %q %q", first.Title, *first.Location)
	}
	items := byUID(feed)
	// After the change to CET on 25 October, 06:00 is 05:00 UTC.
	for _, item := range feed.Items {
		if strings.HasPrefix(item.Title, "Bioabfall") && item.StartAt.Month() == time.November && item.StartAt.Day() == 2 {
			if utc(item) != "2026-11-02T05:00Z/2026-11-02T06:00Z" {
				t.Errorf("November pickup = %s", utc(item))
			}
		}
		if strings.HasPrefix(item.Title, "Restmüll Tonnen und Container 14-täglich") && item.StartAt.Day() == 21 && item.StartAt.Month() == time.October {
			// The summary was folded inside the line; unfolding restores it.
			if item.Title != "Restmüll Tonnen und Container 14-täglich - Musterstadt, Beispielweg" {
				t.Errorf("folded summary = %q", item.Title)
			}
		}
		if strings.HasPrefix(item.Title, "Schadstoffmobil") && (item.Description == nil || !strings.Contains(*item.Description, "(9:00 - 13:00 Uhr)")) {
			t.Errorf("Schadstoffmobil description = %v", item.Description)
		}
	}
	if len(items) != 29 {
		t.Fatalf("UIDs are not unique: %d", len(items))
	}
	// The owner's ZAW filter keeps four of the six waste types.
	zaw := Filter{Include: []string{"Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"}}
	kept := zaw.Apply(feed.Items)
	counts := map[string]int{}
	for _, item := range kept {
		counts[strings.SplitN(item.Title, " - ", 2)[0]]++
	}
	if len(kept) != 21 || counts["Restmüll Container wöchentlich"] != 0 || counts["Schadstoffmobil"] != 0 || counts["Bioabfall"] != 6 || counts["Gelber Sack"] != 6 || counts["Papier Tonnen und Container"] != 3 || counts["Restmüll Tonnen und Container 14-täglich"] != 6 {
		t.Fatalf("kept %d: %v", len(kept), counts)
	}
	// The same selection by exclusion.
	if excluded := (Filter{Exclude: []string{"container wöchentlich", "SCHADSTOFFMOBIL"}}).Apply(feed.Items); len(excluded) != 21 {
		t.Fatalf("exclude filter kept %d", len(excluded))
	}
}

func TestAllDayFloatingAndUTCTimes(t *testing.T) {
	feed := parse(t, calendar(
		vevent("UID:holiday", "DTSTART;VALUE=DATE:20261224", "DTEND;VALUE=DATE:20261227", "SUMMARY:Christmas"),
		vevent("UID:one-day", "DTSTART;VALUE=DATE:20261003", "SUMMARY:Unity day"),
		vevent("UID:bare-date", "DTSTART:20261101", "DURATION:P2D", "SUMMARY:Two days"),
		vevent("UID:floating", "DTSTART:20261020T090000", "DTEND:20261020T100000", "SUMMARY:Floating"),
		vevent("UID:utc", "DTSTART:20261020T090000Z", "DURATION:PT1H30M", "SUMMARY:UTC"),
		vevent("UID:no-end", "DTSTART:20261021T090000Z", "SUMMARY:Instant"),
		vevent("UID:nominal-day", "DTSTART;TZID=Europe/Berlin:20261024T120000", "DURATION:P1D", "SUMMARY:Across DST"),
	))
	items := byUID(feed)
	holiday := items["holiday"]
	if !holiday.AllDay || holiday.StartDate.Format(time.DateOnly) != "2026-12-24" || holiday.EndDate.Format(time.DateOnly) != "2026-12-26" || holiday.TimeZone != nil {
		t.Fatalf("holiday (exclusive DTEND becomes inclusive) = %+v", holiday)
	}
	if day := items["one-day"]; day.StartDate != day.EndDate {
		t.Errorf("one-day = %+v", day)
	}
	if two := items["bare-date"]; !two.AllDay || two.EndDate.Format(time.DateOnly) != "2026-11-02" {
		t.Errorf("bare date with duration = %+v", two)
	}
	// Floating times are the owner's (Berlin, CEST in October).
	if floating := items["floating"]; utc(floating) != "2026-10-20T07:00Z/2026-10-20T08:00Z" || floating.TimeZone != nil {
		t.Errorf("floating = %s %v", utc(floating), floating.TimeZone)
	}
	if u := items["utc"]; utc(u) != "2026-10-20T09:00Z/2026-10-20T10:30Z" || u.TimeZone != nil {
		t.Errorf("utc = %s", utc(u))
	}
	// Without an end, a timed event gets a nominal minute.
	if instant := items["no-end"]; utc(instant) != "2026-10-21T09:00Z/2026-10-21T09:01Z" {
		t.Errorf("no end = %s", utc(instant))
	}
	// P1D is a nominal day: noon to noon local, 25 hours across the change.
	if nominal := items["nominal-day"]; utc(nominal) != "2026-10-24T10:00Z/2026-10-25T11:00Z" {
		t.Errorf("nominal day = %s", utc(nominal))
	}
	// X-WR-TIMEZONE overrides the owner's zone for floating times.
	feed = parse(t, calendar("X-WR-TIMEZONE:America/New_York", vevent("UID:f", "DTSTART:20261020T090000", "SUMMARY:NY")))
	if item := feed.Items[0]; utc(item) != "2026-10-20T13:00Z/2026-10-20T13:01Z" {
		t.Errorf("X-WR-TIMEZONE floating = %s", utc(item))
	}
}

// TZIDs that are not IANA names: a Windows name, a Mozilla-style path, and
// a custom name whose VTIMEZONE rules are applied directly.
func TestTimezoneResolution(t *testing.T) {
	custom := strings.Join([]string{
		"BEGIN:VTIMEZONE", "TZID:My Office Time",
		"BEGIN:STANDARD", "DTSTART:19701025T030000", "TZOFFSETFROM:+0200", "TZOFFSETTO:+0100", "RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU", "END:STANDARD",
		"BEGIN:DAYLIGHT", "DTSTART:19700329T020000", "TZOFFSETFROM:+0100", "TZOFFSETTO:+0200", "RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU", "END:DAYLIGHT",
		"END:VTIMEZONE",
	}, "\r\n")
	licensed := strings.Join([]string{"BEGIN:VTIMEZONE", "TZID:Berlin (custom)", "X-LIC-LOCATION:Europe/Berlin", "BEGIN:STANDARD", "DTSTART:19701025T030000", "TZOFFSETFROM:+0200", "TZOFFSETTO:+0100", "END:STANDARD", "END:VTIMEZONE"}, "\r\n")
	feed := parse(t, calendar(custom, licensed,
		vevent("UID:windows", `DTSTART;TZID="W. Europe Standard Time":20261020T090000`, "SUMMARY:Windows"),
		vevent("UID:mozilla", "DTSTART;TZID=/mozilla.org/20050126_1/Europe/Berlin:20261120T090000", "SUMMARY:Mozilla"),
		vevent("UID:summer", "DTSTART;TZID=My Office Time:20261020T090000", "SUMMARY:Summer"),
		vevent("UID:winter", "DTSTART;TZID=My Office Time:20261120T090000", "SUMMARY:Winter"),
		vevent("UID:licensed", "DTSTART;TZID=Berlin (custom):20261120T090000", "SUMMARY:X-LIC"),
		vevent("UID:unknown", "DTSTART;TZID=Nowhere/Special:20261120T090000", "SUMMARY:Unknown"),
	))
	items := byUID(feed)
	check := func(uid, want string, zone string) {
		t.Helper()
		item := items[uid]
		if got := item.StartAt.UTC().Format("2006-01-02T15:04Z"); got != want {
			t.Errorf("%s start = %s, want %s", uid, got, want)
		}
		got := ""
		if item.TimeZone != nil {
			got = *item.TimeZone
		}
		if got != zone {
			t.Errorf("%s zone = %q, want %q", uid, got, zone)
		}
	}
	check("windows", "2026-10-20T07:00Z", "Europe/Berlin")
	check("mozilla", "2026-11-20T08:00Z", "Europe/Berlin")
	check("summer", "2026-10-20T07:00Z", "") // VTIMEZONE rules: +02:00 in October
	check("winter", "2026-11-20T08:00Z", "") // and +01:00 after the last Sunday
	check("licensed", "2026-11-20T08:00Z", "Europe/Berlin")
	check("unknown", "2026-11-20T08:00Z", "") // falls back to the owner's zone
}

// A weekly series is imported as one event per occurrence within the
// window, keyed by RECURRENCE-ID; EXDATE drops one, an override replaces
// one, and a cancelled override cancels it.
func TestRecurringEventsBecomeOccurrences(t *testing.T) {
	feed := parse(t, calendar(
		vevent("UID:yoga", "DTSTART;TZID=Europe/Berlin:20261005T180000", "DTEND;TZID=Europe/Berlin:20261005T190000",
			"RRULE:FREQ=WEEKLY;COUNT=6", "EXDATE;TZID=Europe/Berlin:20261012T180000", "SUMMARY:Yoga"),
		vevent("UID:yoga", "RECURRENCE-ID;TZID=Europe/Berlin:20261019T180000", "DTSTART;TZID=Europe/Berlin:20261020T183000",
			"DTEND;TZID=Europe/Berlin:20261020T193000", "SUMMARY:Yoga (moved)"),
		vevent("UID:yoga", "RECURRENCE-ID:20261026T170000Z", "DTSTART;TZID=Europe/Berlin:20261026T180000",
			"DTEND;TZID=Europe/Berlin:20261026T190000", "STATUS:CANCELLED", "SUMMARY:Yoga"),
		vevent("UID:bins", "DTSTART;VALUE=DATE:20261001", "RRULE:FREQ=MONTHLY;BYMONTHDAY=1;UNTIL=20261201", "EXDATE;VALUE=DATE:20261101", "SUMMARY:Bins"),
	))
	want := []string{
		"bins#20261001", "bins#20261201",
		"yoga#20261005T160000Z", "yoga#20261019T160000Z", "yoga#20261026T170000Z", "yoga#20261102T170000Z", "yoga#20261109T170000Z",
	}
	if got := uids(feed); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("uids:\n got  %v\n want %v", got, want)
	}
	items := byUID(feed)
	// 18:00 local throughout: 16:00 UTC in summer time, 17:00 after.
	if first := items["yoga#20261005T160000Z"]; utc(first) != "2026-10-05T16:00Z/2026-10-05T17:00Z" || first.Title != "Yoga" {
		t.Errorf("first = %s %q", utc(first), first.Title)
	}
	if after := items["yoga#20261102T170000Z"]; utc(after) != "2026-11-02T17:00Z/2026-11-02T18:00Z" {
		t.Errorf("after DST = %s", utc(after))
	}
	if moved := items["yoga#20261019T160000Z"]; moved.Title != "Yoga (moved)" || utc(moved) != "2026-10-20T16:30Z/2026-10-20T17:30Z" {
		t.Errorf("override = %q %s", moved.Title, utc(moved))
	}
	if cancelled := items["yoga#20261026T170000Z"]; !cancelled.Cancelled {
		t.Errorf("cancelled override = %+v", cancelled)
	}
	if bins := items["bins#20261201"]; !bins.AllDay || bins.StartDate.Format(time.DateOnly) != "2026-12-01" {
		t.Errorf("all-day occurrence = %+v", bins)
	}
}

func TestInfiniteSeriesStayWithinTheWindow(t *testing.T) {
	feed := parse(t, calendar(vevent("UID:daily", "DTSTART:20200101T080000Z", "DURATION:PT15M", "RRULE:FREQ=DAILY", "SUMMARY:Standup")))
	// A year back and a year ahead of now.
	if len(feed.Items) < 728 || len(feed.Items) > 732 {
		t.Fatalf("daily series produced %d occurrences", len(feed.Items))
	}
	first, last := feed.Items[0].StartAt, feed.Items[len(feed.Items)-1].StartAt
	if first.Before(now.Add(-DefaultLookback-24*time.Hour)) || last.After(now.Add(DefaultHorizon)) {
		t.Fatalf("window %v .. %v", first, last)
	}
	if _, err := Parse(calendar(vevent("UID:daily", "DTSTART:20200101T080000Z", "RRULE:FREQ=DAILY", "SUMMARY:x")), Options{Owner: berlin, Now: now, MaxItems: 10}); err == nil || !strings.Contains(err.Error(), "more than 10 events") {
		t.Fatalf("MaxItems: %v", err)
	}
}

func TestUnreadableEventsAreSkippedNotFatal(t *testing.T) {
	feed := parse(t, calendar(
		vevent("UID:ok", "DTSTART:20261020T090000Z", "SUMMARY:Fine\\, escaped\\; text\\nsecond line", "DESCRIPTION:Line one\\nLine two", "CATEGORIES:Paper,Recycling"),
		vevent("UID:no-start", "SUMMARY:Broken"),
		vevent("UID:bad-date", "DTSTART:2026-10-20", "SUMMARY:Broken"),
		vevent("UID:ok", "DTSTART:20261021T090000Z", "SUMMARY:Duplicate"),
		vevent("DTSTART:20261022T090000Z", "SUMMARY:No UID"),
		vevent("UID:untitled", "DTSTART:20261023T090000Z"),
		vevent("UID:bad-rule", "DTSTART:20261023T090000Z", "RRULE:FREQ=SOMETIMES", "SUMMARY:Bad rule"),
		"X-STRAY-PROPERTY:between components",
	))
	if feed.Components != 7 || feed.Skipped != 4 || len(feed.Items) != 3 {
		t.Fatalf("components=%d skipped=%d items=%v", feed.Components, feed.Skipped, uids(feed))
	}
	items := byUID(feed)
	ok := items["ok"]
	if ok.Title != "Fine, escaped; text second line" || *ok.Description != "Line one\nLine two" || strings.Join(ok.Categories, "|") != "Paper|Recycling" {
		t.Errorf("text = %q %q %v", ok.Title, *ok.Description, ok.Categories)
	}
	if items["untitled"].Title != untitled {
		t.Errorf("untitled = %q", items["untitled"].Title)
	}
	var synthetic string
	for uid := range items {
		if strings.HasPrefix(uid, "nouid-") {
			synthetic = uid
		}
	}
	again := byUID(parse(t, calendar(vevent("DTSTART:20261022T090000Z", "SUMMARY:No UID"))))
	if _, stable := again[synthetic]; synthetic == "" || !stable {
		t.Errorf("an event without UID gets a stable key: %q %v", synthetic, again)
	}
	// Category filter terms match whole categories.
	if !(Filter{Include: []string{"recycling"}}).Keep(ok) || (Filter{Include: []string{"cycling"}}).Keep(ok) {
		t.Error("category matching")
	}
}

func TestRejectsDocumentsThatAreNotCompleteFeeds(t *testing.T) {
	for name, body := range map[string]string{
		"html":       "<!doctype html><html><body>Maintenance</body></html>",
		"empty":      "",
		"truncated":  "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTART:20261020T090000Z\r\n",
		"unbalanced": "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nEND:VTODO\r\nEND:VCALENDAR",
		"cut event":  "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nEND:VCALENDAR",
	} {
		if _, err := Parse([]byte(body), options()); err == nil {
			t.Errorf("%s: parsed without error", name)
		} else if _, ok := err.(*Error); !ok {
			t.Errorf("%s: error %T is not a storable *Error", name, err)
		}
	}
	// A BOM, LF line endings, and lower-case property names are tolerated.
	feed := parse(t, []byte("\xef\xbb\xbfBEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nuid:lf\ndtstart:20261020T090000Z\nsummary:LF\nEND:VEVENT\nEND:VCALENDAR\n"))
	if len(feed.Items) != 1 || feed.Items[0].ExternalUID != "lf" || feed.Items[0].Title != "LF" {
		t.Fatalf("lenient feed = %+v", feed)
	}
}

func TestDurationAndOffsetParsing(t *testing.T) {
	for value, want := range map[string]string{"P1D": "0 1 0s", "PT1H30M": "0 0 1h30m0s", "P2W": "2 0 0s", "-PT15M": "0 0 -15m0s", "P1DT12H": "0 1 12h0m0s"} {
		weeks, days, rest, ok := parseDuration(value)
		if got := strings.Join([]string{strconv.Itoa(weeks), strconv.Itoa(days), rest.String()}, " "); !ok || got != want {
			t.Errorf("%s = %s %v, want %s", value, got, ok, want)
		}
	}
	for _, bad := range []string{"", "P", "1D", "PXD", "P1H", "PT1D"} {
		if _, _, _, ok := parseDuration(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if offset, ok := parseUTCOffset("-0330"); !ok || offset != -(3*time.Hour+30*time.Minute) {
		t.Errorf("offset = %v %v", offset, ok)
	}
	if _, ok := parseUTCOffset("0100"); ok {
		t.Error("an offset needs a sign")
	}
}
