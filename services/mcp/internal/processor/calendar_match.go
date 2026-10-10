package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// isCalendarMutation reports whether an action changes an existing Calendar
// event, which the processor first has to identify.
func isCalendarMutation(action plannedAction) bool {
	return action.Tool == "calendar_update" || action.Tool == "calendar_cancel"
}

// eventSearchLimit bounds each Calendar search and the candidates a Human
// Review lists.
const eventSearchLimit = 10

// calendarChangeFields are what calendar_update may change; clear names the
// optional ones to remove.
var calendarChangeFields = []string{"title", "description", "location", "allDay", "startAt", "endAt", "startDate", "endDate", "timeZone", "reminders", "useDefaultReminders", "priority", "clear"}

// clearableEventFields are the optional event fields an update may remove.
var clearableEventFields = map[string]bool{"description": true, "location": true, "timeZone": true, "reminders": true}

// validCalendarChange checks the fields of a calendar_update beyond their
// names: something must change, clear names only optional fields that are
// not also set (reminders may be both: the new ones replace the event's
// own, see addReminders), reminders are objects, useDefaultReminders is a
// boolean, and priority is low, medium, or high. Calendar validates the
// values themselves.
func validCalendarChange(arguments map[string]any) bool {
	changes := 0
	for _, field := range calendarChangeFields {
		if _, ok := arguments[field]; ok {
			changes++
		}
	}
	if changes == 0 {
		return false
	}
	if title, present := arguments["title"]; present {
		if text, ok := title.(string); !ok || strings.TrimSpace(text) == "" {
			return false
		}
	}
	if clear, present := arguments["clear"]; present {
		fields, ok := clear.([]any)
		if !ok {
			return false
		}
		for _, field := range fields {
			name, ok := field.(string)
			if value, alsoSet := arguments[name]; !ok || !clearableEventFields[name] || (alsoSet && value != nil && name != "reminders") {
				return false
			}
		}
	}
	return validReminders(arguments["reminders"]) && validUseDefault(arguments) && validPriority(arguments)
}

// validPriority accepts a missing priority or low, medium, or high.
func validPriority(arguments map[string]any) bool {
	value, present := arguments["priority"]
	if !present {
		return true
	}
	priority, _ := value.(string)
	return priority == "low" || priority == "medium" || priority == "high"
}

// validUseDefault accepts a missing useDefaultReminders or a boolean.
func validUseDefault(arguments map[string]any) bool {
	value, present := arguments["useDefaultReminders"]
	if !present {
		return true
	}
	_, ok := value.(bool)
	return ok
}

