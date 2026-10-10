package google

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teambition/rrule-go"

	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Mapping limits, as for feeds.
const (
	// ExpansionHorizon is how far ahead a recurring event whose rule
	// Calendar's model cannot hold is imported as single occurrences.
	ExpansionHorizon  = 400 * 24 * time.Hour
	maxTitleRunes     = 500
	maxLocationRunes  = 500
	maxNotesRunes     = 10000
	untitled          = "Untitled event"
	minimumSpan       = time.Minute
	maxReminderMinute = 40320
	maxEventReminders = 10
	// workingLocation events say where the owner works that day (home,
	// office); they are status markers in Google Calendar, not events.
	workingLocation = "workingLocation"
)

// mapper turns a calendar's Google events into Calendar items.
type mapper struct {
	// owner is the owner's timezone: an all-day series' until is a date in
	// it.
	owner *time.Location
	// zone is the calendar's timezone, for times Google gives without one.
	zone *time.Location
	// now and windowStart bound the occurrences of a rule Calendar's model
	// cannot hold: from windowStart to now + ExpansionHorizon.
	now, windowStart time.Time
}

// derive maps a calendar's stored Google events to the items Calendar
// stores, keyed (ExternalUID) by Google event id, and counts the events it
// could not read. A single event becomes one item. A recurring event becomes
// a series with its occurrence exceptions when its rule fits Calendar's
// model, else one item per occurrence ("<id>_<original start>", as Google
// names its instances). A changed occurrence whose recurring event is not
// among events becomes an item of its own.
func (m mapper) derive(events []Event) ([]sources.Item, int) {
	instances := map[string][]Event{}
	var masters []Event
	for _, event := range events {
		switch {
		case event.EventType == workingLocation:
		case event.RecurringEventID != "":
			instances[event.RecurringEventID] = append(instances[event.RecurringEventID], event)
		case !event.Cancelled():
			masters = append(masters, event)
		}
	}
	var items []sources.Item
	skipped := 0
	seen := map[string]bool{}
	add := func(found []sources.Item) {
		for _, item := range found {
			if seen[item.ExternalUID] {
				skipped++
				continue
			}
			seen[item.ExternalUID] = true
			items = append(items, item)
		}
	}
	for _, master := range masters {
		group := instances[master.ID]
		delete(instances, master.ID)
		if len(master.Recurrence) == 0 {
			item, err := m.item(master, master.ID)
			if err != nil {
				skipped++
				continue
			}
			add([]sources.Item{item})
			continue
		}
		found, unread, err := m.series(master, group)
		skipped += unread
		if err != nil {
			skipped++
			continue
		}
		add(found)
	}
	orphans := make([]string, 0, len(instances))
	for id := range instances {
		orphans = append(orphans, id)
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		for _, instance := range instances[id] {
			if instance.Cancelled() {
				continue
			}
			item, err := m.item(instance, instance.ID)
			if err != nil {
				skipped++
				continue
			}
			add([]sources.Item{item})
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].ExternalUID < items[b].ExternalUID })
	return items, skipped
}

// span is an event's time: instants for a timed event (with the zone it is
// scheduled in), inclusive dates for an all-day one.
type span struct {
	allDay      bool
	start, end  time.Time
	first, last time.Time
	zone        *time.Location
	zoneName    *string
}

var errNoStart = errors.New("the event has no start")

