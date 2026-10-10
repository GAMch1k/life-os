// Package recurrence expands recurring events into occurrences with
// iCalendar (RFC 5545) rules. Rules run on wall-clock times (a time.Time in
// UTC whose fields are the local date and clock), which a Zone turns into
// instants, so an event keeps its local time across daylight saving changes.
// The API's recurring events and the integrations' recurring events (stored
// as a series) use it.
package recurrence

import (
	"fmt"
	"slices"
	"time"

	"github.com/teambition/rrule-go"
)

// MaxSteps bounds the rule iterations of one expansion, so a rule that
// starts decades ago or never produces an occurrence cannot stall a request.
const MaxSteps = 100000

// Zone resolves wall-clock times to instants.
type Zone interface {
	// Instant is the moment at which the zone's clocks show wall.
	Instant(wall time.Time) time.Time
}

// LocationZone is a Zone backed by an IANA location.
type LocationZone struct{ Location *time.Location }

func (z LocationZone) Instant(wall time.Time) time.Time {
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, z.Location).UTC()
}

// Wall is the wall clock of instant in loc, carried in UTC.
func Wall(instant time.Time, loc *time.Location) time.Time {
	local := instant.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
}

// Rule is a recurrence rule. Option holds FREQ, INTERVAL, COUNT, WKST, and
// the BY* parts; its Dtstart and Until are ignored (the series gives the
// start). Until bounds occurrence starts as an instant and UntilWall as a
// wall-clock time; both are inclusive.
type Rule struct {
	Option    rrule.ROption
	Until     *time.Time
	UntilWall *time.Time
}

// SimpleRule is the rule of the API's simplest Recurrence: every interval
// days, weeks, months, or years until an optional instant.
func SimpleRule(frequency string, interval int, until *time.Time) (Rule, error) {
	return Spec{Frequency: frequency, Interval: interval, Until: until}.Rule()
}

// Spec is the API's Recurrence: every Interval days, weeks, months, or years
// from the series' start; a weekly rule on each of ByWeekday (iCalendar day
// codes, MO to SU) when given; ending after Count occurrences (0: no count)
// or at Until (an inclusive instant), or never.
type Spec struct {
	Frequency string
	Interval  int
	ByWeekday []string
	Count     int
	Until     *time.Time
}

// weekdays maps iCalendar day codes to rule weekdays, in week order.
var weekdays = []struct {
	code    string
	weekday time.Weekday
	rule    rrule.Weekday
}{
	{"MO", time.Monday, rrule.MO}, {"TU", time.Tuesday, rrule.TU}, {"WE", time.Wednesday, rrule.WE},
	{"TH", time.Thursday, rrule.TH}, {"FR", time.Friday, rrule.FR}, {"SA", time.Saturday, rrule.SA}, {"SU", time.Sunday, rrule.SU},
}

// WeekdayCode is the iCalendar code of day, such as "MO".
func WeekdayCode(day time.Weekday) string {
	for _, entry := range weekdays {
		if entry.weekday == day {
			return entry.code
		}
	}
	return ""
}

// NormalizeWeekdays returns codes in week order, each once, and false if
// one of them is not a day code.
func NormalizeWeekdays(codes []string) ([]string, bool) {
	seen := map[string]bool{}
	for _, code := range codes {
		seen[code] = true
	}
	out := make([]string, 0, len(seen))
	for _, entry := range weekdays {
		if seen[entry.code] {
			out = append(out, entry.code)
			delete(seen, entry.code)
		}
	}
	if len(seen) > 0 {
		return nil, false
	}
	return out, true
}

