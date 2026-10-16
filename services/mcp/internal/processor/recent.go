package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// Recent captures as context (P5-20). The owner often corrects or adds to
// what they captured a minute ago: "Buy milk", then "actually oat milk";
// "Meeting with Anna at 5", then "make it 6". Before Codex plans a capture,
// the processor reads the owner's other captures from the ContextMinutes
// before it, from every source (Inbox GET /v1/recent-captures through MCP
// inbox_recent_captures), with what their processing did, and gives Codex a
// compact list of them, clipped and capped. Codex uses it only to resolve
// references and corrections. A planned change may name an entity a recent
// capture created or changed by its id (taskId, eventId, shoppingItemId),
// which the processor accepts only for an id the list named; every change of
// such an entity, however its target was found, is recorded with followsUp,
// so Inbox links the two captures in Activity (inbox.item.followed_up).

const (
	// recentCaptureLimit is the most recent captures read for one capture.
	recentCaptureLimit = 10
	// maxRecentText is the most characters of a recent capture's text shown.
	maxRecentText = 300
	// maxRecentEffects is the most entities shown per recent capture.
	maxRecentEffects = 8
	// maxRecentSummary is the most characters of an entity's summary.
	maxRecentSummary = 120
	// maxRecentNote is the most characters of a review or failure reason.
	maxRecentNote = 160
	// maxRecentContext bounds the list in the prompt, in characters; the
	// oldest captures are left out first.
	maxRecentContext = 6000
)

// recentContextPrompt is the plan prompt's section for recent captures. It
// is added after the owner's time only when there are any, so the fixed
// planPrompt stays a stable prefix and a capture without recent ones is
// planned as before.
const recentContextPrompt = "Recent captures (context only). The owner often corrects or adds to what they captured a few minutes earlier. The list below is the owner's other captures from the minutes before this item, oldest first, each with what Life OS already did for it: done names each entity (task, event, shopping_item, pantry_item, transaction, meal, staple, recipe) with its id, the change (created, updated, added, merged, bought, completed, cancelled, …), and a summary; state human_review means it waits for the owner's decision and nothing was done yet, and failed means nothing was done. Use the list only to understand what this item refers to or corrects. Never plan an action again for an earlier capture: what it asked for is done or waiting for the owner. When this item clearly corrects, changes, or undoes something done for a recent capture (\"actually oat milk\", \"make it 6\", \"no, tomorrow morning\"), change that entity instead of creating another one: a task with task_update (or task_complete or task_reopen) and taskId set to its id, with only the fields that change; an event with calendar_update and eventId set to its id, with only the fields that change (a new start keeps the event's length), or calendar_cancel with eventId (the owner approves every cancellation); a shopping_item with food_update_shopping_item and shoppingItemId set to its id, with only what changes: name (the new name as the owner wrote it, never translated, without the quantity), amount, note, or storeName. food_update_shopping_item may only use shoppingItemId, name, amount, note, and storeName. taskId, eventId, and shoppingItemId may be used this way besides the fields named above, only with an id listed in done for an entity of that kind; never guess one. Something new that continues a recent capture (\"add bread too\", \"and call Anna as well\") is a new action as usual (food_add_shopping_items with bread), never a change of the earlier entity. When the item asks to change or undo something that has no change tool here (a transaction, a meal, a pantry item, a staple, a recipe, or deleting a task), or it is unclear whether or which recent entity it means, set ambiguous true and actions to [] with a short reason. An item unrelated to the recent captures is planned exactly as if they were not there. "

// recentCapture is one capture GET /v1/recent-captures lists.
type recentCapture struct {
	ID            string         `json:"id"`
	Content       string         `json:"content"`
	Source        string         `json:"source"`
	State         string         `json:"state"`
	CorrelationID string         `json:"correlationId"`
	CreatedAt     time.Time      `json:"createdAt"`
	Outcome       *recentOutcome `json:"outcome"`
}

// recentOutcome is a recent capture's latest finished processing attempt.
type recentOutcome struct {
	Status       string           `json:"status"`
	ErrorSummary string           `json:"errorSummary"`
	Actions      []map[string]any `json:"actions"`
}

// recentEffect is one entity a recent capture's processing created or
// changed, as Codex sees it.
type recentEffect struct {
	Entity  string `json:"entity"`
	ID      string `json:"id,omitempty"`
	Change  string `json:"change"`
	Summary string `json:"summary,omitempty"`
	// name is the entity's title or name, its search term.
	name string
	// captureID is the recent capture that did it.
	captureID string
}