func (m mapper) span(event Event) (span, error) {
	if event.Start == nil {
		return span{}, errNoStart
	}
	if event.Start.Date != "" {
		first, err := time.Parse(time.DateOnly, event.Start.Date)
		if err != nil {
			return span{}, fmt.Errorf("unreadable start date: %w", err)
		}
		out := span{allDay: true, first: first, last: first}
		if event.End != nil && event.End.Date != "" {
			// Google's end date is exclusive.
			if end, err := time.Parse(time.DateOnly, event.End.Date); err == nil && end.After(first) {
				out.last = end.AddDate(0, 0, -1)
			}
		}
		return out, nil
	}
	start, err := time.Parse(time.RFC3339, event.Start.DateTime)
	if err != nil {
		return span{}, fmt.Errorf("unreadable start: %w", err)
	}
	out := span{start: start.UTC(), zone: m.zone}
	if loc := loadZone(event.Start.TimeZone); loc != nil {
		out.zone = loc
	}
	if out.zone != nil && out.zone.String() != "Local" {
		name := out.zone.String()
		out.zoneName = &name
	}
	if out.zone == nil {
		out.zone = m.owner
	}
	out.end = out.start.Add(minimumSpan)
	if event.End != nil && event.End.DateTime != "" {
		if end, err := time.Parse(time.RFC3339, event.End.DateTime); err == nil && end.After(start) {
			out.end = end.UTC()
		}
	}
	return out, nil
}

// loadZone loads an IANA timezone by name; "" and "Local" are none.
func loadZone(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}

// item maps one event as a single Calendar event with the given identity.
func (m mapper) item(event Event, uid string) (sources.Item, error) {
	when, err := m.span(event)
	if err != nil {
		return sources.Item{}, err
	}
	return m.itemAt(event, uid, when), nil
}

func (m mapper) itemAt(event Event, uid string, when span) sources.Item {
	item := ics.Item{
		ExternalUID: uid,
		Title:       clipRunes(oneLine(event.Summary), maxTitleRunes),
		Description: optional(clipRunes(notes(event.Description), maxNotesRunes)),
		Location:    optional(clipRunes(oneLine(event.Location), maxLocationRunes)),
		AllDay:      when.allDay,
	}
	if item.Title == "" {
		item.Title = untitled
	}
	if when.allDay {
		item.StartDate, item.EndDate = when.first, when.last
	} else {
		item.StartAt, item.EndAt, item.TimeZone = when.start, when.end, when.zoneName
	}
	reminders := eventReminders(event, when.allDay)
	return sources.Item{Item: item, Reminders: &reminders}
}

// eventReminders are the event's reminders as Calendar keeps them: its
// overrides as relative reminders, following the calendar's defaults when
// Google says useDefault. Google does not expose an all-day event's default
// notifications (its calendar defaults are for timed events), so an
// all-day event following its defaults gets none rather than wrong ones.
func eventReminders(event Event, allDay bool) sources.Reminders {
	out := sources.Reminders{UseDefault: true, Reminders: []sources.Reminder{}}
	if event.Reminders != nil {
		out.UseDefault = event.Reminders.UseDefault
		out.Reminders = relativeReminders(event.Reminders.Overrides)
	}
	if allDay {
		out.UseDefault = false
	}
	return out
}

// relativeReminders maps Google notifications (popup or email, minutes
// before) to relative reminders, each lead time once, at most ten.
func relativeReminders(list []Reminder) []sources.Reminder {
	out := []sources.Reminder{}
	seen := map[int]bool{}
	for _, reminder := range list {
		minutes := min(max(reminder.Minutes, 0), maxReminderMinute)
		if seen[minutes] || len(out) == maxEventReminders {
			continue
		}
		seen[minutes] = true
		out = append(out, sources.Relative(minutes))
	}
	return out
}

// series maps a recurring event and its changed or cancelled instances,
// and counts the changed instances it could not read (their occurrences
// keep the series' values).
func (m mapper) series(master Event, instances []Event) ([]sources.Item, int, error) {
	when, err := m.span(master)
	if err != nil {
		return nil, 0, err
	}
	base := m.itemAt(master, master.ID, when)
	if items, unread, ok := m.fit(master, base, when, instances); ok {
		return items, unread, nil
	}
	return m.expand(master, base, when, instances)
}

// weekdayCodes are iCalendar day codes by rrule weekday (MO is 0).
var weekdayCodes = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}

