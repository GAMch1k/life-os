package ics

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	ical "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"

	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
)

// Normalization limits and defaults.
const (
	DefaultLookback   = 365 * 24 * time.Hour
	DefaultHorizon    = 365 * 24 * time.Hour
	DefaultMaxItems   = 10000
	maxPerSeries      = 1000
	maxTitleRunes     = 500
	maxLocationRunes  = 500
	maxNotesRunes     = 10000
	untitled          = "Untitled event"
	instantEventSpan  = time.Minute
	recurrenceKeyTime = "20060102T150405Z"
	recurrenceKeyDate = "20060102"
)

// Options control normalization.
type Options struct {
	// Owner is the zone of floating times (unless the feed names one with
	// X-WR-TIMEZONE).
	Owner *time.Location
	// Now anchors the window in which recurring events are expanded:
	// occurrences starting from Now-Lookback until Now+Horizon.
	Now               time.Time
	Lookback, Horizon time.Duration
	// MaxItems bounds the events of one feed; a larger feed fails.
	MaxItems int
}

// Item is one feed event normalized for Calendar: a VEVENT, or one
// occurrence of a recurring VEVENT.
type Item struct {
	// ExternalUID is the event's identity in the feed: its UID, or for an
	// occurrence of a recurring event "<UID>#<RECURRENCE-ID>" with the
	// occurrence's original start as 20261017T040000Z (UTC) or 20261017.
	ExternalUID string
	Title       string
	Description *string
	Location    *string
	AllDay      bool
	// StartAt/EndAt are a timed event's instants (UTC).
	StartAt, EndAt time.Time
	// StartDate/EndDate are an all-day event's first and last (inclusive)
	// days, at UTC midnight.
	StartDate, EndDate time.Time
	// TimeZone is the IANA zone a timed event is scheduled in, when known.
	TimeZone *string
	// Cancelled: the feed marks the event STATUS:CANCELLED.
	Cancelled  bool
	Categories []string
}

// Start is the item's start instant; an all-day event starts at midnight of
// its first day in loc.
func (i Item) Start(loc *time.Location) time.Time {
	if i.AllDay {
		return time.Date(i.StartDate.Year(), i.StartDate.Month(), i.StartDate.Day(), 0, 0, 0, 0, loc)
	}
	return i.StartAt
}

// Feed is a parsed feed.
type Feed struct {
	Items []Item
	// Components is the number of VEVENTs in the feed.
	Components int
	// Skipped counts VEVENTs (or occurrences) that could not be read, such
	// as one without a start, or a repeated UID.
	Skipped int
}

// Parse reads an iCalendar feed. It fails only when the document as a whole
// is not a complete iCalendar object; unreadable events are skipped.
func Parse(body []byte, options Options) (Feed, error) {
	if options.Owner == nil {
		options.Owner = time.UTC
	}
	if options.Lookback <= 0 {
		options.Lookback = DefaultLookback
	}
	if options.Horizon <= 0 {
		options.Horizon = DefaultHorizon
	}
	if options.MaxItems <= 0 {
		options.MaxItems = DefaultMaxItems
	}
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimSpace(body)
	if !hasPrefixFold(trimmed, "BEGIN:VCALENDAR") {
		return Feed{}, failure("the response is not an iCalendar feed")
	}
	// A cut-off download would otherwise parse as a shorter feed, and every
	// event after the cut would look removed.
	if !hasSuffixFold(trimmed, "END:VCALENDAR") {
		return Feed{}, failure("the feed is incomplete (no END:VCALENDAR)")
	}
	if !utf8.Valid(trimmed) {
		trimmed = []byte(strings.ToValidUTF8(string(trimmed), "�"))
	}
	calendar, err := ical.ParseCalendarWithOptions(bytes.NewReader(trimmed),
		ical.WithPropertyParser(ical.FallbackParser(ical.LooseParser)),
		ical.WithUnknownPropertyHandler(ical.AcceptUnknownPropertyHandler))
	if err != nil {
		var malformed *ical.MalformedError
		if errors.As(err, &malformed) && malformed.HasLine {
			return Feed{}, failure("the feed could not be parsed (line %d)", malformed.Line)
		}
		return Feed{}, failure("the feed could not be parsed")
	}
	p := parser{options: options, zones: newZones(calendar, options.Owner), seen: map[string]bool{}}
	return p.feed(calendar)
}

