package processor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

const (
	lemonsCapture = "c0000000-0000-4000-8000-000000000020"
	againItem     = "c0000000-0000-4000-8000-000000000021"
	photoA        = "inbox-media:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	photoB        = "inbox-media:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// lemons is "Buy lemons." captured a minute before the item, in the given
// state, with what processing did for it.
func lemons(state string) recentCapture {
	capture := recentCapture{ID: lemonsCapture, Content: "Buy lemons.", Source: "web", State: state, CreatedAt: followUpAt.Add(-time.Minute)}
	switch state {
	case "resolved":
		capture.Outcome = &recentOutcome{Status: "processed", Actions: []map[string]any{{"tool": "food_add_shopping_items", "result": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": milkItem, "name": "lemons"}, "merged": false}}}}}}
	case "human_review":
		capture.Outcome = &recentOutcome{Status: "human_review", ErrorSummary: "Which list?"}
	case "failed":
		capture.Outcome = &recentOutcome{Status: "failed", ErrorSummary: "gave up"}
	}
	return capture
}

func TestRepeatOfFindsTheSameCaptureAlreadyDoneOrWaiting(t *testing.T) {
	item := inboxItem{ID: againItem, Content: "  buy\tLEMONS. ", CreatedAt: followUpAt}
	moved := func(capture recentCapture, by time.Duration) recentCapture {
		capture.CreatedAt = capture.CreatedAt.Add(by)
		return capture
	}
	withMedia := func(capture recentCapture, references ...string) recentCapture {
		capture.MediaReferences = references
		return capture
	}
	renamed := func(capture recentCapture, content string) recentCapture {
		capture.Content = content
		return capture
	}
	for name, tc := range map[string]struct {
		item     inboxItem
		captures []recentCapture
		want     string
	}{
		"resolved":                       {item, []recentCapture{lemons("resolved")}, lemonsCapture},
		"waiting for review":             {item, []recentCapture{lemons("human_review")}, lemonsCapture},
		"resolved by the owner":          {item, []recentCapture{{ID: lemonsCapture, Content: "Buy lemons.", State: "resolved", CreatedAt: followUpAt.Add(-time.Minute)}}, lemonsCapture},
		"failed":                         {item, []recentCapture{lemons("failed")}, ""},
		"other words":                    {item, []recentCapture{renamed(lemons("resolved"), "Lemons please.")}, ""},
		"other punctuation":              {item, []recentCapture{renamed(lemons("resolved"), "Buy lemons")}, ""},
		"outside the window":             {item, []recentCapture{moved(lemons("resolved"), -20*time.Minute)}, ""},
		"at the window's edge":           {item, []recentCapture{moved(lemons("resolved"), -19*time.Minute)}, lemonsCapture},
		"made after the item":            {item, []recentCapture{moved(lemons("resolved"), 2*time.Minute)}, ""},
		"the item itself":                {item, []recentCapture{{ID: againItem, Content: "Buy lemons.", State: "resolved", CreatedAt: followUpAt}}, ""},
		"images on the earlier one only": {item, []recentCapture{withMedia(lemons("resolved"), photoA)}, ""},
		"other images": {inboxItem{ID: againItem, Content: "Photo", MediaReferences: []string{photoA}, CreatedAt: followUpAt},
			[]recentCapture{withMedia(renamed(lemons("resolved"), "Photo"), photoB)}, ""},
		"the same images": {inboxItem{ID: againItem, Content: "Photo", MediaReferences: []string{photoA, photoB}, CreatedAt: followUpAt},
			[]recentCapture{withMedia(renamed(lemons("resolved"), "photo"), photoB, photoA)}, lemonsCapture},
		// The latest one done or waiting, past a later failed one.
		"latest of several": {item, []recentCapture{
			{ID: "failed-later", Content: "Buy lemons.", State: "failed", CreatedAt: followUpAt.Add(-10 * time.Second)},
			lemons("resolved"),
			{ID: "earliest", Content: "buy lemons.", State: "resolved", CreatedAt: followUpAt.Add(-5 * time.Minute)},
		}, lemonsCapture},
	} {
		got, ok := recentWindow{Items: tc.captures}.repeatOf(tc.item, 20)
		if (tc.want == "") == ok || (ok && got.ID != tc.want) {
			t.Errorf("%s: repeat = %v %q, want %q", name, ok, got.ID, tc.want)
		}
	}
	// A capture still on its way is waited for (order.go), never repeated.
	unfinished := recentWindow{Unfinished: []unfinishedCapture{{ID: lemonsCapture, Content: "Buy lemons.", State: "raw", CreatedAt: followUpAt.Add(-time.Minute)}}}
	if _, ok := unfinished.repeatOf(item, 20); ok {
		t.Error("a capture still on its way counts as done")
	}
}

func TestRepeatSummarySaysWhenAndWhere(t *testing.T) {
	if got := repeatSummary(lemons("resolved"), berlin(t)); got != "Same as the capture from 17:09, already done" {
		t.Errorf("resolved = %q", got)
	}
	if got := repeatSummary(lemons("human_review"), berlin(t)); got != "Same as the capture from 17:09, waiting for your review" {
		t.Errorf("review = %q", got)
	}
	if got := repeatSummary(lemons("resolved"), nil); got != "Same as the capture from 15:09, already done" {
		t.Errorf("no zone = %q", got)
	}
}

// traceTools keeps the trace each inbox_resolve was called with.
type traceTools struct {
	*orderedTools
	resolved []activity.Trace
}

func (t *traceTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if name == "inbox_resolve" {
		t.resolved = append(t.resolved, activity.TraceFrom(ctx))
	}
	return t.orderedTools.Call(ctx, name, args)
}

func TestACaptureSentAgainIsResolvedWithoutCodex(t *testing.T) {
	for _, tc := range []struct {
		state, summary string
	}{
		{"resolved", "Same as the capture from 17:09, already done"},
		{"human_review", "Same as the capture from 17:09, waiting for your review"},
	} {
		at := &orderClock{now: followUpAt}
		tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{{ID: againItem, CorrelationID: againItem, Content: "Buy lemons. ", Source: "voice", CreatedAt: followUpAt}},
			windows: map[string]recentWindow{againItem: {Items: []recentCapture{lemons(tc.state)}}}}
		traced := &traceTools{orderedTools: tools}
		runner, planner, logs := orderRunner(t, tools, at, true)
		runner.Tools = traced
		run(t, runner)

		if len(planner.notes) != 0 || contains(tools.calls, "food_add_shopping_items") || contains(tools.calls, "inbox_set_human_review") {
			t.Fatalf("%s: Codex ran or something was done: calls %v", tc.state, tools.calls)
		}
		if tools.started() != 1 || len(tools.records) != 2 {
			t.Fatalf("%s: started %d, records %v", tc.state, tools.started(), tools.records)
		}
		processed := tools.records[1]
		summary, _ := processed["errorSummary"].(*string)
		if processed["status"] != "processed" || len(processed["actions"].([]map[string]any)) != 0 || processed["duplicateOf"] != lemonsCapture || summary == nil || *summary != tc.summary {
			t.Fatalf("%s: record = %v (%v)", tc.state, processed, summary)
		}
		completed := activity.EventID("inbox.processing.completed", "attempt-1")
		if len(traced.resolved) != 1 || traced.resolved[0].CausationID == nil || *traced.resolved[0].CausationID != completed || traced.resolved[0].CorrelationID != againItem {
			t.Fatalf("%s: resolved with %+v, want caused by the completed processing", tc.state, traced.resolved)
		}
		if len(*logs) != 1 || !strings.Contains((*logs)[0], againItem+" repeats capture "+lemonsCapture) {
			t.Errorf("%s: logs = %q", tc.state, *logs)
		}
	}
}

func TestACaptureSentAgainIsPlannedWhenItIsNoRepeat(t *testing.T) {
	approved := map[string]any{"approvedAction": map[string]any{"tool": "food_add_shopping_items", "arguments": map[string]any{"items": []any{map[string]any{"name": "lemons"}}}}}
	for name, tc := range map[string]struct {
		item    inboxItem
		earlier recentCapture
		codex   bool
	}{
		"the earlier one failed": {inboxItem{ID: againItem, Content: "Buy lemons.", CreatedAt: followUpAt}, lemons("failed"), true},
		"other words":            {inboxItem{ID: againItem, Content: "Need lemons.", CreatedAt: followUpAt}, lemons("resolved"), true},
		"with a photo":           {inboxItem{ID: againItem, Content: "Buy lemons.", MediaReferences: []string{photoA}, CreatedAt: followUpAt}, lemons("resolved"), true},
		// The owner approved this one's action: it runs.
		"approved": {inboxItem{ID: againItem, Content: "Buy lemons.", CreatedAt: followUpAt, ProcessingMetadata: approved}, lemons("resolved"), false},
	} {
		tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{tc.item}, windows: map[string]recentWindow{againItem: {Items: []recentCapture{tc.earlier}}}}
		runner, planner, _ := orderRunner(t, tools, &orderClock{now: followUpAt}, true)
		planner.output = `{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"lemons"}]}}],"confidence":0.9}`
		runner.Media = &fakeMedia{}
		run(t, runner)
		if ran := len(planner.notes) == 1; ran != tc.codex {
			t.Errorf("%s: Codex ran = %v, want %v", name, ran, tc.codex)
		}
		last := tools.records[len(tools.records)-1]
		if last["status"] != "processed" || last["duplicateOf"] != nil || !contains(tools.calls, "food_add_shopping_items") {
			t.Errorf("%s: record = %v, calls = %v", name, last, tools.calls)
		}
	}
}

func TestTheDryRunSaysACaptureIsSentAgain(t *testing.T) {
	tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{{ID: againItem, Content: "Buy lemons.", CreatedAt: followUpAt}},
		windows: map[string]recentWindow{againItem: {Items: []recentCapture{lemons("resolved")}}}}
	planner := &recordingPlanner{output: `{"actions":[]}`}
	proposals, err := (Runner{Planner: planner, Tools: tools, Model: "test", DryRun: true, ContextMinutes: 20, TimeZone: berlin(t)}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(planner.notes) != 0 || len(proposals) != 1 || len(proposals[0].Actions) != 0 || proposals[0].Reason != "Same as the capture from 17:09, already done" || tools.started() != 0 {
		t.Fatalf("proposals = %+v, Codex runs %d", proposals, len(planner.notes))
	}
}

// TestPlanSchemaNamesEveryToolThePromptsUse: a tool the prompts tell Codex
// to use but the schema's enum leaves out is one Codex believes it may not
// plan ("no shopping-item update action is available"), and every tool of
// the enum is one the runner runs.
func TestPlanSchemaNamesEveryToolThePromptsUse(t *testing.T) {
	enum := regexp.MustCompile(`"tool":"([a-z_|]+)"`).FindStringSubmatch(planPrompt)
	if enum == nil {
		t.Fatalf("no tool enum in the plan prompt")
	}
	tools := map[string]bool{}
	for _, tool := range strings.Split(enum[1], "|") {
		tools[tool] = true
		if !allowed(tool) {
			t.Errorf("the enum offers %s, which the runner does not run", tool)
		}
	}
	if !tools["food_update_shopping_item"] {
		t.Error("food_update_shopping_item is not in the enum")
	}
	mentioned := regexp.MustCompile(`\b(?:task|calendar|finance|food|recipe)_[a-z_]+`)
	for name, prompt := range map[string]string{"planPrompt": planPrompt, "movePrompt": movePrompt, "recentContextPrompt": recentContextPrompt} {
		for _, tool := range mentioned.FindAllString(prompt, -1) {
			if !tools[tool] {
				t.Errorf("%s mentions %s, which the enum leaves out", name, tool)
			}
		}
	}
}

// TestAMakeItSixAfterTheCoffeeChangesTheCoffee runs "Actually, make it 6."
// through a fake Codex CLI after captures made the same second, as the
// benchmark makes them: a shopping list item, then the coffee. The prompt
// marks the coffee as the capture just before, with its gap in seconds,
// and offers food_update_shopping_item; the coffee is moved by its id.
func TestAMakeItSixAfterTheCoffeeChangesTheCoffee(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt")
	planFile := filepath.Join(dir, "plan")
	if err := os.WriteFile(planFile, []byte(`{"actions":[{"tool":"calendar_update","arguments":{"eventId":"`+coffeeEvent+`","startAt":"2026-10-17T18:00:00+02:00"}}],"ambiguous":false,"confidence":0.92}`), 0o644); err != nil {
		t.Fatal(err)
	}
	codex := fakeCodex(t, `cat "`+planFile+`" > "$out"`)
	wrapper := filepath.Join(dir, "codex-prompt")
	script := "#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%s' \"$last\" > " + promptFile + "\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := newRecentTools()
	tools.item = inboxItem{ID: followUpItem, Content: "Actually, make it 6.", Source: "web", CreatedAt: followUpAt.Add(18 * time.Millisecond)}
	tools.recent = []recentCapture{
		{ID: annaCapture, Content: "Coffee with Lukas tomorrow at 5 pm.", Source: "web", State: "resolved", CreatedAt: followUpAt, Outcome: &recentOutcome{Status: "processed", Actions: []map[string]any{{
			"tool": "calendar_create", "result": map[string]any{"id": coffeeEvent, "title": "Coffee with Lukas", "allDay": false, "startAt": "2026-10-17T17:00:00+02:00", "endAt": "2026-10-17T18:00:00+02:00", "status": "active"},
		}}}},
		{ID: milkCapture, Content: "Buy lemons.", Source: "web", State: "resolved", CreatedAt: followUpAt.Add(-15 * time.Millisecond), Outcome: &recentOutcome{Status: "processed", Actions: []map[string]any{{
			"tool": "food_add_shopping_items", "result": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": milkItem, "name": "lemons"}, "merged": false}}},
		}}}},
	}
	cli := CodexCLI{Command: wrapper, WorkDir: dir, TimeZone: berlin(t), Now: func() time.Time { return followUpAt }}
	runner := Runner{Planner: cli, Tools: tools, Model: "test", TimeZone: berlin(t), ContextMinutes: 20}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	prompt, _ := os.ReadFile(promptFile)
	for _, want := range []string{
		`"minutesBefore":0,"secondsBefore":0,"justBefore":true,"source":"web","text":"Coffee with Lukas tomorrow at 5 pm."`,
		`"minutesBefore":0,"secondsBefore":0,"source":"web","text":"Buy lemons."`,
		"is about what was done for the justBefore capture when it fits that entity",
		"|food_update_shopping_item|",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %s", want)
		}
	}
	if strings.Count(string(prompt), `"justBefore":true`) != 1 {
		t.Errorf("more than one capture is just before: %s", prompt)
	}
	update := tools.inputs["calendar_update"]
	if update["eventId"] != coffeeEvent || update["startAt"] != "2026-10-17T18:00:00+02:00" || update["endAt"] != "2026-10-17T19:00:00+02:00" || contains(tools.calls, "food_update_shopping_item") {
		t.Fatalf("update = %v, calls = %v", update, tools.calls)
	}
	if actions := tools.processed(t); len(actions) != 1 || actions[0]["followsUp"].(map[string]any)["inboxItemId"] != annaCapture || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("actions = %v", actions)
	}
}