// validReminders accepts a missing reminders field or a list of objects.
func validReminders(value any) bool {
	if value == nil {
		return true
	}
	reminders, ok := value.([]any)
	if !ok {
		return false
	}
	for _, reminder := range reminders {
		if _, ok := reminder.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// approvedCalendarTarget takes the event the owner chose in Human Review out
// of an approved calendar action. Inbox records the chosen candidate as
// taskId (its one selection field, also used for Tasks); eventId is accepted
// too. Only an approved action may name its event; a planned one is resolved
// by search. An approved calendar_create names the existing event it
// restates, which it then updates (see findDuplicates).
func approvedCalendarTarget(approved plan) (plan, string) {
	if len(approved.Actions) != 1 || !(isCalendarMutation(approved.Actions[0]) || approved.Actions[0].Tool == "calendar_create") {
		return approved, ""
	}
	arguments := approved.Actions[0].Arguments
	target, _ := arguments["taskId"].(string)
	if target == "" {
		target, _ = arguments["eventId"].(string)
	}
	delete(arguments, "taskId")
	delete(arguments, "eventId")
	return approved, target
}

// location is the timezone an item's dates mean: the capture's own when it
// names a valid one, else the owner's configured one, else UTC.
func (r Runner) location(item inboxItem) *time.Location {
	if name, _ := item.SourceMetadata["timeZone"].(string); name != "" {
		if location, ok := loadTimeZone(name); ok {
			return location
		}
	}
	if r.TimeZone != nil {
		return r.TimeZone
	}
	return time.UTC
}

// findEvents returns the distinct active events the AI may change that any
// of a mutation's search terms matches, in the order found. The search
// covers events that have not ended before the day the item was captured
// (in the owner's timezone), so last year's appointment of the same name
// never makes a capture ambiguous; cancelled events are left out by
// Calendar. Calendar is asked only for editable events: Life OS events and
// Google events of calendars the owner may edit (written to Google). Feed
// (ICS) events, read-only Google calendars, and occurrences of Google rules
// Calendar cannot hold are managed by their source, so they are never
// candidates. The caller acts only on exactly one.
//
// A recurring event is found as one occurrence (search items carry
// occurrence), at most one per series: its next one, or for a cancellation
// naming a day (onDate) its occurrence on that day when it has one.
func (r Runner) findEvents(ctx context.Context, item inboxItem, action plannedAction) ([]map[string]any, error) {
	location := r.location(item)
	reference := item.CreatedAt
	if reference.IsZero() {
		reference = time.Now()
	}
	local := reference.In(location)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	type window struct {
		from, to      time.Time
		recurringOnly bool
	}
	windows := []window{{from: from}}
	if day, ok := onDate(action, location); ok {
		// That day's occurrences come first, so they stand for their series.
		windows = append([]window{{from: day, to: day.AddDate(0, 0, 1), recurringOnly: true}}, windows...)
	}
	found := []map[string]any{}
	seen := map[string]bool{}
	for _, window := range windows {
		for _, term := range searchTerms(action.Arguments, "eventQuery", "eventQueryAlternatives") {
			query := url.Values{"q": {term}, "from": {window.from.UTC().Format(time.RFC3339)}, "timeZone": {location.String()}, "limit": {fmt.Sprint(eventSearchLimit)}}
			if !window.to.IsZero() {
				query.Set("to", window.to.UTC().Format(time.RFC3339))
			}
			items, err := r.searchEvents(ctx, query)
			if err != nil {
				return nil, err
			}
			for _, event := range items {
				if window.recurringOnly && !isRecurring(event) {
					continue
				}
				if id, ok := event["id"].(string); ok && id != "" && !seen[id] {
					seen[id] = true
					found = append(found, event)
				}
			}
		}
	}
	return found, nil
}

// changeableEvents is the Calendar search filter for the events the AI may
// change: Life OS events. Imported (ICS, Google) events are managed by
// their source. Every event lookup of the processor uses it.
var changeableEvents = url.Values{"editable": {"true"}}

// searchEvents runs one calendar_search among the events the AI may change
// and returns the events it found.
func (r Runner) searchEvents(ctx context.Context, query url.Values) ([]map[string]any, error) {
	for key, values := range changeableEvents {
		query[key] = append([]string(nil), values...)
	}
	raw, err := r.Tools.Call(ctx, "calendar_search", map[string]any{"query": query.Encode()})
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(toolData(raw), &result); err != nil {
		// A partial union could look like exactly one match.
		return nil, fmt.Errorf("decode Calendar search result: %w", err)
	}
	return result.Items, nil
}

// onDate is the start, in location, of the day a cancellation names
// (onDate, an ISO date), if it names one.
func onDate(action plannedAction, location *time.Location) (time.Time, bool) {
	if action.Tool != "calendar_cancel" {
		return time.Time{}, false
	}
	value, ok := action.Arguments["onDate"].(string)
	if !ok {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation(time.DateOnly, value, location)
	return day, err == nil
}

// isRecurring reports whether a Calendar item is (an occurrence of) a
// recurring event.
func isRecurring(event map[string]any) bool {
	return event["occurrence"] != nil || event["recurrence"] != nil
}

// recurrenceID is the recurrence id of an occurrence item, if it is one.
func recurrenceID(event map[string]any) string {
	occurrence, _ := event["occurrence"].(map[string]any)
	id, _ := occurrence["recurrenceId"].(string)
	return id
}

// Recurring events are created and changed only in the Calendar; the AI
// may only skip one occurrence (calendar_cancel with its recurrence id).
const (
	recurringChangeReason = "Recurring events are changed in the Calendar."
	recurringCreateReason = "Recurring events are created in the Calendar."
)

// plansRecurrence reports whether an action asks for a repeating event,
// which only the owner sets up, in the Calendar.
func plansRecurrence(action plannedAction) bool {
	_, present := action.Arguments["recurrence"]
	return present && (action.Tool == "calendar_create" || action.Tool == "calendar_update")
}

// recurringUpdate reports whether a resolved change would edit a recurring
// event: its one match is an occurrence.
func recurringUpdate(res resolution) bool {
	return res.action.Tool == "calendar_update" && len(res.found) == 1 && isRecurring(res.found[0])
}

// withOccurrences records, for a cancellation that waits for the owner, the
// occurrence each recurring candidate stands for: Inbox keeps only the
// chosen candidate's id (the series id), and the approval cancels that
// occurrence alone. A map the plan brought is replaced.
func withOccurrences(action plannedAction, found []map[string]any) plannedAction {
	arguments := make(map[string]any, len(action.Arguments)+1)
	for field, value := range action.Arguments {
		arguments[field] = value
	}
	delete(arguments, "occurrences")
	occurrences := map[string]any{}
	for index, event := range found {
		if index == eventSearchLimit {
			break
		}
		id, _ := event["id"].(string)
		if rid := recurrenceID(event); id != "" && rid != "" {
			occurrences[id] = rid
		}
	}
	if len(occurrences) > 0 {
		arguments["occurrences"] = occurrences
	}
	return plannedAction{Tool: action.Tool, Arguments: arguments}
}

// approvedOccurrence is the occurrence an approved cancellation of eventID
// stands for, if it is one.
func approvedOccurrence(arguments map[string]any, eventID string) string {
	occurrences, _ := arguments["occurrences"].(map[string]any)
	rid, _ := occurrences[eventID].(string)
	return rid
}

// getEvent reads one event, for an approved update that needs its times.
func (r Runner) getEvent(ctx context.Context, id string) (map[string]any, error) {
	raw, err := r.Tools.Call(ctx, "calendar_get", map[string]any{"eventId": id})
	if err != nil {
		return nil, err
	}
	var event map[string]any
	if err := json.Unmarshal(toolData(raw), &event); err != nil {
		return nil, fmt.Errorf("decode Calendar event: %w", err)
	}
	return event, nil
}

// calendarArguments is what the Calendar MCP tool receives for one resolved
// event: its id and, for an update, the changed fields. The search terms
// only identified the event. A field the plan set to null is a removal, so
// an optional one joins clear.
func calendarArguments(tool string, planned map[string]any, eventID string) map[string]any {
	arguments := map[string]any{"eventId": eventID}
	if tool == "calendar_cancel" {
		if rid := approvedOccurrence(planned, eventID); rid != "" {
			arguments["recurrenceId"] = rid
		}
	}
	if tool != "calendar_update" {
		return arguments
	}
	clear := map[string]bool{}
	for field, value := range planned {
		switch {
		case field == "eventQuery" || field == "eventQueryAlternatives" || field == "taskId" || field == "eventId" || field == "idempotencyKey":
		case field == "clear":
			fields, _ := value.([]any)
			for _, name := range fields {
				if name, ok := name.(string); ok {
					clear[name] = true
				}
			}
		case value == nil:
			if clearableEventFields[field] {
				clear[field] = true
			}
		default:
			arguments[field] = value
		}
	}
	if len(clear) > 0 {
		names := make([]string, 0, len(clear))
		for name := range clear {
			names = append(names, name)
		}
		sort.Strings(names)
		fields := make([]any, len(names))
		for i, name := range names {
			fields[i] = name
		}
		arguments["clear"] = fields
	}
	return arguments
}

// unchangedRemindersReason is recorded when a planned calendar_update only
// adds reminders the event already has.
const unchangedRemindersReason = "The event already has these reminders, so nothing changed"

// addReminders makes the reminders of a planned calendar_update (the
// Calendar arguments for event) add to the event's own: "remind me 2 hours
// before" keeps the reminders it has, each reminder once (mergeReminders,
// as for a restated event). Only a plan that also names reminders in clear
// replaces them, because the item asked to remove or replace them; Calendar
// then gets just the new ones. It reports whether anything is left to
// change.
func addReminders(arguments, event map[string]any) bool {
	added, ok := arguments["reminders"].([]any)
	if !ok {
		return true
	}
	clear, _ := arguments["clear"].([]any)
	others := []any{}
	for _, field := range clear {
		if field != "reminders" {
			others = append(others, field)
		}
	}
	if len(others) < len(clear) {
		if len(others) == 0 {
			delete(arguments, "clear")
		} else {
			arguments["clear"] = others
		}
		return true
	}
	own, _ := event["reminders"].([]any)
	if merged, grew := mergeReminders(own, added); grew {
		arguments["reminders"] = merged
		return true
	}
	delete(arguments, "reminders")
	for field := range arguments {
		if field != "eventId" {
			return true
		}
	}
	return false
}

// missingEnd reports whether an update moves an event's start without
// saying where it ends.
func missingEnd(arguments map[string]any) bool {
	_, startAt := arguments["startAt"]
	_, endAt := arguments["endAt"]
	_, startDate := arguments["startDate"]
	_, endDate := arguments["endDate"]
	return (startAt && !endAt) || (startDate && !endDate)
}

// completeCreate gives a new event without an end its default length, as
// Google Calendar does: a timed event lasts an hour and an all-day event is
// its one day ("Add a haircut on Sunday at 11:00" names no duration).
func completeCreate(arguments map[string]any) {
	if !missingEnd(arguments) {
		return
	}
	if start, ok := arguments["startDate"].(string); ok {
		arguments["endDate"] = start
		return
	}
	start, err := time.Parse(time.RFC3339, text(arguments["startAt"]))
	if err != nil {
		return // Calendar's validation explains it
	}
	arguments["endAt"] = start.Add(time.Hour).Format(time.RFC3339)
}

// keepDuration completes a reschedule that gives only the new start: "move
// the dentist to Monday at 9" keeps the appointment's length, and an all-day
// event keeps its number of days. Turning an event into an all-day one with
// only a start date makes it that one day. Anything else is left to the plan
// and to Calendar's validation.
func keepDuration(arguments, event map[string]any) {
	wasAllDay, _ := event["allDay"].(bool)
	allDay := wasAllDay
	if value, ok := arguments["allDay"].(bool); ok {
		allDay = value
	}
	_, hasEndAt := arguments["endAt"]
	_, hasEndDate := arguments["endDate"]
	if allDay != wasAllDay {
		if start, ok := arguments["startDate"].(string); ok && allDay && !hasEndDate {
			arguments["endDate"] = start
		}
		return
	}
	if allDay {
		start, ok := arguments["startDate"].(string)
		newStart, err := time.Parse(time.DateOnly, start)
		oldStart, startErr := time.Parse(time.DateOnly, text(event["startDate"]))
		oldEnd, endErr := time.Parse(time.DateOnly, text(event["endDate"]))
		if !ok || hasEndDate || err != nil || startErr != nil || endErr != nil || oldEnd.Before(oldStart) {
			return
		}
		arguments["endDate"] = newStart.AddDate(0, 0, int(oldEnd.Sub(oldStart).Hours()/24)).Format(time.DateOnly)
		return
	}
	start, ok := arguments["startAt"].(string)
	newStart, err := time.Parse(time.RFC3339, start)
	oldStart, startErr := time.Parse(time.RFC3339, text(event["startAt"]))
	oldEnd, endErr := time.Parse(time.RFC3339, text(event["endAt"]))
	if !ok || hasEndAt || err != nil || startErr != nil || endErr != nil || !oldEnd.After(oldStart) {
		return
	}
	// The new start's own offset is kept, and the length is real time.
	arguments["endAt"] = newStart.Add(oldEnd.Sub(oldStart)).Format(time.RFC3339)
}

// keepTimeOfDay completes a date-only reschedule of a timed event ("move
// the dentist to Monday"): it starts on the new date at its original local
// time of day, in the owner's timezone, so a daylight-saving change between
// the two dates does not shift it.
func keepTimeOfDay(arguments, event map[string]any, location *time.Location) {
	if allDay, _ := event["allDay"].(bool); allDay {
		return
	}
	_, switching := arguments["allDay"]
	_, hasStartAt := arguments["startAt"]
	_, hasEndDate := arguments["endDate"]
	date, isDate := arguments["startDate"].(string)
	day, err := time.ParseInLocation(time.DateOnly, date, location)
	start, startErr := time.Parse(time.RFC3339, text(event["startAt"]))
	if switching || hasStartAt || hasEndDate || !isDate || err != nil || startErr != nil {
		return
	}
	local := start.In(location)
	delete(arguments, "startDate")
	arguments["startAt"] = time.Date(day.Year(), day.Month(), day.Day(), local.Hour(), local.Minute(), local.Second(), 0, location).Format(time.RFC3339)
}

func text(value any) string {
	s, _ := value.(string)
	return s
}

// eventCandidates is the events a Human Review offers to choose from. The
// Dashboard shows a candidate's title, so it says when (in the owner's
// timezone) and where; the event's own title is kept as eventTitle.
func eventCandidates(found []map[string]any, location *time.Location) []map[string]any {
	if len(found) > eventSearchLimit {
		found = found[:eventSearchLimit]
	}
	candidates := make([]map[string]any, 0, len(found))
	for _, event := range found {
		candidate := map[string]any{"id": event["id"], "kind": "calendar_event", "title": eventLabel(event, location), "eventTitle": event["title"]}
		if rid := recurrenceID(event); rid != "" {
			candidate["recurrenceId"] = rid
		}
		for _, field := range []string{"allDay", "startAt", "endAt", "startDate", "endDate", "location", "status"} {
			if value, ok := event[field]; ok && value != nil {
				candidate[field] = value
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// eventLabel reads like "Dentist · Fri 16 Oct 15:00–16:00 · Dr. Weber".
func eventLabel(event map[string]any, location *time.Location) string {
	parts := []string{text(event["title"])}
	if when := eventWhen(event, location); when != "" {
		parts = append(parts, when)
	}
	if place := strings.TrimSpace(text(event["location"])); place != "" {
		parts = append(parts, place)
	}
	return strings.Join(parts, " · ")
}

func eventWhen(event map[string]any, location *time.Location) string {
	const day = "Mon 2 Jan 2006"
	if allDay, _ := event["allDay"].(bool); allDay {
		start, err := time.Parse(time.DateOnly, text(event["startDate"]))
		if err != nil {
			return ""
		}
		end, err := time.Parse(time.DateOnly, text(event["endDate"]))
		if err != nil || !end.After(start) {
			return start.Format(day) + " (all day)"
		}
		return start.Format(day) + " – " + end.Format(day) + " (all day)"
	}
	start, err := time.Parse(time.RFC3339, text(event["startAt"]))
	if err != nil {
		return ""
	}
	start = start.In(location)
	end, err := time.Parse(time.RFC3339, text(event["endAt"]))
	if err != nil {
		return start.Format(day + " 15:04")
	}
	end = end.In(location)
	if end.Format(time.DateOnly) == start.Format(time.DateOnly) {
		return start.Format(day+" 15:04") + "–" + end.Format("15:04")
	}
	return start.Format(day+" 15:04") + " – " + end.Format(day+" 15:04")
}

// managedBySourceReason is the Human Review reason when Calendar refuses a
// change because the event belongs to an imported source.
const managedBySourceReason = "This event is managed by its source and can't be changed by capture"

// managedBySource reports whether Calendar refused a change because the
// event is imported from a feed (409 managed_by_source) or read-only in
// Google (409 google_read_only, such as a calendar the owner can only
// read). Retrying cannot help.
func managedBySource(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "managed_by_source") || strings.Contains(err.Error(), "google_read_only"))
}

// calendarReviewReason tells the owner, in the Human Review queue, what the
// processor wants to do and why it waits.
func calendarReviewReason(tool string, matches int) string {
	switch {
	case tool == "calendar_cancel" && matches == 1:
		return "Cancelling a Calendar event needs your approval: choose the event to cancel it"
	case tool == "calendar_cancel" && matches == 0:
		return "No upcoming Calendar event matches, so nothing was cancelled"
	case tool == "calendar_cancel":
		return "Several Calendar events match: choose the one to cancel"
	case matches == 0:
		return "No upcoming Calendar event matches, so nothing was changed"
	default:
		return "Several Calendar events match: choose the one to change"
	}
}