func hasPrefixFold(value []byte, prefix string) bool {
	return len(value) >= len(prefix) && strings.EqualFold(string(value[:len(prefix)]), prefix)
}

func hasSuffixFold(value []byte, suffix string) bool {
	return len(value) >= len(suffix) && strings.EqualFold(string(value[len(value)-len(suffix):]), suffix)
}

type parser struct {
	options Options
	zones   *zones
	seen    map[string]bool
	feedOut Feed
}

// group is the VEVENTs sharing a UID: the master and its overrides.
type group struct {
	uid       string
	master    *ical.VEvent
	overrides []*ical.VEvent
}

func (p *parser) feed(calendar *ical.Calendar) (Feed, error) {
	var groups []*group
	byUID := map[string]*group{}
	for _, event := range calendar.Events() {
		p.feedOut.Components++
		uid := text(&event.ComponentBase, "UID")
		if uid == "" {
			uid = syntheticUID(&event.ComponentBase)
		}
		g := byUID[uid]
		if g == nil {
			g = &group{uid: uid}
			byUID[uid] = g
			groups = append(groups, g)
		}
		switch {
		case property(&event.ComponentBase, "RECURRENCE-ID") != nil:
			g.overrides = append(g.overrides, event)
		case g.master == nil:
			g.master = event
		default:
			p.feedOut.Skipped++ // the same UID twice: keep the first
		}
	}
	for _, g := range groups {
		if err := p.group(g); err != nil {
			return Feed{}, err
		}
	}
	return p.feedOut, nil
}

// syntheticUID keys an event without a UID by its start and summary, so it
// stays stable as long as those do.
func syntheticUID(base *ical.ComponentBase) string {
	start := ""
	if dtstart := property(base, "DTSTART"); dtstart != nil {
		start = dtstart.Value
	}
	sum := sha1.Sum([]byte(start + "|" + text(base, "SUMMARY")))
	return "nouid-" + hex.EncodeToString(sum[:8])
}

func (p *parser) add(item Item) error {
	if p.seen[item.ExternalUID] {
		p.feedOut.Skipped++
		return nil
	}
	if len(p.feedOut.Items) >= p.options.MaxItems {
		return failure("the feed has more than %d events", p.options.MaxItems)
	}
	p.seen[item.ExternalUID] = true
	p.feedOut.Items = append(p.feedOut.Items, item)
	return nil
}