// maxSeriesCount bounds a count rule, as for series created in Life OS.
const maxSeriesCount = 1000

// fit maps the recurring event to Calendar's series model when its
// recurrence is one RRULE (plus EXDATEs) that the model holds: FREQ
// DAILY/WEEKLY/MONTHLY/YEARLY with INTERVAL, COUNT or UNTIL, and BYDAY
// (plain days including the start's) for a weekly rule; a BYMONTHDAY or
// BYMONTH that only repeats the start's day or month is accepted. EXDATEs
// and the instances become occurrence exceptions. ok is false when the
// rule or an instance does not fit; the series is then expanded.
func (m mapper) fit(master Event, base sources.Item, when span, instances []Event) ([]sources.Item, int, bool) {
	var rules, exdates []string
	for _, line := range master.Recurrence {
		switch propertyName(line) {
		case "RRULE":
			rules = append(rules, line)
		case "EXDATE":
			exdates = append(exdates, line)
		default:
			return nil, 0, false // RDATE, EXRULE
		}
	}
	if len(rules) != 1 {
		return nil, 0, false
	}
	rule, err := ics.ParseRule(cleanLine(rules[0]))
	if err != nil {
		return nil, 0, false
	}
	option := rule.Option
	frequencies := map[rrule.Frequency]string{rrule.DAILY: "daily", rrule.WEEKLY: "weekly", rrule.MONTHLY: "monthly", rrule.YEARLY: "yearly"}
	frequency, known := frequencies[option.Freq]
	if !known || len(option.Bysetpos) > 0 || len(option.Byyearday) > 0 || len(option.Byweekno) > 0 || len(option.Byhour) > 0 ||
		len(option.Byminute) > 0 || len(option.Bysecond) > 0 || len(option.Byeaster) > 0 {
		return nil, 0, false
	}
	wall, weekday := when.first, when.first.Weekday()
	if !when.allDay {
		wall = recurrence.Wall(when.start, when.zone)
		weekday = wall.Weekday()
	}
	series := &sources.Recurrence{Frequency: frequency, Interval: max(option.Interval, 1), Count: option.Count}
	if len(option.Byweekday) > 0 {
		if option.Freq != rrule.WEEKLY {
			return nil, 0, false // such as monthly on the second Tuesday
		}
		var codes []string
		for _, day := range option.Byweekday {
			if day.N() != 0 {
				return nil, 0, false
			}
			codes = append(codes, weekdayCodes[day.Day()])
		}
		codes, _ = recurrence.NormalizeWeekdays(codes)
		if !slices.Contains(codes, recurrence.WeekdayCode(weekday)) {
			return nil, 0, false
		}
		if len(codes) > 1 && series.Interval > 1 && option.Wkst != rrule.MO {
			return nil, 0, false // weeks starting on another day group the days differently
		}
		if len(codes) > 1 || codes[0] != recurrence.WeekdayCode(weekday) {
			series.ByWeekday = codes
		}
	}
	if len(option.Bymonthday) > 0 && (option.Freq != rrule.MONTHLY && option.Freq != rrule.YEARLY || len(option.Bymonthday) != 1 || option.Bymonthday[0] != wall.Day()) {
		return nil, 0, false
	}
	if len(option.Bymonth) > 0 && (option.Freq != rrule.YEARLY || len(option.Bymonth) != 1 || option.Bymonth[0] != int(wall.Month())) {
		return nil, 0, false
	}
	if option.Freq == rrule.YEARLY && len(option.Bymonthday) > 0 && len(option.Bymonth) == 0 {
		return nil, 0, false // the day in every month
	}
	if series.Count > maxSeriesCount || series.Count > 0 && (rule.Until != nil || rule.UntilWall != nil) {
		return nil, 0, false
	}
	switch {
	case when.allDay && (rule.Until != nil || rule.UntilWall != nil):
		last := m.untilDate(rule)
		if last.Before(when.first) {
			return nil, 0, false
		}
		// The series compares its dates with until's date in the owner's
		// timezone: noon there names that date.
		until := time.Date(last.Year(), last.Month(), last.Day(), 12, 0, 0, 0, m.owner).UTC()
		series.Until = &until
	case rule.Until != nil:
		until := rule.Until.UTC()
		series.Until = &until
	case rule.UntilWall != nil:
		until := recurrence.LocationZone{Location: when.zone}.Instant(*rule.UntilWall)
		series.Until = &until
	}
	if series.Until != nil && !when.allDay && series.Until.Before(when.start) {
		return nil, 0, false
	}
	expansion, err := m.recurrenceSeries(series, base, when, wall)
	if err != nil {
		return nil, 0, false
	}
	exceptions := map[string]sources.Exception{}
	for _, line := range exdates {
		for _, key := range exdateKeys(line, when, wall) {
			exceptions[key] = sources.Exception{Cancelled: true}
		}
	}
	var changed []Event
	keys := map[string]string{} // instance id -> recurrence id
	for _, instance := range instances {
		key, ok := instanceKey(instance, when)
		if !ok {
			return nil, 0, false
		}
		if instance.Cancelled() {
			exceptions[key] = sources.Exception{Cancelled: true}
			continue
		}
		keys[instance.ID] = key
		changed = append(changed, instance)
	}
	wanted := make([]string, 0, len(keys))
	for _, key := range keys {
		wanted = append(wanted, key)
	}
	occurs := occurrenceKeys(expansion, when.allDay, wanted)
	var standalone []sources.Item
	unread := 0
	for _, instance := range changed {
		key := keys[instance.ID]
		own, err := m.item(instance, instance.ID)
		if err != nil {
			unread++
			continue
		}
		if own.AllDay != when.allDay {
			return nil, 0, false // an occurrence cannot switch between timed and all-day
		}
		if !occurs[key] {
			// Not an occurrence of the series as Calendar repeats it: keep
			// it as an event of its own.
			standalone = append(standalone, own)
			continue
		}
		changes := occurrenceChanges(base, own, key, when)
		if changes.Empty() {
			continue
		}
		exceptions[key] = sources.Exception{Changes: changes}
	}
	item := base
	item.Recurrence = series
	if len(exceptions) > 0 {
		item.Exceptions = exceptions
	}
	return append([]sources.Item{item}, standalone...), unread, true
}

