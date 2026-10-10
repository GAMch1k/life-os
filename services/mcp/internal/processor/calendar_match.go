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
var calendarChangeFields = []string{"title", "description", "location", "allDay", "startAt", "endAt", "startDate", "endDate", "timeZone", "reminders", "clear"}

// clearableEventFields are the optional event fields an update may remove.
var clearableEventFields = map[string]bool{"description": true, "location": true, "timeZone": true, "reminders": true}

// validCalendarChange checks the fields of a calendar_update beyond their
// names: something must change, clear names only optional fields, and
// reminders are objects. Calendar validates the values themselves.
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
			if value, alsoSet := arguments[name]; !ok || !clearableEventFields[name] || (alsoSet && value != nil) {
				return false
			}
		}
	}
	return validReminders(arguments["reminders"])
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
// by search.
func approvedCalendarTarget(approved plan) (plan, string) {
	if len(approved.Actions) != 1 || !isCalendarMutation(approved.Actions[0]) {
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

// findEvents returns the distinct active manual events any of a mutation's
// search terms matches, in the order found. The search covers events that
// have not ended before the day the item was captured (in the owner's
// timezone), so last year's appointment of the same name never makes a
// capture ambiguous; cancelled events are left out by Calendar. Imported
// (ICS, Google) events are managed by their source, which alone changes or
// cancels them, so they are never candidates. The caller acts only on
// exactly one.
func (r Runner) findEvents(ctx context.Context, item inboxItem, action plannedAction) ([]map[string]any, error) {
	location := r.location(item)
	reference := item.CreatedAt
	if reference.IsZero() {
		reference = time.Now()
	}
	local := reference.In(location)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	found := []map[string]any{}
	seen := map[string]bool{}
	for _, term := range searchTerms(action.Arguments, "eventQuery", "eventQueryAlternatives") {
		query := url.Values{"q": {term}, "from": {from.UTC().Format(time.RFC3339)}, "timeZone": {location.String()}, "sourceKind": {"manual"}, "limit": {fmt.Sprint(eventSearchLimit)}}
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
		for _, event := range result.Items {
			if id, ok := event["id"].(string); ok && id != "" && !seen[id] {
				seen[id] = true
				found = append(found, event)
			}
		}
	}
	return found, nil
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

// missingEnd reports whether an update moves an event's start without
// saying where it ends.
func missingEnd(arguments map[string]any) bool {
	_, startAt := arguments["startAt"]
	_, endAt := arguments["endAt"]
	_, startDate := arguments["startDate"]
	_, endDate := arguments["endDate"]
	return (startAt && !endAt) || (startDate && !endDate)
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
// event is imported (409, code managed_by_source). Retrying cannot help.
func managedBySource(err error) bool {
	return err != nil && strings.Contains(err.Error(), "managed_by_source")
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
