package processor

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// A capture sometimes restates an event that already exists to add to it
// ("Tonight at 8 the date with Ira. Remind me two hours before."), and Codex
// plans a calendar_create of the same event. Before creating, the processor
// looks for that event; the create then becomes a calendar_update that adds
// only what the capture brings (BUG-33).
const (
	// duplicateReason is the Human Review reason when a planned new event
	// matches several events that already exist.
	duplicateReason = "This looks like an event that already exists: choose it to update it"
	// unchangedDuplicateReason is recorded when a planned new event already
	// exists with everything the capture says.
	unchangedDuplicateReason = "The event already exists and the capture adds nothing to it"
	// duplicateStartTolerance is how far apart two timed starts may be and
	// still be the same event's.
	duplicateStartTolerance = 5 * time.Minute
	// Calendar search takes at most this many words of at most this many
	// characters in total.
	maxEventSearchWords  = 10
	maxEventSearchLength = 200
)

// plannedStart is when a planned calendar_create starts: an instant for a
// timed event, or for an all-day one its date (and the start of that day in
// location). ok is false when the plan gives no readable start, which
// Calendar's validation then explains.
func plannedStart(arguments map[string]any, location *time.Location) (start time.Time, allDay, ok bool) {
	if at, err := time.Parse(time.RFC3339, text(arguments["startAt"])); err == nil {
		return at, false, true
	}
	if day, err := time.ParseInLocation(time.DateOnly, text(arguments["startDate"]), location); err == nil {
		return day, true, true
	}
	return time.Time{}, false, false
}

// findDuplicates returns the existing events a planned calendar_create
// would duplicate. It searches the planned title on the planned start's
// local day (in the capture's timezone, like every Calendar search of the
// processor), among the active events the AI may change (changeableEvents,
// the filter of every event lookup): events managed by their source never
// turn a create into an update. An event is a duplicate when it starts
// when the plan does (a timed event within five minutes, an all-day event
// on the same date) and its title is the planned one apart from case,
// diacritics, punctuation, and spacing.
func (r Runner) findDuplicates(ctx context.Context, item inboxItem, action plannedAction) ([]map[string]any, error) {
	location := r.location(item)
	title := text(action.Arguments["title"])
	start, allDay, ok := plannedStart(action.Arguments, location)
	terms := titleSearch(title)
	if !ok || terms == "" {
		return nil, nil
	}
	local := start.In(location)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	query := url.Values{"q": {terms}, "from": {from.UTC().Format(time.RFC3339)}, "to": {from.AddDate(0, 0, 1).UTC().Format(time.RFC3339)}, "timeZone": {location.String()}, "limit": {fmt.Sprint(eventSearchLimit)}}
	items, err := r.searchEvents(ctx, query)
	if err != nil {
		return nil, searchError{domain: "calendar", kind: classifyActionError(err), err: err}
	}
	found := []map[string]any{}
	seen := map[string]bool{}
	for _, event := range items {
		id, _ := event["id"].(string)
		if id != "" && !seen[id] && duplicates(event, title, start, allDay) {
			seen[id] = true
			found = append(found, event)
		}
	}
	return found, nil
}

// duplicates reports whether an existing event is the one a planned
// calendar_create titled title, starting at start, describes.
func duplicates(event map[string]any, title string, start time.Time, allDay bool) bool {
	if text(event["status"]) == "cancelled" {
		return false
	}
	if eventAllDay, _ := event["allDay"].(bool); eventAllDay != allDay {
		return false
	}
	if allDay {
		if text(event["startDate"]) != start.Format(time.DateOnly) {
			return false
		}
	} else {
		eventStart, err := time.Parse(time.RFC3339, text(event["startAt"]))
		if err != nil {
			return false
		}
		if gap := eventStart.Sub(start).Abs(); gap > duplicateStartTolerance {
			return false
		}
	}
	return comparableTitle(text(event["title"])) == comparableTitle(title)
}

// duplicateChange is the calendar_update that gives an existing event what
// a planned calendar_create of it adds, and nothing else; the event keeps
// its title, time, and everything the capture does not change:
//   - reminders: the event's own and the new ones, each once (an update
//     replaces the event's own reminders, so they are merged here);
//   - useDefaultReminders, when the plan sets it to another value;
//   - location and description, when the event has none yet;
//   - priority, when the plan gives another one;
//   - the end, when the plan states another one (see statedEnd).
//
// It is empty when the event already has all of that.
func duplicateChange(planned, event map[string]any) map[string]any {
	change := map[string]any{}
	if added, _ := planned["reminders"].([]any); len(added) > 0 {
		own, _ := event["reminders"].([]any)
		if merged, grew := mergeReminders(own, added); grew {
			change["reminders"] = merged
		}
	}
	if use, ok := planned["useDefaultReminders"].(bool); ok {
		if current, known := event["useDefaultReminders"].(bool); !known || current != use {
			change["useDefaultReminders"] = use
		}
	}
	for _, field := range []string{"location", "description"} {
		if value := strings.TrimSpace(text(planned[field])); value != "" && strings.TrimSpace(text(event[field])) == "" {
			change[field] = value
		}
	}
	if priority, ok := planned["priority"].(string); ok && priority != text(event["priority"]) {
		change["priority"] = priority
	}
	if field, end, ok := statedEnd(planned, event); ok {
		change[field] = end
	}
	return change
}

