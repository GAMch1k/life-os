package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	milkCapture   = "c0000000-0000-4000-8000-000000000001"
	annaCapture   = "c0000000-0000-4000-8000-000000000002"
	sophieCapture = "c0000000-0000-4000-8000-000000000003"
	followUpItem  = "c0000000-0000-4000-8000-0000000000ff"
	milkItem      = "a0000000-0000-4000-8000-000000000001"
	coffeeEvent   = "e0000000-0000-4000-8000-000000000001"
	sophieTask    = "70000000-0000-4000-8000-000000000001"
)

// followUpAt is when the follow-up capture was made; the recent captures
// came minutes before.
var followUpAt = time.Date(2026, 10, 16, 15, 10, 0, 0, time.UTC)

// recentCaptures is what Inbox lists before the follow-up, newest first: a
// Task, an event, and a shopping list item made by three captures.
func recentCaptures() []recentCapture {
	processed := func(actions ...map[string]any) *recentOutcome {
		return &recentOutcome{Status: "processed", Actions: actions}
	}
	return []recentCapture{
		{ID: sophieCapture, Content: "Remind me to call Sophie", Source: "voice", State: "resolved", CreatedAt: followUpAt.Add(-2 * time.Minute), Outcome: processed(map[string]any{
			"tool": "task_create", "arguments": map[string]any{"title": "Call Sophie"}, "result": map[string]any{"id": sophieTask, "title": "Call Sophie", "dueAt": nil},
		})},
		{ID: annaCapture, Content: "Coffee with Lukas tomorrow at 5 pm", Source: "web", State: "resolved", CreatedAt: followUpAt.Add(-5 * time.Minute), Outcome: processed(map[string]any{
			"tool": "calendar_create", "result": map[string]any{"id": coffeeEvent, "title": "Coffee with Lukas", "allDay": false, "startAt": "2026-10-17T17:00:00+02:00", "endAt": "2026-10-17T18:00:00+02:00", "status": "active"},
		})},
		{ID: milkCapture, Content: "Milch kaufen", Source: "watch", State: "resolved", CreatedAt: followUpAt.Add(-8 * time.Minute), Outcome: processed(map[string]any{
			"tool": "food_add_shopping_items", "result": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": milkItem, "name": "Milch", "amount": nil}, "merged": false}}},
		})},
	}
}

// recentTools serves one follow-up capture, the recent captures before it,
// and the Life OS tools a follow-up uses, recording every call.
type recentTools struct {
	*fakeTools
	item    inboxItem
	recent  []recentCapture
	event   map[string]any
	failing map[string]error
	inputs  map[string]map[string]any
	queries []string
	review  map[string]any
}

func newRecentTools() *recentTools {
	return &recentTools{
		fakeTools: &fakeTools{},
		item:      inboxItem{ID: followUpItem, Content: "Actually oat milk", Source: "web", CreatedAt: followUpAt},
		recent:    recentCaptures(),
		event:     map[string]any{"id": coffeeEvent, "title": "Coffee with Lukas", "allDay": false, "startAt": "2026-10-17T17:00:00+02:00", "endAt": "2026-10-17T18:00:00+02:00", "status": "active"},
		failing:   map[string]error{},
		inputs:    map[string]map[string]any{},
	}
}

func (r *recentTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if err, failing := r.failing[name]; failing {
		r.calls = append(r.calls, name)
		return nil, err
	}
	switch name {
	case "inbox_list":
		r.calls = append(r.calls, name)
		raw, _ := json.Marshal(inboxList{Items: []inboxItem{r.item}})
		return raw, nil
	case "inbox_recent_captures":
		r.calls = append(r.calls, name)
		r.queries = append(r.queries, args["query"].(string))
		raw, _ := json.Marshal(map[string]any{"items": r.recent})
		return raw, nil
	case "calendar_get":
		r.calls = append(r.calls, name)
		raw, _ := json.Marshal(r.event)
		return raw, nil
	case "task_search":
		r.calls = append(r.calls, name)
		raw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"id": sophieTask, "title": "Call Sophie"}}})
		return raw, nil
	case "inbox_set_human_review":
		r.calls = append(r.calls, name)
		r.review = args
		return json.RawMessage(`{}`), nil
	case "task_update", "calendar_update", "calendar_cancel", "food_update_shopping_item", "food_add_shopping_items", "task_create", "calendar_create":
		r.calls = append(r.calls, name)
		r.inputs[name] = args
		raw, _ := json.Marshal(map[string]any{"id": "result-of-" + name})
		return raw, nil
	}
	return r.fakeTools.Call(ctx, name, args)
}

