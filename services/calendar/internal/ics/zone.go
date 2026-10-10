package ics

import (
	"strings"
	"time"

	ical "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"

	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
)

// zone is a resolved TZID: how its wall-clock times map to instants, and
// its IANA name when it has one (the event's timeZone display hint).
type zone struct {
	recurrence.Zone
	iana string
}

func (z zone) ianaName() *string {
	if z.iana == "" {
		return nil
	}
	name := z.iana
	return &name
}

// zones resolves the TZIDs of one feed. A TZID is resolved, in order, as an
// IANA name; through its VTIMEZONE's X-LIC-LOCATION; as a Windows zone name
// (Outlook/Exchange); as an IANA name at the end of a longer identifier
// ("/mozilla.org/20050126_1/Europe/Berlin"); and finally from the rules of
// its VTIMEZONE definition. Unknown TZIDs fall back to the floating zone.
type zones struct {
	definitions map[string]*ical.VTimezone
	floating    zone
	resolved    map[string]*zone
}

func newZones(calendar *ical.Calendar, owner *time.Location) *zones {
	z := &zones{definitions: map[string]*ical.VTimezone{}, resolved: map[string]*zone{}}
	floating := owner
	// X-WR-TIMEZONE (Google, Apple) names the calendar's default zone.
	for _, property := range calendar.CalendarProperties {
		if strings.EqualFold(property.IANAToken, "X-WR-TIMEZONE") {
			if loc := loadIANA(property.Value); loc != nil {
				floating = loc
			}
		}
	}
	z.floating = zone{Zone: recurrence.LocationZone{Location: floating}}
	for _, definition := range calendar.Timezones() {
		if id := property(&definition.ComponentBase, "TZID"); id != nil {
			z.definitions[cleanTZID(id.Value)] = definition
		}
	}
	return z
}

func cleanTZID(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"`)
}

// loadIANA loads an IANA zone by name; "Local" and "" are not names here.
func loadIANA(name string) *time.Location {
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

// lookup resolves tzid; ok is false when it cannot be resolved.
func (z *zones) lookup(tzid string) (zone, bool) {
	tzid = cleanTZID(tzid)
	if cached, seen := z.resolved[tzid]; seen {
		if cached == nil {
			return zone{}, false
		}
		return *cached, true
	}
	resolved := z.resolve(tzid)
	z.resolved[tzid] = resolved
	if resolved == nil {
		return zone{}, false
	}
	return *resolved, true
}

func (z *zones) resolve(tzid string) *zone {
	iana := func(loc *time.Location) *zone {
		return &zone{Zone: recurrence.LocationZone{Location: loc}, iana: loc.String()}
	}
	if loc := loadIANA(tzid); loc != nil {
		return iana(loc)
	}
	definition := z.definitions[tzid]
	if definition != nil {
		for _, property := range definition.Properties {
			if strings.EqualFold(property.IANAToken, "X-LIC-LOCATION") {
				if loc := loadIANA(property.Value); loc != nil {
					return iana(loc)
				}
			}
		}
	}
	if loc := ical.WindowsTimezoneToIANA(tzid); loc != nil {
		return iana(loc)
	}
	if segments := strings.Split(tzid, "/"); len(segments) > 2 {
		for index := 1; index < len(segments)-1; index++ {
			if loc := loadIANA(strings.Join(segments[index:], "/")); loc != nil && strings.Contains(loc.String(), "/") {
				return iana(loc)
			}
		}
	}
	if definition != nil {
		if rules := definitionZone(definition); rules != nil {
			return &zone{Zone: rules}
		}
	}
	return nil
}

// observance is one STANDARD or DAYLIGHT part of a VTIMEZONE.
type observance struct {
	start    time.Time // wall-clock onset, in the offset before it
	offsetTo time.Duration
	rule     *rrule.RRule
	dates    []time.Time
}

// latestOnset is the observance's last onset at or before wall.
func (o observance) latestOnset(wall time.Time) (time.Time, bool) {
	var best time.Time
	found := false
	consider := func(onset time.Time) {
		if !onset.IsZero() && !onset.After(wall) && (!found || onset.After(best)) {
			best, found = onset, true
		}
	}
	consider(o.start)
	if o.rule != nil {
		consider(o.rule.Before(wall, true))
	}
	for _, date := range o.dates {
		consider(date)
	}
	return best, found
}

// rulesZone applies a VTIMEZONE's observances: the offset in force at a
// wall-clock time is that of the observance with the latest onset before it.
type rulesZone struct{ observances []observance }

func (z rulesZone) Instant(wall time.Time) time.Time {
	var onset time.Time
	offset, found := z.observances[0].offsetTo, false
	for _, item := range z.observances {
		if candidate, ok := item.latestOnset(wall); ok && (!found || candidate.After(onset)) {
			onset, offset, found = candidate, item.offsetTo, true
		}
	}
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC).Add(-offset)
}

func definitionZone(definition *ical.VTimezone) recurrence.Zone {
	var observances []observance
	for _, component := range definition.Components {
		var base *ical.ComponentBase
		switch part := component.(type) {
		case *ical.Standard:
			base = &part.ComponentBase
		case *ical.Daylight:
			base = &part.ComponentBase
		default:
			continue
		}
		item, ok := parseObservance(base)
		if ok {
			observances = append(observances, item)
		}
	}
	if len(observances) == 0 {
		return nil
	}
	return rulesZone{observances: observances}
}

func parseObservance(base *ical.ComponentBase) (observance, bool) {
	start := property(base, "DTSTART")
	offset := property(base, "TZOFFSETTO")
	if start == nil || offset == nil {
		return observance{}, false
	}
	value, err := parseDateValue(start.Value, start.ICalParameters)
	if err != nil {
		return observance{}, false
	}
	offsetTo, ok := parseUTCOffset(offset.Value)
	if !ok {
		return observance{}, false
	}
	item := observance{start: value.wall, offsetTo: offsetTo}
	if ruleProperty := property(base, "RRULE"); ruleProperty != nil {
		if rule, err := ParseRule(ruleProperty.Value); err == nil {
			option := rule.Option
			option.Dtstart = value.wall
			if rule.UntilWall != nil {
				option.Until = *rule.UntilWall
			} else if rule.Until != nil {
				option.Until = *rule.Until
			}
			if parsed, err := rrule.NewRRule(option); err == nil {
				item.rule = parsed
			}
		}
	}
	for _, rdate := range properties(base, "RDATE") {
		for _, raw := range strings.Split(rdate.Value, ",") {
			if date, err := parseDateValue(raw, rdate.ICalParameters); err == nil {
				item.dates = append(item.dates, date.wall)
			}
		}
	}
	return item, true
}

// parseUTCOffset reads a UTC-OFFSET value such as +0100 or -023000.
func parseUTCOffset(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if len(value) != 5 && len(value) != 7 {
		return 0, false
	}
	sign := time.Duration(1)
	switch value[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	digits := func(text string) (time.Duration, bool) {
		if len(text) != 2 || text[0] < '0' || text[0] > '9' || text[1] < '0' || text[1] > '9' {
			return 0, false
		}
		return time.Duration(text[0]-'0')*10 + time.Duration(text[1]-'0'), true
	}
	hours, ok1 := digits(value[1:3])
	minutes, ok2 := digits(value[3:5])
	seconds, ok3 := time.Duration(0), true
	if len(value) == 7 {
		seconds, ok3 = digits(value[5:7])
	}
	if !ok1 || !ok2 || !ok3 {
		return 0, false
	}
	return sign * (hours*time.Hour + minutes*time.Minute + seconds*time.Second), true
}