// recentEntry is one recent capture as the prompt lists it.
type recentEntry struct {
	CapturedAt    string         `json:"capturedAt"`
	MinutesBefore int            `json:"minutesBefore"`
	Source        string         `json:"source"`
	Text          string         `json:"text"`
	State         string         `json:"state"`
	Done          []recentEffect `json:"done,omitempty"`
	Note          string         `json:"note,omitempty"`
}

// recentContext is what a capture's planning knows about the captures just
// before it: the prompt section, and the entities it names, by id.
type recentContext struct {
	note string
	// entities maps an entity id to the latest listed capture that created
	// or changed it.
	entities map[string]recentEffect
}

type recentContextKey struct{}

// WithRecentContext gives the planning runs started with ctx the prompt
// section about recent captures (empty for none).
func WithRecentContext(ctx context.Context, note string) context.Context {
	return context.WithValue(ctx, recentContextKey{}, note)
}

func recentContextFrom(ctx context.Context) string {
	note, _ := ctx.Value(recentContextKey{}).(string)
	return note
}

// recentContext reads the captures made in the ContextMinutes before item
// and builds what its planning gets. With ContextMinutes 0 it is empty.
func (r Runner) recentContext(ctx context.Context, item inboxItem) (recentContext, error) {
	if r.ContextMinutes <= 0 {
		return recentContext{}, nil
	}
	raw, err := r.Tools.Call(ctx, "inbox_recent_captures", map[string]any{"query": fmt.Sprintf("before=%s&minutes=%d&limit=%d", url.QueryEscape(item.ID), r.ContextMinutes, recentCaptureLimit)})
	if err != nil {
		return recentContext{}, err
	}
	var list struct {
		Items []recentCapture `json:"items"`
	}
	if err := json.Unmarshal(toolData(raw), &list); err != nil {
		return recentContext{}, fmt.Errorf("decode recent captures: %w", err)
	}
	return buildRecentContext(item, list.Items, r.ContextMinutes, r.location(item)), nil
}

// buildRecentContext lists the recent captures (given newest first, as
// Inbox lists them) oldest first, without item itself, at most
// recentCaptureLimit, clipped, and within maxRecentContext characters (the
// oldest left out first). Only the entities it lists can be named by id.
func buildRecentContext(item inboxItem, captures []recentCapture, minutes int, location *time.Location) recentContext {
	if location == nil {
		location = time.UTC
	}
	var lines []string
	entities := map[string]recentEffect{}
	size := 0
	for _, capture := range captures {
		if capture.ID == item.ID {
			continue
		}
		if len(lines) == recentCaptureLimit {
			break
		}
		entry := recentEntryOf(item, capture, location)
		line := encodeRecent(entry)
		length := utf8.RuneCountInString(line) + 1
		if size+length > maxRecentContext {
			break
		}
		size += length
		lines = append(lines, line)
		// Newest first: an entity a later capture changed again stays that
		// capture's.
		for _, effect := range entry.Done {
			if _, later := entities[effect.ID]; effect.ID != "" && !later {
				entities[effect.ID] = effect
			}
		}
	}
	if len(lines) == 0 {
		return recentContext{}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	note := recentContextPrompt + fmt.Sprintf("Recent captures from the %d minutes before this item: [", minutes) + strings.Join(lines, ",") + "]. "
	return recentContext{note: note, entities: entities}
}

// recentEntryOf is one recent capture as the prompt lists it.
func recentEntryOf(item inboxItem, capture recentCapture, location *time.Location) recentEntry {
	entry := recentEntry{
		CapturedAt: capture.CreatedAt.In(location).Format("Monday 2006-01-02 15:04"),
		Source:     capture.Source,
		Text:       activity.Preview(capture.Content, maxRecentText),
		State:      capture.State,
	}
	if !item.CreatedAt.IsZero() {
		entry.MinutesBefore = int(math.Round(item.CreatedAt.Sub(capture.CreatedAt).Minutes()))
	}
	entry.Done = recentEffects(capture, location)
	if len(entry.Done) > maxRecentEffects {
		entry.Done = entry.Done[:maxRecentEffects]
	}
	reason := ""
	if capture.Outcome != nil {
		reason = activity.Preview(capture.Outcome.ErrorSummary, maxRecentNote)
	}
	switch {
	case capture.State == "human_review":
		entry.Note = "waiting for the owner's decision, nothing done yet"
		if reason != "" {
			entry.Note += ": " + reason
		}
	case capture.State == "failed":
		entry.Note = "processing failed, nothing done"
	case capture.Outcome == nil:
		entry.Note = "resolved by the owner"
	case len(entry.Done) == 0:
		entry.Note = "nothing was changed"
	}
	return entry
}

// encodeRecent is one entry as compact JSON, without HTML escaping.
func encodeRecent(entry recentEntry) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(entry)
	return strings.TrimSpace(b.String())
}