// processed is the actions of the processed attempt.
func (r *recentTools) processed(t *testing.T) []map[string]any {
	t.Helper()
	for _, record := range r.records {
		if record["status"] == "processed" {
			return record["actions"].([]map[string]any)
		}
	}
	t.Fatalf("no processed attempt; calls = %v, records = %v", r.calls, r.records)
	return nil
}

// recordingPlanner returns one plan and keeps the recent captures each run
// was given.
type recordingPlanner struct {
	output string
	notes  []string
}

func (p *recordingPlanner) Plan(ctx context.Context, _ string) (string, error) {
	p.notes = append(p.notes, recentContextFrom(ctx))
	return p.output, nil
}

func runFollowUp(t *testing.T, tools *recentTools, plan string) *recordingPlanner {
	t.Helper()
	planner := &recordingPlanner{output: plan}
	runner := Runner{Planner: planner, Tools: tools, Model: "test", TimeZone: berlin(t), ContextMinutes: 20}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return planner
}

func TestRecentContextIsBuiltCappedAndClipped(t *testing.T) {
	item := inboxItem{ID: followUpItem, CreatedAt: followUpAt}
	captures := append([]recentCapture{{ID: followUpItem, Content: "the item itself", State: "resolved", CreatedAt: followUpAt}}, recentCaptures()...)
	captures = append(captures,
		recentCapture{ID: "review", Content: "Meeting with Anna at 5", Source: "web", State: "human_review", CreatedAt: followUpAt.Add(-9 * time.Minute), Outcome: &recentOutcome{Status: "human_review", ErrorSummary: "Several Calendar events match: choose the one to change"}},
		recentCapture{ID: "failed", Content: "Pay " + strings.Repeat("the long bill ", 50), Source: "web", State: "failed", CreatedAt: followUpAt.Add(-10 * time.Minute), Outcome: &recentOutcome{Status: "failed", ErrorSummary: "gave up"}},
		recentCapture{ID: "by-hand", Content: "Resolved by hand", Source: "manual", State: "resolved", CreatedAt: followUpAt.Add(-11 * time.Minute)},
		recentCapture{ID: "nothing", Content: "I bought the light bulbs", Source: "web", State: "resolved", CreatedAt: followUpAt.Add(-12 * time.Minute), Outcome: &recentOutcome{Status: "processed", Actions: []map[string]any{{"tool": "none", "reason": "Nothing matches"}}}},
	)
	rc := buildRecentContext(item, captures, 20, berlin(t))
	if !strings.HasPrefix(rc.note, recentContextPrompt+"Recent captures from the 20 minutes before this item: [") || !strings.HasSuffix(rc.note, "]. ") {
		t.Fatalf("note = %s", rc.note)
	}
	if strings.Contains(rc.note, "the item itself") {
		t.Fatal("the item itself is listed")
	}
	var listed []recentEntry
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(rc.note, recentContextPrompt+"Recent captures from the 20 minutes before this item: "), ". ")), &listed); err != nil {
		t.Fatalf("listed captures are not JSON: %v\n%s", err, rc.note)
	}
	var texts []string
	for _, entry := range listed {
		texts = append(texts, entry.Text)
	}
	if len(listed) != 7 || listed[0].Text != "I bought the light bulbs" || listed[6].Text != "Remind me to call Sophie" {
		t.Fatalf("listed = %q, want the seven oldest first", texts)
	}
	milk := listed[4]
	want := recentEntry{CapturedAt: "Friday 2026-10-16 17:02", MinutesBefore: 8, SecondsBefore: 480, Source: "watch", Text: "Milch kaufen", State: "resolved", Done: []recentEffect{{Entity: "shopping_item", ID: milkItem, Change: "added", Summary: "Milch"}}}
	if !reflect.DeepEqual(milk, want) {
		t.Fatalf("milk = %+v, want %+v", milk, want)
	}
	// The newest, made just before the item, says so; no other does.
	for index, entry := range listed {
		if entry.JustBefore != (index == 6) {
			t.Errorf("%s: justBefore = %v", entry.Text, entry.JustBefore)
		}
	}
	if !strings.Contains(rc.note, `"minutesBefore":2,"secondsBefore":120,"justBefore":true,"source":"voice","text":"Remind me to call Sophie"`) {
		t.Errorf("the capture just before is not marked: %s", rc.note)
	}
	if coffee := listed[5].Done; len(coffee) != 1 || coffee[0].ID != coffeeEvent || coffee[0].Summary != "Coffee with Lukas · Sat 17 Oct 2026 17:00–18:00" || coffee[0].Change != "created" {
		t.Fatalf("coffee = %+v", coffee)
	}
	if sophie := listed[6].Done; len(sophie) != 1 || sophie[0].Entity != "task" || sophie[0].ID != sophieTask || sophie[0].Summary != "Call Sophie · no due date" {
		t.Fatalf("sophie = %+v", sophie)
	}
	if review := listed[3]; review.Note != "waiting for the owner's decision, nothing done yet: Several Calendar events match: choose the one to change" || len(review.Done) != 0 {
		t.Errorf("review = %+v", review)
	}
	if failed := listed[2]; failed.Note != "processing failed, nothing done" || len([]rune(failed.Text)) != maxRecentText || !strings.HasSuffix(failed.Text, "…") {
		t.Errorf("failed = %+v (%d characters)", failed, len([]rune(failed.Text)))
	}
	if listed[0].Note != "nothing was changed" || listed[0].Done != nil {
		t.Errorf("nothing done = %+v", listed[0])
	}
	if !strings.Contains(rc.note, `"text":"Resolved by hand","state":"resolved","note":"resolved by the owner"`) {
		t.Errorf("resolved by hand is not said: %s", rc.note)
	}
	if len(rc.entities) != 3 || rc.entities[milkItem].captureID != milkCapture || rc.entities[coffeeEvent].captureID != annaCapture || rc.entities[sophieTask].name != "Call Sophie" {
		t.Errorf("entities = %+v", rc.entities)
	}

	// At most ten captures, eight entities each, and the size cap: the
	// oldest go first.
	var many []recentCapture
	for i := 0; i < 14; i++ {
		var items []any
		for j := 0; j < 12; j++ {
			items = append(items, map[string]any{"item": map[string]any{"id": fmt.Sprintf("item-%d-%d", i, j), "name": strings.Repeat("Hafermilch ", 3)}, "merged": false})
		}
		many = append(many, recentCapture{ID: fmt.Sprintf("capture-%d", i), Content: fmt.Sprintf("capture %d", i), State: "resolved", CreatedAt: followUpAt.Add(-time.Duration(i+1) * time.Minute),
			Outcome: &recentOutcome{Status: "processed", Actions: []map[string]any{{"tool": "food_add_shopping_items", "result": map[string]any{"items": items}}}}})
	}
	rc = buildRecentContext(item, many, 20, berlin(t))
	section := strings.TrimPrefix(rc.note, recentContextPrompt)
	if len(section) > maxRecentContext+100 {
		t.Fatalf("the list is %d characters, want at most about %d", len(section), maxRecentContext)
	}
	if !strings.Contains(section, `"text":"capture 0"`) || strings.Contains(section, `"text":"capture 9"`) || strings.Contains(section, "item-0-8") {
		t.Fatalf("want the newest captures, 8 entities each: %s", section)
	}
	if _, listedOld := rc.entities["item-9-0"]; listedOld || len(rc.entities)%maxRecentEffects != 0 {
		t.Errorf("entities include ones not listed: %d", len(rc.entities))
	}

	// Nothing recent, or only the item: no section at all.
	if rc := buildRecentContext(item, []recentCapture{{ID: followUpItem}}, 20, berlin(t)); rc.note != "" || rc.entities != nil {
		t.Errorf("only the item = %+v", rc)
	}
}