// untilDate is an all-day series' last date: a date UNTIL as is, a UTC one
// as its date in the owner's timezone.
func (m mapper) untilDate(rule recurrence.Rule) time.Time {
	if rule.UntilWall != nil {
		wall := *rule.UntilWall
		return time.Date(wall.Year(), wall.Month(), wall.Day(), 0, 0, 0, 0, time.UTC)
	}
	local := rule.Until.In(m.owner)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// recurrenceSeries is the series as Calendar's API repeats it (see the api
// package's series): a timed one on the wall clock of its zone, an all-day
// one by dates with until's date in the owner's timezone.
func (m mapper) recurrenceSeries(rule *sources.Recurrence, base sources.Item, when span, wall time.Time) (recurrence.Series, error) {
	spec := recurrence.Spec{Frequency: rule.Frequency, Interval: rule.Interval, ByWeekday: rule.ByWeekday, Count: rule.Count, Until: rule.Until}
	parsed, err := spec.Rule()
	if err != nil {
		return recurrence.Series{}, err
	}
	if when.allDay {
		if parsed.Until != nil {
			local := parsed.Until.In(m.owner)
			last := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
			parsed.Until, parsed.UntilWall = nil, &last
		}
		days := int(when.last.Sub(when.first).Hours()/24) + 1
		return recurrence.Series{Rule: parsed, AllDay: true, Start: when.first, Days: days, Zone: recurrence.LocationZone{Location: m.owner}}, nil
	}
	return recurrence.Series{Rule: parsed, Start: wall, Duration: base.EndAt.Sub(base.StartAt), Zone: recurrence.LocationZone{Location: when.zone}}, nil
}

// occurrenceKeys reports which of keys are recurrence ids of the series'
// occurrences, iterating no further than the latest of them.
func occurrenceKeys(series recurrence.Series, allDay bool, keys []string) map[string]bool {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out
	}
	wanted := map[string]bool{}
	latest := ""
	for _, key := range keys {
		wanted[key] = true
		if key > latest {
			latest = key
		}
	}
	next, err := series.Iterator()
	if err != nil {
		return out
	}
	for occurrence, ok := next(); ok; occurrence, ok = next() {
		key := recurrenceID(occurrence.Start, occurrence.Wall, allDay)
		if key > latest {
			break
		}
		if wanted[key] {
			out[key] = true
		}
	}
	return out
}