func (p *parser) group(g *group) error {
	overrides := map[string]*ical.VEvent{}
	overrideDates := map[string]string{} // an override whose RECURRENCE-ID is a date, by date
	var overrideOrder []string
	for _, override := range g.overrides {
		value, err := dateProperty(&override.ComponentBase, "RECURRENCE-ID")
		if err != nil {
			p.feedOut.Skipped++
			continue
		}
		key := p.recurrenceKey(value)
		if _, duplicate := overrides[key]; duplicate {
			p.feedOut.Skipped++
			continue
		}
		overrides[key] = override
		overrideOrder = append(overrideOrder, key)
		if value.date {
			overrideDates[value.wall.Format(recurrenceKeyDate)] = key
		}
	}
	used := map[string]bool{}
	if g.master != nil {
		base, err := p.event(&g.master.ComponentBase)
		if err != nil {
			p.feedOut.Skipped++
		} else if !base.recurring {
			base.item.ExternalUID = g.uid
			if err := p.add(base.item); err != nil {
				return err
			}
		} else {
			instances, err := p.expand(base)
			if err != nil {
				p.feedOut.Skipped++
			}
			for _, instance := range instances {
				key := instance.key
				override := overrides[key]
				if override == nil {
					if dateKey, ok := overrideDates[instance.wall.Format(recurrenceKeyDate)]; ok && !used[dateKey] {
						key, override = dateKey, overrides[dateKey]
					}
				}
				if override != nil {
					used[key] = true
					if err := p.addOverride(g.uid, instance.key, override); err != nil {
						return err
					}
					continue
				}
				instance.item.ExternalUID = g.uid + "#" + instance.key
				if err := p.add(instance.item); err != nil {
					return err
				}
			}
		}
	}
	// Overrides of occurrences outside the window (or without a master in
	// the feed) are still events of the feed.
	for _, key := range overrideOrder {
		if !used[key] {
			if err := p.addOverride(g.uid, key, overrides[key]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *parser) addOverride(uid, key string, override *ical.VEvent) error {
	parsed, err := p.event(&override.ComponentBase)
	if err != nil {
		p.feedOut.Skipped++
		return nil
	}
	parsed.item.ExternalUID = uid + "#" + key
	return p.add(parsed.item)
}

// recurrenceKey is the RECURRENCE-ID form of an original start.
func (p *parser) recurrenceKey(value dateValue) string {
	if value.date {
		return value.wall.Format(recurrenceKeyDate)
	}
	instant, _ := p.instant(value)
	return instant.UTC().Format(recurrenceKeyTime)
}

// parsedEvent is a VEVENT normalized as a single event, plus what its
// expansion needs when it recurs.
type parsedEvent struct {
	item      Item
	recurring bool
	start     dateValue
	zone      recurrence.Zone
	duration  time.Duration
	days      int
	base      *ical.ComponentBase
}

func (p *parser) event(base *ical.ComponentBase) (parsedEvent, error) {
	start, err := dateProperty(base, "DTSTART")
	if err != nil {
		return parsedEvent{}, err
	}
	item := Item{
		Title:       clip(oneLine(text(base, "SUMMARY")), maxTitleRunes),
		Description: optional(clip(strings.TrimSpace(text(base, "DESCRIPTION")), maxNotesRunes)),
		Location:    optional(clip(oneLine(text(base, "LOCATION")), maxLocationRunes)),
		AllDay:      start.date,
		Cancelled:   strings.EqualFold(strings.TrimSpace(text(base, "STATUS")), "CANCELLED"),
		Categories:  categories(base),
	}
	if item.Title == "" {
		item.Title = untitled
	}
	parsed := parsedEvent{start: start, base: base}
	parsed.recurring = property(base, "RRULE") != nil || property(base, "RDATE") != nil
	if start.date {
		days := 1
		if end, err := dateProperty(base, "DTEND"); err == nil {
			if span := int(end.wall.Sub(start.wall).Hours() / 24); span > 1 {
				days = span
			}
		} else if duration := property(base, "DURATION"); duration != nil {
			if weeks, d, rest, ok := parseDuration(duration.Value); ok && weeks*7+d > 1 && rest >= 0 {
				days = weeks*7 + d
			}
		}
		item.StartDate, item.EndDate = start.wall, start.wall.AddDate(0, 0, days-1)
		parsed.days = days
		parsed.zone = recurrence.LocationZone{Location: time.UTC}
		parsed.item = item
		return parsed, nil
	}
	startAt, startZone := p.instant(start)
	item.StartAt, item.TimeZone = startAt, startZone.ianaName()
	endAt := time.Time{}
	if end, err := dateProperty(base, "DTEND"); err == nil {
		endAt, _ = p.instant(end)
	} else if duration := property(base, "DURATION"); duration != nil {
		if weeks, days, rest, ok := parseDuration(duration.Value); ok {
			// Days are nominal (the same wall-clock time days later);
			// hours, minutes, and seconds are exact.
			endAt = startZone.Instant(start.wall.AddDate(0, 0, weeks*7+days)).Add(rest)
		}
	}
	if !endAt.After(startAt) {
		endAt = startAt.Add(instantEventSpan)
	}
	item.EndAt = endAt
	parsed.duration = endAt.Sub(startAt)
	parsed.zone = startZone
	parsed.item = item
	return parsed, nil
}

// instance is one occurrence of a recurring VEVENT.
type instance struct {
	key  string
	wall time.Time
	item Item
}

func (p *parser) expand(event parsedEvent) ([]instance, error) {
	windowStart := p.options.Now.Add(-p.options.Lookback)
	windowEnd := p.options.Now.Add(p.options.Horizon)
	series := recurrence.Series{Start: event.start.wall, AllDay: event.item.AllDay, Duration: event.duration, Days: event.days, Zone: event.zone}
	var occurrences []recurrence.Occurrence
	if rule := property(event.base, "RRULE"); rule != nil {
		parsedRule, err := parseRule(rule.Value)
		if err != nil {
			return nil, err
		}
		series.Rule = parsedRule
		found, err := series.Between(windowStart, windowEnd, maxPerSeries, nil)
		if err != nil {
			return nil, err
		}
		occurrences = found
	} else {
		// RDATE alone: the master's own start is the first occurrence.
		single := series
		single.Rule = recurrence.Rule{Option: rrule.ROption{Freq: rrule.DAILY, Count: 1}}
		occurrences, _ = single.Between(windowStart, windowEnd, 1, nil)
	}
	for _, property := range properties(event.base, "RDATE") {
		for _, raw := range strings.Split(property.Value, ",") {
			value, err := parseDateValue(strings.SplitN(raw, "/", 2)[0], property.ICalParameters)
			if err != nil {
				continue
			}
			wall := value.wall
			if !value.date && !event.item.AllDay && (value.utc || value.tzid != "") {
				// Bring an RDATE in another zone onto the series' clock.
				instant, _ := p.instant(value)
				wall = wallIn(instant, event.zone, wall)
			}
			occurrence := series.At(wall)
			if occurrence.End.After(windowStart) && occurrence.Start.Before(windowEnd) {
				occurrences = append(occurrences, occurrence)
			}
		}
	}
	excluded := map[string]bool{}
	excludedDates := map[string]bool{}
	for _, property := range properties(event.base, "EXDATE") {
		for _, raw := range strings.Split(property.Value, ",") {
			value, err := parseDateValue(raw, property.ICalParameters)
			if err != nil {
				continue
			}
			if value.date || event.item.AllDay {
				excludedDates[value.wall.Format(recurrenceKeyDate)] = true
			} else {
				excluded[p.recurrenceKey(value)] = true
			}
		}
	}
	sort.SliceStable(occurrences, func(a, b int) bool { return occurrences[a].Start.Before(occurrences[b].Start) })
	var instances []instance
	for _, occurrence := range occurrences {
		key := occurrence.Wall.Format(recurrenceKeyDate)
		if !event.item.AllDay {
			key = occurrence.Start.UTC().Format(recurrenceKeyTime)
		}
		if excluded[key] || excludedDates[occurrence.Wall.Format(recurrenceKeyDate)] {
			continue
		}
		item := event.item
		if item.AllDay {
			item.StartDate, item.EndDate = occurrence.Wall, occurrence.LastDay
		} else {
			item.StartAt, item.EndAt = occurrence.Start, occurrence.End
		}
		instances = append(instances, instance{key: key, wall: occurrence.Wall, item: item})
	}
	return instances, nil
}

// wallIn is instant's wall clock in z (fallback when z is not location-based).
func wallIn(instant time.Time, z recurrence.Zone, fallback time.Time) time.Time {
	switch typed := z.(type) {
	case recurrence.LocationZone:
		return recurrence.Wall(instant, typed.Location)
	case zone:
		return wallIn(instant, typed.Zone, fallback)
	}
	return fallback
}

// dateValue is a DATE or DATE-TIME property value.
type dateValue struct {
	// wall is the value's date and wall-clock time, carried in UTC.
	wall time.Time
	date bool
	utc  bool
	tzid string
}

func dateProperty(base *ical.ComponentBase, name string) (dateValue, error) {
	prop := property(base, name)
	if prop == nil {
		return dateValue{}, fmt.Errorf("no %s", name)
	}
	return parseDateValue(prop.Value, prop.ICalParameters)
}

func parseDateValue(raw string, params map[string][]string) (dateValue, error) {
	raw = strings.TrimSpace(raw)
	value := dateValue{tzid: parameter(params, "TZID")}
	if strings.HasSuffix(raw, "Z") || strings.HasSuffix(raw, "z") {
		value.utc, raw = true, raw[:len(raw)-1]
		value.tzid = ""
	}
	layouts := []string{"20060102T150405", "20060102T1504"}
	if len(raw) == 8 || strings.EqualFold(parameter(params, "VALUE"), "DATE") {
		value.date, value.utc, value.tzid = true, false, ""
		layouts = []string{"20060102"}
		if len(raw) > 8 {
			raw = raw[:8]
		}
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			value.wall = parsed
			return value, nil
		}
	}
	return dateValue{}, fmt.Errorf("unreadable date %q", raw)
}

// instant resolves a DATE-TIME value, with the zone it is in: UTC as is, a
// TZID through the feed's zones, a floating time in the floating zone.
func (p *parser) instant(value dateValue) (time.Time, zone) {
	switch {
	case value.utc:
		return value.wall, zone{Zone: recurrence.LocationZone{Location: time.UTC}}
	case value.tzid != "":
		if found, ok := p.zones.lookup(value.tzid); ok {
			return found.Instant(value.wall), found
		}
	}
	floating := zone{Zone: p.zones.floating.Zone}
	return floating.Instant(value.wall), floating
}

// parseRule reads an RRULE value. UNTIL is kept out of the rrule options:
// a UTC UNTIL bounds occurrence instants, a floating or date one their wall
// clock (a date includes its whole day). Unknown parts are ignored.
func parseRule(value string) (recurrence.Rule, error) {
	var parts []string
	var rule recurrence.Rule
	known := map[string]bool{"FREQ": true, "INTERVAL": true, "COUNT": true, "WKST": true, "BYSETPOS": true, "BYMONTH": true,
		"BYMONTHDAY": true, "BYYEARDAY": true, "BYWEEKNO": true, "BYDAY": true, "BYHOUR": true, "BYMINUTE": true, "BYSECOND": true}
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(value), "RRULE:"), ";") {
		key, val, ok := strings.Cut(part, "=")
		key = strings.ToUpper(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if !ok || val == "" {
			continue
		}
		if key == "UNTIL" {
			until, err := parseDateValue(val, nil)
			if err != nil {
				return recurrence.Rule{}, err
			}
			switch {
			case until.utc:
				rule.Until = &until.wall
			case until.date:
				end := until.wall.Add(24*time.Hour - time.Second)
				rule.UntilWall = &end
			default:
				rule.UntilWall = &until.wall
			}
			continue
		}
		if known[key] {
			parts = append(parts, key+"="+strings.ToUpper(val))
		}
	}
	option, err := rrule.StrToROption(strings.Join(parts, ";"))
	if err != nil {
		return recurrence.Rule{}, err
	}
	rule.Option = *option
	return rule, nil
}

// parseDuration reads an RFC 5545 duration such as P1D, PT1H30M, or P2W
// into nominal weeks and days plus an exact remainder.
func parseDuration(value string) (weeks, days int, rest time.Duration, ok bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	sign := 1
	if strings.HasPrefix(value, "-") {
		sign, value = -1, value[1:]
	}
	value = strings.TrimPrefix(value, "+")
	if !strings.HasPrefix(value, "P") || len(value) < 3 {
		return 0, 0, 0, false
	}
	inTime := false
	number := ""
	for _, r := range value[1:] {
		switch {
		case r >= '0' && r <= '9':
			number += string(r)
			continue
		case r == 'T':
			inTime = true
			continue
		}
		n, err := strconv.Atoi(number)
		if err != nil {
			return 0, 0, 0, false
		}
		number = ""
		switch {
		case r == 'W' && !inTime:
			weeks = n
		case r == 'D' && !inTime:
			days = n
		case r == 'H' && inTime:
			rest += time.Duration(n) * time.Hour
		case r == 'M' && inTime:
			rest += time.Duration(n) * time.Minute
		case r == 'S' && inTime:
			rest += time.Duration(n) * time.Second
		default:
			return 0, 0, 0, false
		}
	}
	if number != "" {
		return 0, 0, 0, false
	}
	return sign * weeks, sign * days, time.Duration(sign) * rest, true
}

// property returns a component's first property named name (any case).
func property(base *ical.ComponentBase, name string) *ical.IANAProperty {
	for index := range base.Properties {
		if strings.EqualFold(base.Properties[index].IANAToken, name) {
			return &base.Properties[index]
		}
	}
	return nil
}

func properties(base *ical.ComponentBase, name string) []*ical.IANAProperty {
	var found []*ical.IANAProperty
	for index := range base.Properties {
		if strings.EqualFold(base.Properties[index].IANAToken, name) {
			found = append(found, &base.Properties[index])
		}
	}
	return found
}

func parameter(params map[string][]string, name string) string {
	for key, values := range params {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return strings.Trim(values[0], `"`)
		}
	}
	return ""
}

func text(base *ical.ComponentBase, name string) string {
	if prop := property(base, name); prop != nil {
		return prop.Value
	}
	return ""
}

func categories(base *ical.ComponentBase) []string {
	var out []string
	for _, prop := range properties(base, "CATEGORIES") {
		for _, value := range strings.Split(prop.Value, ",") {
			if value = strings.TrimSpace(value); value != "" {
				out = append(out, value)
			}
		}
	}
	return out
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func clip(value string, limit int) string {
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
