package processor

import (
	"math"
	"regexp"
	"strings"
	"time"
)

// Relative moves. Codex never sees the existing Tasks and events, so it
// cannot know the time a capture keeps or shifts ("move the dentist task to
// tomorrow, same time", "push the meeting an hour later"). task_update and
// calendar_update may therefore carry move, which the MCP tool resolves from
// the Task's or event's current time on the owner's wall clock (see
// services/mcp/cmd/server/moves.go).

// movePrompt is the plan prompt's clause about moves. It follows planPrompt
// (whose field lists it extends) as a separate clause.
const movePrompt = "Moving a Task or event relative to its current time. You do not know when the existing Tasks and events are, so when the item keeps or shifts the time something already has, task_update and calendar_update may use move instead of dueAt, clearDueAt, allDay, startAt, endAt, startDate, and endDate; the processor works the new time out from the current one in the owner's timezone, and an event keeps its length. " +
	"move is an object with days (whole days later at the same time of day, counted from its current date; negative for earlier), hours and minutes (that much later; negative for earlier), toDate (the new ISO date, keeping the time of day; resolve tomorrow or a weekday from the capture's date as for any date), toTime (the new time of day, HH:MM, on toDate or else on its current date), and, for task_update only, dateOnly true (keep the date, drop the time). " +
	"\"Move the dentist task to tomorrow, same time\" is move {\"toDate\": tomorrow's date}; \"push the meeting an hour later\" {\"hours\": 1}; \"postpone the report by two days\" {\"days\": 2}; \"move the electrician call to Friday\" {\"toDate\": the coming Friday's date}, which keeps the time it has (a Task without a time stays without one); \"move it to Friday at 15:00\" {\"toDate\": Friday's date, \"toTime\": \"15:00\"} (dueAt or startAt work too); \"same day, but at 18:00\" {\"toTime\": \"18:00\"}; \"remove the time from the passport task\" {\"dateOnly\": true}. " +
	"A question about a Task or event (\"when is the dentist call due?\") changes nothing: set ambiguous true and actions to [] with reason \"Questions are not answered by capture: see the Tasks or Calendar view.\". "

// moveTimeFields are the absolute times a move replaces.
var moveTimeFields = []string{"dueAt", "clearDueAt", "allDay", "startAt", "endAt", "startDate", "endDate"}

// plannedMove is a planned action's move, if it has one.
func plannedMove(action plannedAction) (map[string]any, bool) {
	if action.Tool != "task_update" && action.Tool != "calendar_update" {
		return nil, false
	}
	move, ok := action.Arguments["move"].(map[string]any)
	return move, ok
}

// validMove checks a planned move's shape: an object of known fields with
// values of the right type, instead of an absolute time. MCP checks the
// values themselves (dates, times, ranges, and what fits the Task or event).
// The timezone is the processor's (withMoveZone).
func validMove(action plannedAction) bool {
	value, present := action.Arguments["move"]
	if !present || value == nil {
		return true
	}
	move, ok := plannedMove(action)
	if !ok || len(move) == 0 {
		return false
	}
	for field, value := range move {
		switch field {
		case "days", "hours", "minutes":
			number, ok := value.(float64)
			if !ok || number != math.Trunc(number) {
				return false
			}
		case "toDate", "toTime":
			if _, ok := value.(string); !ok {
				return false
			}
		case "dateOnly":
			if _, ok := value.(bool); !ok {
				return false
			}
		default:
			return false
		}
	}
	for _, field := range moveTimeFields {
		if _, set := action.Arguments[field]; set {
			return false
		}
	}
	return true
}

// withMoveZone makes a move mean the capture's wall clock, like the plan's
// other dates and times. A null move is left out.
func withMoveZone(arguments map[string]any, location *time.Location) {
	if value, present := arguments["move"]; present && value == nil {
		delete(arguments, "move")
	}
	move, ok := arguments["move"].(map[string]any)
	if !ok || location == nil {
		return
	}
	zoned := make(map[string]any, len(move)+1)
	for field, value := range move {
		zoned[field] = value
	}
	zoned["timeZone"] = location.String()
	arguments["move"] = zoned
}

// movesLast runs a plan's moves after its other actions. A move is relative,
// so unlike an absolute change it is not the same when run twice: a retry
// after a later action failed would move again.
func movesLast(p plan) plan {
	ordered := make([]plannedAction, 0, len(p.Actions))
	for _, last := range []bool{false, true} {
		for _, action := range p.Actions {
			if _, moves := plannedMove(action); moves == last {
				ordered = append(ordered, action)
			}
		}
	}
	p.Actions = ordered
	return p
}

// moveRefusal starts MCP's answer to a move that does not fit the Task or
// event it found, such as an hour later for a Task due on a date without a
// time.
const moveRefusal = "cannot move: "

// apiHints are the parenthesized tool hints in MCP's refusal, left out for
// the owner.
var apiHints = regexp.MustCompile(`\s*\([^)]*\)`)

// refusedMove is the Human Review reason for a move MCP refused: the owner
// says what they meant, since a retry cannot help.
func refusedMove(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	text := err.Error()
	at := strings.Index(text, moveRefusal)
	if at < 0 {
		return "", false
	}
	reason := strings.TrimSpace(apiHints.ReplaceAllString(text[at+len(moveRefusal):], ""))
	return strings.TrimSuffix(reason, ".") + ". Nothing was changed.", true
}