// recentEffects is what a recent capture's processed attempt created or
// changed, from its recorded actions and the owning services' results. A
// capture waiting for review, failed, or resolved by hand did nothing.
func recentEffects(capture recentCapture, location *time.Location) []recentEffect {
	if capture.Outcome == nil || capture.Outcome.Status != "processed" {
		return nil
	}
	var effects []recentEffect
	for _, action := range capture.Outcome.Actions {
		for _, effect := range effectsOf(action, location) {
			effect.captureID = capture.ID
			effects = append(effects, effect)
		}
	}
	return effects
}

// effectsOf reads the entities one recorded action created or changed. An
// action that did nothing (tool none) or recorded no result has none.
func effectsOf(action map[string]any, location *time.Location) []recentEffect {
	tool := text(action["tool"])
	result, _ := action["result"].(map[string]any)
	if result == nil {
		return nil
	}
	one := func(entity, change string, object map[string]any) []recentEffect {
		if object == nil {
			return nil
		}
		return []recentEffect{describeEntity(entity, change, object, location)}
	}
	each := func(entity, change string, field string) []recentEffect {
		var effects []recentEffect
		items, _ := result["items"].([]any)
		for _, raw := range items {
			object, _ := raw.(map[string]any)
			if nested, ok := object[field].(map[string]any); field != "" && ok {
				changed := change
				if merged, _ := object["merged"].(bool); merged {
					changed = "merged"
				}
				effects = append(effects, describeEntity(entity, changed, nested, location))
			} else if field == "" && object != nil {
				effects = append(effects, describeEntity(entity, change, object, location))
			}
		}
		return effects
	}
	switch tool {
	case "task_create":
		return one("task", "created", result)
	case "task_update":
		return one("task", "updated", result)
	case "task_complete":
		return one("task", "completed", result)
	case "task_reopen":
		return one("task", "reopened", result)
	case "calendar_create":
		return one("event", "created", result)
	case "calendar_update":
		return one("event", "updated", result)
	case "calendar_cancel":
		return one("event", "cancelled", result)
	case "finance_create_manual_transaction":
		return one("transaction", "recorded", result)
	case "food_add_shopping_items":
		return each("shopping_item", "added", "item")
	case "food_update_shopping_item":
		return one("shopping_item", "updated", result)
	case "food_mark_bought":
		return each("shopping_item", "bought", "")
	case "food_add_pantry_items":
		return each("pantry_item", "added", "")
	case "food_update_pantry_item":
		state := text(result["state"])
		if state == "" {
			state = "updated"
		}
		return one("pantry_item", state, result)
	case "food_plan_meal":
		return one("meal", "planned", result)
	case "food_clear_meal":
		return []recentEffect{{Entity: "meal", Change: "cleared", Summary: activity.Preview(strings.TrimSpace(text(result["date"])+" "+text(result["slot"])), maxRecentSummary)}}
	case "food_add_missing_ingredients":
		var effects []recentEffect
		items, _ := result["items"].([]any)
		for _, raw := range items {
			object, _ := raw.(map[string]any)
			status := text(object["status"])
			if item, ok := object["shoppingItem"].(map[string]any); ok && (status == "added" || status == "merged") {
				effects = append(effects, describeEntity("shopping_item", status, item, location))
			}
		}
		return effects
	case "food_mark_running_low":
		return []recentEffect{{Entity: "staple", Change: "running low", Summary: activity.Preview(text(result["name"]), maxRecentSummary)}}
	case "food_add_staple":
		staple, _ := result["staple"].(map[string]any)
		return one("staple", "added", staple)
	case "food_remove_staple":
		staple, _ := result["staple"].(map[string]any)
		return one("staple", "removed", staple)
	case "recipe_import":
		recipe, _ := result["recipe"].(map[string]any)
		if recipe == nil && text(result["recipeId"]) != "" {
			recipe = map[string]any{"id": result["recipeId"]}
		}
		return one("recipe", "saved", recipe)
	}
	return nil
}