// Rule is the spec as a Rule.
func (s Spec) Rule() (Rule, error) {
	frequencies := map[string]rrule.Frequency{"daily": rrule.DAILY, "weekly": rrule.WEEKLY, "monthly": rrule.MONTHLY, "yearly": rrule.YEARLY}
	freq, ok := frequencies[s.Frequency]
	if !ok {
		return Rule{}, fmt.Errorf("unknown recurrence frequency %q", s.Frequency)
	}
	if s.Interval < 1 {
		return Rule{}, fmt.Errorf("recurrence interval must be at least 1")
	}
	if s.Count < 0 {
		return Rule{}, fmt.Errorf("recurrence count must not be negative")
	}
	option := rrule.ROption{Freq: freq, Interval: s.Interval, Count: s.Count}
	if len(s.ByWeekday) > 0 {
		if freq != rrule.WEEKLY {
			return Rule{}, fmt.Errorf("byWeekday is only for weekly recurrence")
		}
		if _, ok := NormalizeWeekdays(s.ByWeekday); !ok {
			return Rule{}, fmt.Errorf("unknown weekday in %v", s.ByWeekday)
		}
		for _, entry := range weekdays {
			if slices.Contains(s.ByWeekday, entry.code) {
				option.Byweekday = append(option.Byweekday, entry.rule)
			}
		}
	}
	return Rule{Option: option, Until: s.Until}, nil
}

// Series is a recurring event.
type Series struct {
	Rule Rule
	// Start is the first occurrence's wall-clock start; for an all-day
	// event, its first day at midnight.
	Start  time.Time
	AllDay bool
	// Duration is the length of a timed occurrence.
	Duration time.Duration
	// Days is the number of days of an all-day occurrence (at least 1).
	Days int
	// Zone resolves wall-clock times: the zone a timed event is scheduled
	// in, or the zone whose days an all-day event's dates are.
	Zone Zone
}

// Occurrence is one occurrence of a series.
type Occurrence struct {
	// Wall is its wall-clock start (an all-day occurrence's first day at
	// midnight).
	Wall time.Time
	// LastDay is an all-day occurrence's last day (at midnight).
	LastDay time.Time
	// Start and End are instants. An all-day occurrence runs from midnight
	// of its first day to midnight after its last day in the series' zone.
	Start, End time.Time
}

// Iterator returns the series' occurrences in order of start; ok is false
// once they run out (or after MaxSteps rule iterations).
func (s Series) Iterator() (func() (Occurrence, bool), error) {
	option := s.Rule.Option
	option.Dtstart = time.Date(s.Start.Year(), s.Start.Month(), s.Start.Day(), s.Start.Hour(), s.Start.Minute(), s.Start.Second(), 0, time.UTC)
	option.Until = time.Time{}
	rule, err := rrule.NewRRule(option)
	if err != nil {
		return nil, fmt.Errorf("recurrence rule: %w", err)
	}
	next := rule.Iterator()
	steps := 0
	return func() (Occurrence, bool) {
		steps++
		if steps > MaxSteps {
			return Occurrence{}, false
		}
		wall, ok := next()
		if !ok || (s.Rule.UntilWall != nil && wall.After(*s.Rule.UntilWall)) {
			return Occurrence{}, false
		}
		occurrence := s.At(wall)
		if s.Rule.Until != nil && occurrence.Start.After(*s.Rule.Until) {
			return Occurrence{}, false
		}
		return occurrence, true
	}, nil
}

// At is the series' occurrence starting at the wall-clock time wall.
func (s Series) At(wall time.Time) Occurrence {
	start := s.Zone.Instant(wall)
	if s.AllDay {
		days := max(s.Days, 1)
		return Occurrence{Wall: wall, LastDay: wall.AddDate(0, 0, days-1), Start: start, End: s.Zone.Instant(wall.AddDate(0, 0, days))}
	}
	return Occurrence{Wall: wall, Start: start, End: start.Add(s.Duration)}
}

// Between returns, in order, the occurrences that overlap [from, to) and
// that keep accepts (nil accepts all), at most limit of them (0: no limit).
func (s Series) Between(from, to time.Time, limit int, keep func(Occurrence) bool) ([]Occurrence, error) {
	next, err := s.Iterator()
	if err != nil {
		return nil, err
	}
	var found []Occurrence
	for {
		occurrence, ok := next()
		if !ok || !occurrence.Start.Before(to) {
			return found, nil
		}
		if !occurrence.End.After(from) || (keep != nil && !keep(occurrence)) {
			continue
		}
		found = append(found, occurrence)
		if limit > 0 && len(found) == limit {
			return found, nil
		}
	}
}