// recurrenceID is an occurrence's id in Calendar: its original start as an
// RFC 3339 instant in UTC, or its date for an all-day series. Both sort in
// time order as text.
func recurrenceID(start, wall time.Time, allDay bool) string {
	if allDay {
		return wall.Format(time.DateOnly)
	}
	return start.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// instanceKey is the recurrence id of a changed or cancelled instance: its
// originalStartTime, of the series' kind.
func instanceKey(instance Event, when span) (string, bool) {
	original := instance.OriginalStartTime
	if original == nil {
		return "", false
	}
	if when.allDay {
		if original.Date == "" {
			return "", false
		}
		day, err := time.Parse(time.DateOnly, original.Date)
		return day.Format(time.DateOnly), err == nil
	}
	start, err := time.Parse(time.RFC3339, original.DateTime)
	if original.DateTime == "" || err != nil {
		return "", false
	}
	return recurrenceID(start, time.Time{}, false), true
}

// occurrenceChanges is what an instance changes of its occurrence as the
// series has it.
func occurrenceChanges(base, own sources.Item, key string, when span) sources.Changes {
	var changes sources.Changes
	if own.Title != base.Title {
		title := own.Title
		changes.Title = &title
	}
	if !sameText(own.Description, base.Description) {
		changes.Description = &sources.Text{Value: own.Description}
	}
	if !sameText(own.Location, base.Location) {
		changes.Location = &sources.Text{Value: own.Location}
	}
	if when.allDay {
		first, _ := time.Parse(time.DateOnly, key)
		last := first.Add(base.EndDate.Sub(base.StartDate))
		if !own.StartDate.Equal(first) || !own.EndDate.Equal(last) {
			start, end := own.StartDate, own.EndDate
			changes.StartDate, changes.EndDate = &start, &end
		}
	} else {
		start, _ := time.Parse(time.RFC3339, key)
		end := start.Add(base.EndAt.Sub(base.StartAt))
		if !own.StartAt.Equal(start) || !own.EndAt.Equal(end) {
			startAt, endAt := own.StartAt, own.EndAt
			changes.StartAt, changes.EndAt = &startAt, &endAt
		}
	}
	if own.Reminders != nil && base.Reminders != nil {
		if !(sources.Reminders{Reminders: own.Reminders.Reminders}).Equal(sources.Reminders{Reminders: base.Reminders.Reminders}) {
			list := own.Reminders.Reminders
			changes.Reminders = &list
		}
		if own.Reminders.UseDefault != base.Reminders.UseDefault {
			use := own.Reminders.UseDefault
			changes.UseDefaultReminders = &use
		}
	}
	return changes
}

// exdateKeys reads an EXDATE line into the recurrence ids it skips.
func exdateKeys(line string, when span, wall time.Time) []string {
	line = cleanLine(line)
	head, values, ok := strings.Cut(line, ":")
	if !ok {
		return nil
	}
	zone := when.zone
	date := false
	for _, param := range strings.Split(head, ";")[1:] {
		name, value, _ := strings.Cut(param, "=")
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "TZID":
			if loc := loadZone(strings.Trim(value, `"`)); loc != nil {
				zone = loc
			}
		case "VALUE":
			date = strings.EqualFold(strings.TrimSpace(value), "DATE")
		}
	}
	var keys []string
	for _, raw := range strings.Split(values, ",") {
		raw = strings.TrimSpace(raw)
		switch {
		case date || len(raw) == 8:
			day, err := time.Parse("20060102", raw[:min(len(raw), 8)])
			if err != nil {
				continue
			}
			if when.allDay {
				keys = append(keys, day.Format(time.DateOnly))
			} else {
				at := time.Date(day.Year(), day.Month(), day.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
				keys = append(keys, recurrenceID(recurrence.LocationZone{Location: when.zone}.Instant(at), time.Time{}, false))
			}
		case strings.HasSuffix(strings.ToUpper(raw), "Z"):
			instant, err := time.Parse("20060102T150405", raw[:len(raw)-1])
			if err != nil {
				continue
			}
			if when.allDay {
				keys = append(keys, instant.Format(time.DateOnly))
			} else {
				keys = append(keys, recurrenceID(instant, time.Time{}, false))
			}
		default:
			local, err := time.Parse("20060102T150405", raw)
			if err != nil {
				continue
			}
			if when.allDay {
				keys = append(keys, local.Format(time.DateOnly))
			} else {
				keys = append(keys, recurrenceID(recurrence.LocationZone{Location: zone}.Instant(local), time.Time{}, false))
			}
		}
	}
	return keys
}

// expand imports a recurring event whose rule Calendar's model cannot hold
// as one read-only event per occurrence from the window start until
// ExpansionHorizon ahead, through the feed parser's recurrence expansion
// (any RRULE, RDATE, and EXDATE). Each is keyed as Google names the instance
// ("<id>_20261012T070000Z", or "<id>_20261012" all-day); a changed instance
// replaces its occurrence and a cancelled one removes it.
func (m mapper) expand(master Event, base sources.Item, when span, instances []Event) ([]sources.Item, int, error) {
	lookback := m.now.Sub(m.windowStart)
	if m.windowStart.IsZero() || lookback <= 0 {
		lookback = ics.DefaultLookback
	}
	feed, err := ics.Parse(vevent(when, master.Recurrence), ics.Options{Owner: when.zoneOr(m.owner), Now: m.now, Lookback: lookback, Horizon: ExpansionHorizon})
	if err != nil {
		return nil, 0, err
	}
	overrides := map[string]Event{}
	var order []string
	for _, instance := range instances {
		key, ok := feedKey(instance, when)
		if !ok {
			continue
		}
		if _, seen := overrides[key]; !seen {
			order = append(order, key)
		}
		overrides[key] = instance
	}
	var items []sources.Item
	unread := 0
	used := map[string]bool{}
	for _, occurrence := range feed.Items {
		_, key, recurring := strings.Cut(occurrence.ExternalUID, "#")
		if !recurring {
			return []sources.Item{base}, 0, nil // no RRULE or RDATE after all
		}
		uid := master.ID + "_" + key
		if instance, ok := overrides[key]; ok {
			used[key] = true
			if instance.Cancelled() {
				continue
			}
			if own, err := m.item(instance, uid); err == nil {
				items = append(items, own)
				continue
			}
			unread++ // the occurrence keeps the series' values
		}
		item := base
		item.ExternalUID = uid
		if when.allDay {
			item.StartDate, item.EndDate = occurrence.StartDate, occurrence.EndDate
		} else {
			item.StartAt, item.EndAt = occurrence.StartAt, occurrence.EndAt
		}
		items = append(items, item)
	}
	// Changed instances outside the window (or that the expansion does not
	// produce) are still events of the calendar.
	for _, key := range order {
		instance := overrides[key]
		if used[key] || instance.Cancelled() {
			continue
		}
		if own, err := m.item(instance, master.ID+"_"+key); err == nil {
			items = append(items, own)
		} else {
			unread++
		}
	}
	return items, unread, nil
}

func (s span) zoneOr(fallback *time.Location) *time.Location {
	if s.zone != nil {
		return s.zone
	}
	return fallback
}

// feedKey is an instance's original start in the feed parser's (and
// Google's instance id) form: 20261012T070000Z, or 20261012 all-day.
func feedKey(instance Event, when span) (string, bool) {
	key, ok := instanceKey(instance, when)
	if !ok {
		return "", false
	}
	if when.allDay {
		day, _ := time.Parse(time.DateOnly, key)
		return day.Format("20060102"), true
	}
	start, _ := time.Parse(time.RFC3339, key)
	return start.UTC().Format("20060102T150405Z"), true
}

// vevent is a one-event iCalendar document holding the recurring event's
// start, end, and recurrence lines, for the feed parser to expand.
func vevent(when span, lines []string) []byte {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Life OS//Google Calendar sync//EN\r\nBEGIN:VEVENT\r\nUID:google\r\n")
	if when.allDay {
		fmt.Fprintf(&b, "DTSTART;VALUE=DATE:%s\r\nDTEND;VALUE=DATE:%s\r\n", when.first.Format("20060102"), when.last.AddDate(0, 0, 1).Format("20060102"))
	} else {
		zone := when.zone
		fmt.Fprintf(&b, "DTSTART;TZID=%s:%s\r\nDTEND;TZID=%s:%s\r\n", zone.String(), when.start.In(zone).Format("20060102T150405"),
			zone.String(), when.end.In(zone).Format("20060102T150405"))
	}
	for _, line := range lines {
		switch propertyName(line) {
		case "RRULE", "RDATE", "EXDATE", "EXRULE":
			b.WriteString(cleanLine(line))
			b.WriteString("\r\n")
		}
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return []byte(b.String())
}

// propertyName is an iCalendar line's property name, upper case.
func propertyName(line string) string {
	line = cleanLine(line)
	end := strings.IndexAny(line, ";:")
	if end < 0 {
		return strings.ToUpper(line)
	}
	return strings.ToUpper(line[:end])
}

// cleanLine drops line breaks, so a recurrence line stays one line.
func cleanLine(line string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "").Replace(line))
}