// describeEntity is an entity's id, its name, and a short summary: a Task's
// title and due time, an event's title and when, a transaction's amount and
// counterparty, a list or pantry item's name and amount, a meal's day.
func describeEntity(entity, change string, object map[string]any, location *time.Location) recentEffect {
	effect := recentEffect{Entity: entity, ID: text(object["id"]), Change: change}
	var parts []string
	add := func(part string) {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	switch entity {
	case "task":
		effect.name = text(object["title"])
		add(effect.name)
		if due, err := time.Parse(time.RFC3339, text(object["dueAt"])); err == nil {
			add("due " + due.In(location).Format("Mon 2006-01-02 15:04"))
		}
	case "event":
		effect.name = text(object["title"])
		add(eventLabel(object, location))
		if status := text(object["status"]); status != "" && status != "active" {
			add(status)
		}
	case "transaction":
		amount, _ := object["amount"].(map[string]any)
		if minor, ok := amount["amount"].(float64); ok {
			add(fmt.Sprintf("%.2f %s", minor/100, text(amount["currency"])))
		}
		add(text(object["counterparty"]))
		add(text(object["bookingDate"]))
	case "meal":
		effect.name = text(object["title"])
		add(text(object["date"]) + " " + text(object["slot"]) + ": " + effect.name)
	default:
		effect.name = text(object["name"])
		if effect.name == "" {
			effect.name = text(object["title"])
		}
		add(effect.name)
		add(text(object["amount"]))
		add(text(object["storeName"]))
		if expires := text(object["expiresOn"]); expires != "" {
			add("expires " + expires)
		}
	}
	effect.Summary = activity.Preview(strings.Join(parts, " · "), maxRecentSummary)
	return effect
}

// recentIDFields are the arguments a planned change may name a recent
// entity by, per kind of change.
var recentIDFields = map[string]struct{ field, entity, query string }{
	"task_update":               {"taskId", "task", ""},
	"task_complete":             {"taskId", "task", ""},
	"task_reopen":               {"taskId", "task", ""},
	"calendar_update":           {"eventId", "event", "eventQuery"},
	"calendar_cancel":           {"eventId", "event", "eventQuery"},
	"food_update_shopping_item": {"shoppingItemId", "shopping_item", "itemQuery"},
}

// bind checks the ids a Codex plan names its changes' targets by: each must
// be an entity of that kind the recent captures listed, else the plan is
// invalid. It returns, per action, the recent entity it names (nil for
// none). A Task keeps its taskId, which skips the Task search; an event or
// list item's id is taken out of the arguments (the processor uses it), and
// its title or name becomes the search term when Codex gave none, so the
// action stays valid in Human Review.
func (rc recentContext) bind(p plan) ([]*recentEffect, error) {
	pins := make([]*recentEffect, len(p.Actions))
	for index, action := range p.Actions {
		spec, named := recentIDFields[action.Tool]
		if !named || action.Arguments == nil {
			continue
		}
		raw, present := action.Arguments[spec.field]
		if !present || raw == nil {
			continue
		}
		id, _ := raw.(string)
		effect, ok := rc.entities[strings.TrimSpace(id)]
		if !ok || effect.Entity != spec.entity {
			return nil, fmt.Errorf("%s %v of %s is not a %s the recent captures name", spec.field, raw, action.Tool, strings.ReplaceAll(spec.entity, "_", " "))
		}
		pins[index] = &effect
		if spec.query == "" {
			action.Arguments[spec.field] = effect.ID
			continue
		}
		delete(action.Arguments, spec.field)
		if query, _ := action.Arguments[spec.query].(string); strings.TrimSpace(query) == "" && effect.name != "" {
			action.Arguments[spec.query] = effect.name
		}
	}
	return pins, nil
}

// followsUp is the earlier capture a change's target belongs to (the latest
// listed capture that created or changed it), for the action's audit entry;
// nil when the target is no recent capture's.
func (rc recentContext) followsUp(arguments map[string]any) map[string]any {
	ids := []string{text(arguments["taskId"]), text(arguments["eventId"]), text(arguments["itemId"])}
	if listed, ok := arguments["itemIds"].([]any); ok {
		for _, id := range listed {
			ids = append(ids, text(id))
		}
	}
	for _, id := range ids {
		if effect, ok := rc.entities[id]; ok && id != "" {
			return map[string]any{"inboxItemId": effect.captureID, "entity": effect.Entity, "entityId": effect.ID}
		}
	}
	return nil
}

// candidate is a recent entity as the one Human Review candidate of a change
// Codex was unsure about.
func (e recentEffect) candidate() map[string]any {
	switch e.Entity {
	case "shopping_item":
		return foodCandidates("food_update_shopping_item", []map[string]any{{"id": e.ID, "name": e.name}})[0]
	case "event":
		return map[string]any{"id": e.ID, "kind": "calendar_event", "title": e.Summary, "eventTitle": e.name}
	default:
		return map[string]any{"id": e.ID, "title": e.name}
	}
}