// statedEnd is the end a planned create of an existing event gives, when it
// is another end than the event's. An end exactly the default length after
// the planned start (an hour, or the one day of an all-day event) is what
// Codex fills in when the capture names no duration, so it never cuts a
// longer event short; neither does an end before the event starts.
func statedEnd(planned, event map[string]any) (string, any, bool) {
	if startDate, ok := planned["startDate"].(string); ok {
		endDate, _ := planned["endDate"].(string)
		end, err := time.Parse(time.DateOnly, endDate)
		eventStart, startErr := time.Parse(time.DateOnly, text(event["startDate"]))
		if err != nil || startErr != nil || endDate == startDate || endDate == text(event["endDate"]) || end.Before(eventStart) {
			return "", nil, false
		}
		return "endDate", endDate, true
	}
	start, err := time.Parse(time.RFC3339, text(planned["startAt"]))
	end, endErr := time.Parse(time.RFC3339, text(planned["endAt"]))
	eventStart, startErr := time.Parse(time.RFC3339, text(event["startAt"]))
	if err != nil || endErr != nil || startErr != nil || end.Sub(start) == time.Hour || !end.After(eventStart) {
		return "", nil, false
	}
	if eventEnd, err := time.Parse(time.RFC3339, text(event["endAt"])); err == nil && eventEnd.Equal(end) {
		return "", nil, false
	}
	return "endAt", planned["endAt"], true
}

// mergeReminders is own followed by the added reminders it lacks, and
// whether any were added. Reminders are the same when their kind and
// offsets are; only those fields are kept.
func mergeReminders(own, added []any) ([]any, bool) {
	merged := []any{}
	seen := map[string]bool{}
	grew := false
	for index, list := range [][]any{own, added} {
		for _, value := range list {
			reminder, ok := value.(map[string]any)
			if !ok {
				continue
			}
			kept := map[string]any{}
			for _, field := range []string{"kind", "minutesBefore", "daysBefore", "localTime"} {
				if value, present := reminder[field]; present && value != nil {
					kept[field] = value
				}
			}
			key := fmt.Sprintf("%v|%v|%v|%v", kept["kind"], kept["minutesBefore"], kept["daysBefore"], kept["localTime"])
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, kept)
			grew = grew || index == 1
		}
	}
	return merged, grew
}

// titleWords are the words of a title: runs of letters, marks, and digits.
func titleWords(title string) []string {
	return strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsNumber(r)
	})
}

// foldDiacritics removes the diacritics from s ("Café" is "Cafe", "ё" is
// "е").
func foldDiacritics(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// comparableTitle is a title without case, diacritics, punctuation, or
// extra spacing: the search terms' case-insensitive comparison, also
// insensitive to accents and punctuation.
func comparableTitle(title string) string {
	return strings.ToLower(strings.Join(titleWords(foldDiacritics(title)), " "))
}

// titleSearch is the Calendar search for events titled like title. Calendar
// finds an event when each word occurs in it, case-insensitively but with
// its diacritics, so each word is searched as its longest part without
// any ("Zahnärztin" as "rztin", which also finds "Zahnarztin"), each once,
// within Calendar's limits. Empty when no word is left to search.
func titleSearch(title string) string {
	var words []string
	seen := map[string]bool{}
	length := 0
	for _, word := range titleWords(norm.NFC.String(title)) {
		part := plainPart(word)
		key := strings.ToLower(part)
		if part == "" || seen[key] {
			continue
		}
		if len(words) == maxEventSearchWords || length+utf8.RuneCountInString(part)+len(words) > maxEventSearchLength {
			break
		}
		seen[key] = true
		words = append(words, part)
		length += utf8.RuneCountInString(part)
	}
	return strings.Join(words, " ")
}

// plainPart is the longest run of a word's letters that carry no
// diacritics (the first of equally long ones).
func plainPart(word string) string {
	best, current := "", []rune{}
	for _, r := range word + "\x00" {
		if r != 0 && foldDiacritics(string(r)) == string(r) {
			current = append(current, r)
			continue
		}
		if len(current) > utf8.RuneCountInString(best) {
			best = string(current)
		}
		current = current[:0]
	}
	return best
}