var (
	htmlTag    = regexp.MustCompile(`(?i)</?(br|p|div|span|a|b|i|u|ul|ol|li|strong|em|h[1-6]|table|tr|td)\b[^>]*>`)
	htmlAnchor = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*"([^"]*)"[^>]*>(.*?)</a>`)
	htmlBreak  = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|h[1-6]|tr)>`)
	htmlAny    = regexp.MustCompile(`(?s)<[^>]*>`)
	blankLines = regexp.MustCompile(`\n[ \t]*\n(\s*\n)+`)
)

// notes is an event's description as plain text. Google Calendar keeps
// descriptions edited on the web as HTML; tags are dropped (a link keeps
// its address), line breaks kept, and entities decoded.
func notes(description string) string {
	text := strings.ReplaceAll(description, "\r\n", "\n")
	if htmlTag.MatchString(text) {
		text = htmlAnchor.ReplaceAllStringFunc(text, func(anchor string) string {
			parts := htmlAnchor.FindStringSubmatch(anchor)
			label := strings.TrimSpace(htmlAny.ReplaceAllString(parts[2], ""))
			address := html.UnescapeString(parts[1])
			if label == "" || html.UnescapeString(label) == address {
				return address
			}
			return label + " (" + address + ")"
		})
		text = htmlBreak.ReplaceAllString(text, "\n")
		text = htmlAny.ReplaceAllString(text, "")
		text = html.UnescapeString(text)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(text, "\n\n"))
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func clipRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func sameText(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}
