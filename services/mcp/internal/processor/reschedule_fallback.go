package processor

import (
	"context"
	"fmt"
	"time"
)

// resolution is what searching for a mutation's target found: the action to
// run (the equivalent action in the other domain when only that one
// matched) and the entities its domain found.
type resolution struct {
	action plannedAction
	found  []map[string]any
	// triedOther is set when nothing matched in the planned domain and the
	// other domain was searched too.
	triedOther bool
}

// searchError is a failed target search; Task, Calendar, and Food failures
// are retried as before.
type searchError struct {
	domain string
	kind   failureKind
	err    error
}

func (e searchError) Error() string { return e.domain + " search failed: " + e.err.Error() }

// resolve finds the target of an existing-entity mutation. A reschedule is
// sometimes planned for the wrong domain ("Move the electrician call to
// Monday" as calendar_update while it is a Task): when nothing matches in the
// planned domain, a reschedule is looked up in the other one with the same
// search terms, and the equivalent action runs there if that finds
// something. Likewise a purchase marked bought that is not on the shopping
// list is looked up among the open Tasks (boughtTask). Cancelling,
// completing, reopening, and changes without a new time never cross
// domains.
func (r Runner) resolve(ctx context.Context, item inboxItem, action plannedAction) (resolution, error) {
	found, err := r.findTargets(ctx, item, action)
	if err != nil || len(found) > 0 {
		return resolution{action: action, found: found}, err
	}
	location := r.location(item)
	other, ok := equivalentReschedule(action, location)
	if !ok {
		// A purchase not on the shopping list may be an open Task.
		other, ok = boughtTask(action)
	}
	if !ok {
		return resolution{action: action, found: found}, nil
	}
	otherFound, err := r.findTargets(ctx, item, other)
	if err != nil || len(otherFound) == 0 {
		return resolution{action: action, found: found, triedOther: true}, err
	}
	if other.Tool == "calendar_update" {
		fitAllDayEvents(other.Arguments, otherFound, location)
	}
	return resolution{action: other, found: otherFound, triedOther: true}, nil
}

func (r Runner) findTargets(ctx context.Context, item inboxItem, action plannedAction) ([]map[string]any, error) {
	if isFoodMutation(action) {
		found, err := r.findFoodItems(ctx, action)
		if err != nil {
			return nil, searchError{domain: "food", kind: classifyActionError(err), err: err}
		}
		return found, nil
	}
	if isTaskMutation(action) {
		found, err := r.findTasks(ctx, action)
		if err != nil {
			return nil, searchError{domain: "task", kind: failureDependency, err: err}
		}
		return found, nil
	}
	found, err := r.findEvents(ctx, item, action)
	if err != nil {
		return nil, searchError{domain: "calendar", kind: classifyActionError(err), err: err}
	}
	return found, nil
}

// equivalentReschedule is a reschedule in the other domain: a task_update
// that sets dueAt becomes a calendar_update starting then (on that date when
// it is due at the owner-local midnight), and a
// calendar_update with a new start becomes a task_update due then (an
// all-day start is due at the owner-local midnight of that date, like a
// date-only Task). A move (moves.go) carries over as it is. Search terms,
// the title, and notes/description carry over; fields only one domain has
// are dropped.
func equivalentReschedule(action plannedAction, location *time.Location) (plannedAction, bool) {
	in := action.Arguments
	out := map[string]any{}
	var tool string
	_, moves := plannedMove(action)
	switch {
	case action.Tool == "task_update" && in["taskId"] == nil && in["clearDueAt"] != true:
		dueAt, ok := in["dueAt"].(string)
		if (!ok || dueAt == "") && !moves {
			return plannedAction{}, false
		}
		tool = "calendar_update"
		copyField(out, "eventQuery", in, "taskQuery")
		copyField(out, "eventQueryAlternatives", in, "taskQueryAlternatives")
		copyField(out, "title", in, "title")
		copyField(out, "description", in, "notes")
		if moves {
			// A move means the same for an event (it keeps its length).
			out["move"] = in["move"]
			break
		}
		out["startAt"] = dueAt
		// Due at the owner-local midnight is a date-only due date: the event
		// moves to that day (a timed one keeps its time of day).
		if at, err := time.Parse(time.RFC3339, dueAt); err == nil {
			if local := at.In(location); local.Hour() == 0 && local.Minute() == 0 && local.Second() == 0 && local.Nanosecond() == 0 {
				delete(out, "startAt")
				out["startDate"] = local.Format(time.DateOnly)
			}
		}
	case action.Tool == "calendar_update" && moves:
		tool = "task_update"
		copyField(out, "taskQuery", in, "eventQuery")
		copyField(out, "taskQueryAlternatives", in, "eventQueryAlternatives")
		copyField(out, "title", in, "title")
		copyField(out, "notes", in, "description")
		out["move"] = in["move"]
	case action.Tool == "calendar_update":
		dueAt, ok := in["startAt"].(string)
		if !ok || dueAt == "" {
			startDate, isDate := in["startDate"].(string)
			day, err := time.ParseInLocation(time.DateOnly, startDate, location)
			if !isDate || err != nil {
				return plannedAction{}, false
			}
			dueAt = day.Format(time.RFC3339)
		}
		tool = "task_update"
		copyField(out, "taskQuery", in, "eventQuery")
		copyField(out, "taskQueryAlternatives", in, "eventQueryAlternatives")
		copyField(out, "title", in, "title")
		copyField(out, "notes", in, "description")
		out["dueAt"] = dueAt
	default:
		return plannedAction{}, false
	}
	other := plannedAction{Tool: tool, Arguments: out}
	return other, validAction(other)
}