func TestEffectsNameWhatEachToolDid(t *testing.T) {
	location := berlin(t)
	for _, tc := range []struct {
		name   string
		action map[string]any
		want   []recentEffect
	}{
		{"merged list item", map[string]any{"tool": "food_add_shopping_items", "result": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": "i1", "name": "milk", "amount": "2 l", "storeName": "Lidl"}, "merged": true}}}},
			[]recentEffect{{Entity: "shopping_item", ID: "i1", Change: "merged", Summary: "milk · 2 l · Lidl"}}},
		{"bought", map[string]any{"tool": "food_mark_bought", "result": map[string]any{"items": []any{map[string]any{"id": "i2", "name": "Бананы"}}}},
			[]recentEffect{{Entity: "shopping_item", ID: "i2", Change: "bought", Summary: "Бананы"}}},
		{"pantry", map[string]any{"tool": "food_add_pantry_items", "result": map[string]any{"items": []any{map[string]any{"id": "p1", "name": "Joghurt", "expiresOn": "2026-10-23"}}}},
			[]recentEffect{{Entity: "pantry_item", ID: "p1", Change: "added", Summary: "Joghurt · expires 2026-10-23"}}},
		{"used up", map[string]any{"tool": "food_update_pantry_item", "result": map[string]any{"id": "p2", "name": "Milch", "state": "used_up"}},
			[]recentEffect{{Entity: "pantry_item", ID: "p2", Change: "used_up", Summary: "Milch"}}},
		{"cash", map[string]any{"tool": "finance_create_manual_transaction", "result": map[string]any{"id": "t1", "amount": map[string]any{"amount": -1250.0, "currency": "EUR"}, "counterparty": "Bäckerei", "bookingDate": "2026-10-16"}},
			[]recentEffect{{Entity: "transaction", ID: "t1", Change: "recorded", Summary: "-12.50 EUR · Bäckerei · 2026-10-16"}}},
		{"task due", map[string]any{"tool": "task_update", "result": map[string]any{"id": "k1", "title": "Call Anna", "dueAt": "2026-10-17T07:00:00Z"}},
			[]recentEffect{{Entity: "task", ID: "k1", Change: "updated", Summary: "Call Anna · due Sat 2026-10-17 09:00"}}},
		{"meal", map[string]any{"tool": "food_plan_meal", "result": map[string]any{"id": "m1", "date": "2026-10-21", "slot": "dinner", "title": "Lasagne"}},
			[]recentEffect{{Entity: "meal", ID: "m1", Change: "planned", Summary: "2026-10-21 dinner: Lasagne"}}},
		{"meal cleared", map[string]any{"tool": "food_clear_meal", "result": map[string]any{"date": "2026-10-17", "slot": "breakfast", "cleared": true}},
			[]recentEffect{{Entity: "meal", Change: "cleared", Summary: "2026-10-17 breakfast"}}},
		{"ingredients", map[string]any{"tool": "food_add_missing_ingredients", "result": map[string]any{"items": []any{
			map[string]any{"status": "added", "shoppingItem": map[string]any{"id": "i3", "name": "Hackfleisch"}},
			map[string]any{"status": "in_pantry", "pantryItem": map[string]any{"id": "p3", "name": "Spaghetti"}},
		}}}, []recentEffect{{Entity: "shopping_item", ID: "i3", Change: "added", Summary: "Hackfleisch"}}},
		{"staple", map[string]any{"tool": "food_add_staple", "result": map[string]any{"created": true, "staple": map[string]any{"id": "s1", "name": "Kaffee"}}},
			[]recentEffect{{Entity: "staple", ID: "s1", Change: "added", Summary: "Kaffee"}}},
		{"recipe", map[string]any{"tool": "recipe_import", "result": map[string]any{"saved": true, "recipe": map[string]any{"id": "r1", "title": "Omas Rindergulasch"}}},
			[]recentEffect{{Entity: "recipe", ID: "r1", Change: "saved", Summary: "Omas Rindergulasch"}}},
		{"recipe draft", map[string]any{"tool": "recipe_save_draft", "result": map[string]any{"saved": true, "recipeId": "r2", "recipe": map[string]any{"id": "r2", "title": "Crispy chicken"}}},
			[]recentEffect{{Entity: "recipe", ID: "r2", Change: "saved", Summary: "Crispy chicken"}}},
		{"cancelled event", map[string]any{"tool": "calendar_cancel", "result": map[string]any{"id": "e1", "title": "Haircut", "allDay": true, "startDate": "2026-10-20", "endDate": "2026-10-21", "status": "cancelled"}},
			[]recentEffect{{Entity: "event", ID: "e1", Change: "cancelled", Summary: "Haircut · Tue 20 Oct 2026 – Wed 21 Oct 2026 (all day) · cancelled"}}},
		{"nothing done", map[string]any{"tool": "none", "reason": "Nothing matches"}, nil},
		{"failed call", map[string]any{"tool": "task_create", "arguments": map[string]any{"title": "x"}}, nil},
	} {
		got := effectsOf(tc.action, location)
		for i := range got {
			got[i].name = ""
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: effects = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestRunnerGivesCodexTheRecentCaptures(t *testing.T) {
	tools := newRecentTools()
	planner := runFollowUp(t, tools, `{"actions":[],"ambiguous":true,"confidence":0.2,"reason":"unclear"}`)
	if len(tools.queries) != 1 {
		t.Fatalf("recent captures read %d times", len(tools.queries))
	}
	query, _ := url.ParseQuery(tools.queries[0])
	if query.Get("before") != followUpItem || query.Get("minutes") != "20" || query.Get("limit") != "10" {
		t.Fatalf("query = %s", tools.queries[0])
	}
	if len(planner.notes) != 1 || !strings.HasPrefix(planner.notes[0], recentContextPrompt) || !strings.Contains(planner.notes[0], `"id":"`+milkItem+`"`) {
		t.Fatalf("Codex was not given the recent captures: %v", planner.notes)
	}

	// Off: no read, no section.
	tools = newRecentTools()
	planner = &recordingPlanner{output: `{"actions":[],"ambiguous":true,"confidence":0.2}`}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "test"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if contains(tools.calls, "inbox_recent_captures") || planner.notes[0] != "" {
		t.Fatalf("context off: calls = %v, note = %q", tools.calls, planner.notes[0])
	}

	// Unreadable: retried later, Codex is not run.
	tools = newRecentTools()
	tools.failing["inbox_recent_captures"] = errors.New("MCP tool inbox_recent_captures error: connection refused")
	planner = runFollowUp(t, tools, `{}`)
	if len(planner.notes) != 0 {
		t.Fatal("Codex ran without the recent captures")
	}
	last := tools.records[len(tools.records)-1]
	if last["status"] != "retryable" || !strings.HasPrefix(last["errorSummary"].(string), "recent captures unavailable:") {
		t.Fatalf("record = %v", last)
	}

	// The dry run plans with them too.
	tools = newRecentTools()
	planner = &recordingPlanner{output: `{"actions":[]}`}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "test", DryRun: true, ContextMinutes: 15}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(planner.notes) != 1 || !strings.Contains(planner.notes[0], "from the 15 minutes before") {
		t.Fatalf("dry run notes = %v", planner.notes)
	}
}

func TestPlanPromptListsTheRecentCapturesBeforeTheItem(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt")
	codex := fakeCodex(t, `printf '{}' > "$out"`)
	wrapper := filepath.Join(dir, "codex-prompt")
	script := "#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%s' \"$last\" > " + promptFile + "\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := CodexCLI{Command: wrapper, WorkDir: dir, TimeZone: berlin(t), Now: func() time.Time { return followUpAt }}
	rc := buildRecentContext(inboxItem{ID: followUpItem, CreatedAt: followUpAt}, recentCaptures(), 20, berlin(t))
	if _, err := cli.Plan(WithRecentContext(context.Background(), rc.note), `{"content":"Actually oat milk"}`); err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(promptFile)
	section := strings.Index(string(prompt), "Recent captures (context only).")
	clock := strings.Index(string(prompt), "The owner's timezone is Europe/Berlin")
	if !strings.HasPrefix(string(prompt), planPrompt) || clock < 0 || section < clock || !strings.HasSuffix(string(prompt), rc.note+`Inbox item: {"content":"Actually oat milk"}`) {
		t.Fatalf("prompt = %s", prompt)
	}
	for _, rule := range []string{"Use the list only to understand what this item refers to or corrects", "never an earlier capture's request again", "food_update_shopping_item and shoppingItemId", "set ambiguous true and actions to []",
		// P5-22: repeats, corrections that name nothing, new times, and
		// captures still on their way.
		`reason "Already done for the capture from HH:MM: <its text>"`, "is such a change, never a question", `"make it tomorrow morning" is dueAt tomorrow at 09:00`, "raw or processing means it is still on its way",
		// P5-23: an exact repeat is the processor's; a repeat in other
		// words, and a correction that names nothing, the capture just
		// before first.
		"in other words", "shopping list items and recipe links are still planned as usual", "When that capture failed, plan the item as usual",
		"how long before this item (minutesBefore, and secondsBefore to the second), justBefore true on the one capture made just before this item",
		`A correction that names nothing ("make it 6", "actually tomorrow", "no, 7 pm", "make it tomorrow morning") is about what was done for the justBefore capture when it fits that entity`,
		"only when it does not fit, about the latest earlier capture in the list whose done it fits; when it is still unclear which entity it means, set ambiguous true"} {
		if !strings.Contains(string(prompt), rule) {
			t.Errorf("prompt lacks %q", rule)
		}
	}
	// Without recent captures the prompt is what it was.
	if _, err := cli.Plan(context.Background(), `{"content":"Buy milk"}`); err != nil {
		t.Fatal(err)
	}
	prompt, _ = os.ReadFile(promptFile)
	if strings.Contains(string(prompt), "Recent captures") {
		t.Fatalf("prompt without recent captures = %s", prompt)
	}
}

func TestFollowUpRenamesTheShoppingItemInsteadOfAddingOne(t *testing.T) {
	tools := newRecentTools()
	runFollowUp(t, tools, `{"actions":[{"tool":"food_update_shopping_item","arguments":{"shoppingItemId":"`+milkItem+`","name":"Hafermilch"}}],"confidence":0.95}`)
	if contains(tools.calls, "food_add_shopping_items") || contains(tools.calls, "food_list_shopping_items") {
		t.Fatalf("calls = %v, want a rename of the listed item only", tools.calls)
	}
	update := tools.inputs["food_update_shopping_item"]
	key, _ := update["idempotencyKey"].(string)
	if update["itemId"] != milkItem || update["name"] != "Hafermilch" || !strings.HasPrefix(key, "food-") || len(update) != 3 {
		t.Fatalf("update = %v", update)
	}
	actions := tools.processed(t)
	if len(actions) != 1 || !reflect.DeepEqual(actions[0]["followsUp"], map[string]any{"inboxItemId": milkCapture, "entity": "shopping_item", "entityId": milkItem}) {
		t.Fatalf("actions = %v, want the rename linked to the milk capture", actions)
	}
	if !contains(tools.calls, "inbox_resolve") {
		t.Fatal("the follow-up was not resolved")
	}
}

func TestFollowUpMovesTheEventAndKeepsItsLength(t *testing.T) {
	tools := newRecentTools()
	tools.item.Content = "Actually make it 6"
	runFollowUp(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventId":"`+coffeeEvent+`","startAt":"2026-10-17T18:00:00+02:00"}}],"confidence":0.9}`)
	if contains(tools.calls, "calendar_search") || contains(tools.calls, "calendar_create") {
		t.Fatalf("calls = %v, want the listed event changed without a search", tools.calls)
	}
	update := tools.inputs["calendar_update"]
	if update["eventId"] != coffeeEvent || update["startAt"] != "2026-10-17T18:00:00+02:00" || update["endAt"] != "2026-10-17T19:00:00+02:00" || update["eventQuery"] != nil {
		t.Fatalf("update = %v", update)
	}
	if actions := tools.processed(t); !reflect.DeepEqual(actions[0]["followsUp"], map[string]any{"inboxItemId": annaCapture, "entity": "event", "entityId": coffeeEvent}) {
		t.Fatalf("actions = %v", actions)
	}
}

func TestFollowUpReschedulesTheTaskItNames(t *testing.T) {
	tools := newRecentTools()
	tools.item.Content = "Make it tomorrow morning"
	runFollowUp(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskId":"`+sophieTask+`","dueAt":"2026-10-17T09:00:00+02:00"}}],"confidence":0.9}`)
	if contains(tools.calls, "task_search") || contains(tools.calls, "task_create") {
		t.Fatalf("calls = %v", tools.calls)
	}
	if update := tools.inputs["task_update"]; update["taskId"] != sophieTask || update["dueAt"] != "2026-10-17T09:00:00+02:00" {
		t.Fatalf("update = %v", update)
	}
	if actions := tools.processed(t); !reflect.DeepEqual(actions[0]["followsUp"], map[string]any{"inboxItemId": sophieCapture, "entity": "task", "entityId": sophieTask}) {
		t.Fatalf("actions = %v", actions)
	}
}

func TestAChangeFoundBySearchIsLinkedToTheRecentCapture(t *testing.T) {
	tools := newRecentTools()
	runFollowUp(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"Sophie","dueAt":"2026-10-17T09:00:00+02:00"}}],"confidence":0.9}`)
	if !contains(tools.calls, "task_search") {
		t.Fatalf("calls = %v", tools.calls)
	}
	if actions := tools.processed(t); actions[0]["followsUp"] == nil {
		t.Fatalf("actions = %v, want the change linked", actions)
	}
	// A new entity next to a follow-up is created as usual, unlinked.
	tools = newRecentTools()
	tools.item.Content = "Add bread too"
	runFollowUp(t, tools, `{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"Brot"}]}}],"confidence":0.9}`)
	if actions := tools.processed(t); len(actions) != 1 || actions[0]["followsUp"] != nil || contains(tools.calls, "food_update_shopping_item") {
		t.Fatalf("actions = %v, calls = %v", actions, tools.calls)
	}
}

func TestFollowUpNamingAnIDTheRecentCapturesDidNotListIsInvalid(t *testing.T) {
	for name, plan := range map[string]string{
		"unknown task":            `{"actions":[{"tool":"task_complete","arguments":{"taskId":"70000000-0000-4000-8000-0000000000aa"}}],"confidence":0.9}`,
		"an event as a task":      `{"actions":[{"tool":"task_update","arguments":{"taskId":"` + coffeeEvent + `","title":"x"}}],"confidence":0.9}`,
		"unknown list item":       `{"actions":[{"tool":"food_update_shopping_item","arguments":{"shoppingItemId":"nope","name":"Hafermilch"}}],"confidence":0.9}`,
		"a list item as an event": `{"actions":[{"tool":"calendar_update","arguments":{"eventId":"` + milkItem + `","title":"x"}}],"confidence":0.9}`,
		"an id on another tool":   `{"actions":[{"tool":"food_mark_bought","arguments":{"itemQuery":"milk","shoppingItemId":"` + milkItem + `"}}],"confidence":0.9}`,
	} {
		tools := newRecentTools()
		runFollowUp(t, tools, plan)
		for _, call := range tools.calls {
			if call == "task_update" || call == "task_complete" || call == "food_update_shopping_item" || call == "calendar_update" || call == "food_mark_bought" {
				t.Errorf("%s: %s ran", name, call)
			}
		}
		last := tools.records[len(tools.records)-1]
		if last["status"] != "retryable" || !strings.Contains(fmt.Sprint(last["errorSummary"]), "invalid") {
			t.Errorf("%s: record = %v", name, last)
		}
	}
	// Without recent captures, Codex cannot name a Task by id at all.
	tools := newRecentTools()
	tools.recent = nil
	runFollowUp(t, tools, `{"actions":[{"tool":"task_complete","arguments":{"taskId":"`+sophieTask+`"}}],"confidence":0.9}`)
	if contains(tools.calls, "task_complete") {
		t.Fatal("a Task named by an id nobody listed was completed")
	}
}

func TestFollowUpCancellationAndDoubtGoToTheOwner(t *testing.T) {
	tools := newRecentTools()
	tools.item.Content = "No, cancel the coffee"
	runFollowUp(t, tools, `{"actions":[{"tool":"calendar_cancel","arguments":{"eventId":"`+coffeeEvent+`"}}],"confidence":0.9}`)
	if contains(tools.calls, "calendar_cancel") || tools.review == nil {
		t.Fatalf("calls = %v", tools.calls)
	}
	candidates := tools.review["candidates"].([]map[string]any)
	proposed := tools.review["proposedAction"].(plannedAction)
	if len(candidates) != 1 || candidates[0]["id"] != coffeeEvent || proposed.Arguments["eventQuery"] != "Coffee with Lukas" || proposed.Arguments["eventId"] != nil {
		t.Fatalf("review = %v", tools.review)
	}

	tools = newRecentTools()
	runFollowUp(t, tools, `{"actions":[{"tool":"food_update_shopping_item","arguments":{"shoppingItemId":"`+milkItem+`","name":"Hafermilch"}}],"confidence":0.6,"reason":"maybe"}`)
	candidates, _ = tools.review["candidates"].([]map[string]any)
	proposed, _ = tools.review["proposedAction"].(plannedAction)
	if contains(tools.calls, "food_update_shopping_item") || len(candidates) != 1 || candidates[0]["id"] != milkItem || candidates[0]["kind"] != "shopping_item" || proposed.Arguments["itemQuery"] != "Milch" {
		t.Fatalf("unsure rename: calls = %v, review = %v", tools.calls, tools.review)
	}
	if tools.review["errorSummary"] != "ambiguous or low-confidence shopping list item selection" {
		t.Errorf("reason = %v", tools.review["errorSummary"])
	}

	// A cancelled event is not changed.
	tools = newRecentTools()
	tools.event["status"] = "cancelled"
	runFollowUp(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventId":"`+coffeeEvent+`","startAt":"2026-10-17T18:00:00+02:00"}}],"confidence":0.9}`)
	if contains(tools.calls, "calendar_update") || tools.review == nil {
		t.Fatalf("a cancelled event: calls = %v", tools.calls)
	}

	// A new name already on the list: Food refuses, the owner decides.
	tools = newRecentTools()
	tools.failing["food_update_shopping_item"] = errors.New(`MCP tool food_update_shopping_item rejected: service returned 409: {"error":{"code":"already_on_list"}}`)
	runFollowUp(t, tools, `{"actions":[{"tool":"food_update_shopping_item","arguments":{"shoppingItemId":"`+milkItem+`","name":"Hafermilch"}}],"confidence":0.9}`)
	if tools.review == nil || !strings.Contains(tools.review["errorSummary"].(string), "already on the shopping list") {
		t.Fatalf("already on the list: review = %v, records = %v", tools.review, tools.records)
	}
}