// copyField copies a non-null value under another name.
func copyField(to map[string]any, toField string, from map[string]any, fromField string) {
	if value, ok := from[fromField]; ok && value != nil {
		to[toField] = value
	}
}

// fitAllDayEvents turns the start of a reschedule that came from a Task into
// a date when every event found is all-day, since an all-day event takes a
// date rather than an instant.
func fitAllDayEvents(arguments map[string]any, events []map[string]any, location *time.Location) {
	for _, event := range events {
		if allDay, _ := event["allDay"].(bool); !allDay {
			return
		}
	}
	start, err := time.Parse(time.RFC3339, text(arguments["startAt"]))
	if err != nil {
		return
	}
	delete(arguments, "startAt")
	arguments["startDate"] = start.In(location).Format(time.DateOnly)
}

// reviewCandidatesFor is what a Human Review offers for an action: Tasks,
// labelled events, or labelled shopping list or pantry items.
func (r Runner) reviewCandidatesFor(item inboxItem, action plannedAction, found []map[string]any) []map[string]any {
	if isCalendarMutation(action) {
		return eventCandidates(found, r.location(item))
	}
	if isFoodMutation(action) {
		return foodCandidates(action.Tool, found)
	}
	return reviewCandidates(found)
}

// reviewResolution sends a resolved mutation to Human Review with reason.
// A change that would edit a recurring event offers nothing to approve; a
// cancellation records which occurrence each recurring candidate stands for.
func (r Runner) reviewResolution(ctx context.Context, item inboxItem, attemptID string, res resolution, reason string) error {
	if recurringUpdate(res) {
		return r.humanReview(ctx, item.ID, attemptID, res.action, []map[string]any{}, recurringChangeReason)
	}
	action := res.action
	if action.Tool == "calendar_cancel" {
		action = withOccurrences(action, res.found)
	}
	return r.humanReview(ctx, item.ID, attemptID, action, r.reviewCandidatesFor(item, action, res.found), reason)
}

// reviewReason says why a mutation waits for the owner.
func reviewReason(res resolution) string {
	switch {
	case len(res.found) == 0 && res.triedOther && isCalendarMutation(res.action):
		return "No upcoming Calendar event or open Task matches, so nothing was changed"
	case len(res.found) == 0 && res.triedOther:
		return "No open Task or upcoming Calendar event matches, so nothing was changed"
	case isCalendarMutation(res.action):
		return calendarReviewReason(res.action.Tool, len(res.found))
	case isFoodMutation(res.action):
		return foodReviewReason(res.action.Tool)
	default:
		return "ambiguous or missing Task selection"
	}
}

// lowConfidenceReason says why an uncertain mutation waits for the owner.
func lowConfidenceReason(action plannedAction) string {
	if isCalendarMutation(action) {
		return "ambiguous or low-confidence Calendar event selection"
	}
	if action.Tool == "food_mark_bought" {
		return "ambiguous or low-confidence shopping list item selection"
	}
	if action.Tool == "food_update_pantry_item" {
		return "ambiguous or low-confidence pantry item selection"
	}
	return "ambiguous or low-confidence Task selection"
}

// failedSearch is the retry reason and kind of a failed target search.
func failedSearch(err error) (string, failureKind) {
	if failed, ok := err.(searchError); ok {
		return failed.Error(), failed.kind
	}
	return fmt.Sprintf("search failed: %v", err), failureDependency
}