func TestAnApprovedChangeOfARecentCapturesEventIsLinked(t *testing.T) {
	tools := newRecentTools()
	tools.item.Content = "No, cancel the coffee"
	// The owner approved the cancellation the follow-up proposed.
	tools.item.ProcessingMetadata = map[string]any{"approvedAction": map[string]any{"tool": "calendar_cancel", "arguments": map[string]any{"eventQuery": "Coffee with Lukas", "taskId": coffeeEvent}}}
	planner := runFollowUp(t, tools, `{}`)
	if len(planner.notes) != 0 {
		t.Fatal("Codex ran for an approved action")
	}
	if cancel := tools.inputs["calendar_cancel"]; cancel["eventId"] != coffeeEvent {
		t.Fatalf("cancel = %v, calls = %v", cancel, tools.calls)
	}
	if actions := tools.processed(t); !reflect.DeepEqual(actions[0]["followsUp"], map[string]any{"inboxItemId": annaCapture, "entity": "event", "entityId": coffeeEvent}) {
		t.Fatalf("actions = %v", actions)
	}
}

func TestShoppingItemChangeIsValidatedAndFoundBySearch(t *testing.T) {
	for name, tc := range map[string]struct {
		arguments map[string]any
		valid     bool
	}{
		"rename":          {map[string]any{"itemQuery": "milk", "name": "oat milk"}, true},
		"amount and note": {map[string]any{"itemQuery": "milk", "amount": "2 l", "note": "Bio"}, true},
		"nothing":         {map[string]any{"itemQuery": "milk"}, false},
		"empty name":      {map[string]any{"itemQuery": "milk", "name": " "}, false},
		"long name":       {map[string]any{"itemQuery": "milk", "name": strings.Repeat("x", 201)}, false},
		"not text":        {map[string]any{"itemQuery": "milk", "amount": 2.0}, false},
		"no item":         {map[string]any{"name": "oat milk"}, false},
		"unknown field":   {map[string]any{"itemQuery": "milk", "aisle": "dairy"}, false},
	} {
		if got := validAction(plannedAction{Tool: "food_update_shopping_item", Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: valid = %v, want %v", name, got, tc.valid)
		}
	}
	got := foodArguments("food_update_shopping_item", map[string]any{"itemQuery": "milk", "itemQueryAlternatives": []any{"Milch"}, "name": " oat milk ", "amount": "1 l"}, milkItem)
	if !reflect.DeepEqual(got, map[string]any{"itemId": milkItem, "name": "oat milk", "amount": "1 l"}) {
		t.Fatalf("arguments = %v", got)
	}
}

func TestRunPlansTheOldestCaptureFirst(t *testing.T) {
	tools := &orderTools{fakeTools: &fakeTools{}}
	planner := &orderPlanner{}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "test"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(planner.contents, []string{"Buy milk", "Actually oat milk", "Add bread too"}) {
		t.Fatalf("planned %q, want the oldest capture first", planner.contents)
	}
}

// orderTools lists three raw captures newest first, as Inbox does.
type orderTools struct{ *fakeTools }

func (o *orderTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if name == "inbox_list" {
		raw, _ := json.Marshal(inboxList{Items: []inboxItem{
			{ID: "3", Content: "Add bread too", CreatedAt: followUpAt.Add(time.Minute)},
			{ID: "2", Content: "Actually oat milk", CreatedAt: followUpAt},
			{ID: "1", Content: "Buy milk", CreatedAt: followUpAt.Add(-time.Minute)},
		}})
		return raw, nil
	}
	return o.fakeTools.Call(ctx, name, args)
}

type orderPlanner struct{ contents []string }

func (p *orderPlanner) Plan(_ context.Context, prompt string) (string, error) {
	var item struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(prompt), &item)
	p.contents = append(p.contents, item.Content)
	return `{"actions":[],"ambiguous":true,"confidence":0.1}`, nil
}

func TestLoadConfigReadsTheContextMinutes(t *testing.T) {
	for value, want := range map[string]int{"": 20, "0": 0, "15": 15, " 120 ": 120} {
		t.Setenv("INBOX_CONTEXT_MINUTES", value)
		cfg, err := LoadConfig()
		if err != nil || cfg.ContextMinutes != want {
			t.Errorf("INBOX_CONTEXT_MINUTES=%q: %d, %v; want %d", value, cfg.ContextMinutes, err, want)
		}
	}
	for _, value := range []string{"-1", "121", "twenty", "1.5"} {
		t.Setenv("INBOX_CONTEXT_MINUTES", value)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "INBOX_CONTEXT_MINUTES") {
			t.Errorf("INBOX_CONTEXT_MINUTES=%q accepted: %v", value, err)
		}
	}
}
