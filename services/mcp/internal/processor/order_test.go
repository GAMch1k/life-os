package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	coffeeCapture = "c0000000-0000-4000-8000-000000000010"
	sixItem       = "c0000000-0000-4000-8000-000000000011"
	laterItem     = "c0000000-0000-4000-8000-000000000012"
	farItem       = "c0000000-0000-4000-8000-000000000013"
)

// orderedTools polls the given captures and answers each one's recent
// captures read from windows, by the capture's id.
type orderedTools struct {
	*fakeTools
	items   []inboxItem
	windows map[string]recentWindow
	// reads are the captures whose recent captures were read, in order.
	reads []string
	list  string
}

func (o *orderedTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	switch name {
	case "inbox_list":
		o.calls = append(o.calls, name)
		o.list = args["query"].(string)
		raw, _ := json.Marshal(inboxList{Items: o.items})
		return raw, nil
	case "inbox_recent_captures":
		o.calls = append(o.calls, name)
		query, _ := url.ParseQuery(args["query"].(string))
		o.reads = append(o.reads, query.Get("before"))
		window := o.windows[query.Get("before")]
		if window.Items == nil {
			window.Items = []recentCapture{}
		}
		if window.Unfinished == nil {
			window.Unfinished = []unfinishedCapture{}
		}
		raw, _ := json.Marshal(window)
		return raw, nil
	}
	return o.fakeTools.Call(ctx, name, args)
}

// started is how many attempts were started.
func (o *orderedTools) started() int { return len(o.starts) }

// orderClock is a settable time for CaptureOrder.
type orderClock struct{ now time.Time }

func (c *orderClock) Now() time.Time { return c.now }

// coffeeOnItsWay is the coffee capture before "Actually, make it 6": its
// first attempt failed, and it waits for its retry.
func coffeeOnItsWay() unfinishedCapture {
	retry := followUpAt.Add(20 * time.Second)
	return unfinishedCapture{ID: coffeeCapture, Content: "Coffee with Lukas tomorrow at 5 pm.", Source: "web", State: "raw", CreatedAt: followUpAt.Add(-10 * time.Second), NextRetryAt: &retry}
}

// orderRunner plans with a recording planner, waits at most two minutes
// by the test clock, and keeps what it logs.
func orderRunner(t *testing.T, tools *orderedTools, at *orderClock, order bool) (Runner, *recordingPlanner, *[]string) {
	t.Helper()
	planner := &recordingPlanner{output: `{"actions":[{"tool":"task_create","arguments":{"title":"Call Sophie"}}],"confidence":0.9}`}
	var logs []string
	runner := Runner{Planner: planner, Tools: tools, Model: "test", TimeZone: berlin(t), ContextMinutes: 20, Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }}
	if order {
		runner.Order = &CaptureOrder{MaxWait: 2 * time.Minute, Now: at.Now}
	}
	return runner, planner, &logs
}

func run(t *testing.T, runner Runner) []Proposal {
	t.Helper()
	proposals, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return proposals
}

func TestACaptureWaitsForAnEarlierCaptureStillOnItsWay(t *testing.T) {
	at := &orderClock{now: followUpAt}
	tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{{ID: sixItem, Content: "Actually, make it 6.", Source: "web", CreatedAt: followUpAt}},
		windows: map[string]recentWindow{sixItem: {Unfinished: []unfinishedCapture{coffeeOnItsWay()}}}}
	runner, planner, logs := orderRunner(t, tools, at, true)

	proposals := run(t, runner)
	if q, _ := url.ParseQuery(tools.list); q.Get("order") != "oldest" || q.Get("state") != "raw" || q.Get("due") != "true" {
		t.Errorf("poll = %s, want the due raw captures oldest first", tools.list)
	}
	if tools.started() != 0 || len(planner.notes) != 0 || len(tools.records) != 0 {
		t.Fatalf("a capture behind one still on its way was planned: calls = %v", tools.calls)
	}
	if len(proposals) != 1 || !proposals[0].waiting || proposals[0].Reason != waitingReason {
		t.Fatalf("proposals = %+v", proposals)
	}
	// By id and state: what the capture says never reaches the log.
	if len(*logs) != 1 || !strings.Contains((*logs)[0], sixItem+" waits for 1 earlier capture(s) still on their way: "+coffeeCapture+" (raw)") || strings.Contains((*logs)[0], "Coffee") {
		t.Fatalf("logs = %q", *logs)
	}

	// Still within the bound: it waits again, quietly, and reads once per poll.
	at.now = at.now.Add(time.Minute)
	run(t, runner)
	if tools.started() != 0 || len(*logs) != 1 || len(tools.reads) != 2 {
		t.Fatalf("second poll: started %d, logs %q, reads %v", tools.started(), *logs, tools.reads)
	}

	// The coffee is done: the follow-up is planned with what it did, and
	// nothing says it waited.
	tools.windows[sixItem] = recentWindow{Items: []recentCapture{{ID: coffeeCapture, Content: "Coffee with Lukas tomorrow at 5 pm.", Source: "web", State: "resolved", CreatedAt: followUpAt.Add(-10 * time.Second),
		Outcome: &recentOutcome{Status: "processed", Actions: []map[string]any{{"tool": "calendar_create", "result": map[string]any{"id": coffeeEvent, "title": "Coffee with Lukas", "allDay": false, "startAt": "2026-10-17T17:00:00+02:00", "endAt": "2026-10-17T18:00:00+02:00", "status": "active"}}}}}}}
	run(t, runner)
	if tools.started() != 1 || len(planner.notes) != 1 || !strings.Contains(planner.notes[0], coffeeEvent) || strings.Contains(planner.notes[0], "still on its way, nothing done yet") {
		t.Fatalf("after the coffee: started %d, notes %q", tools.started(), planner.notes)
	}
	if processed := tools.records[len(tools.records)-1]; processed["status"] != "processed" || processed["errorSummary"].(*string) != nil {
		t.Fatalf("record = %v", processed)
	}
	if len(runner.Order.since) != 0 {
		t.Errorf("the planned capture is still remembered: %v", runner.Order.since)
	}
}

func TestACaptureIsPlannedAnywayAfterItsWait(t *testing.T) {
	at := &orderClock{now: followUpAt}
	tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{{ID: sixItem, Content: "Actually, make it 6.", Source: "web", CreatedAt: followUpAt}},
		windows: map[string]recentWindow{sixItem: {Unfinished: []unfinishedCapture{coffeeOnItsWay()}}}}
	runner, planner, logs := orderRunner(t, tools, at, true)
	run(t, runner)
	at.now = at.now.Add(2*time.Minute + 5*time.Second)
	run(t, runner)

	if tools.started() != 1 || len(planner.notes) != 1 {
		t.Fatalf("not planned after the bound: started %d, calls %v", tools.started(), tools.calls)
	}
	// Codex sees the capture still on its way, and that nothing is done for it.
	if note := planner.notes[0]; !strings.Contains(note, `"text":"Coffee with Lukas tomorrow at 5 pm.","state":"raw","note":"still on its way, nothing done yet"`) {
		t.Fatalf("note = %s", note)
	}
	processed := tools.records[len(tools.records)-1]
	summary, _ := processed["errorSummary"].(*string)
	if processed["status"] != "processed" || summary == nil || !strings.HasPrefix(*summary, "planned without waiting longer: 1 earlier capture still on the way after 2m5s: 17:09:50 “Coffee with Lukas tomorrow at 5 pm.” (raw, retry at 17:10:20)") {
		t.Fatalf("record = %v (%v)", processed, summary)
	}
	if len(*logs) != 2 || !strings.HasPrefix((*logs)[1], "capture "+sixItem+" planned without waiting longer") || strings.Contains(strings.Join(*logs, "\n"), "Coffee") {
		t.Fatalf("logs = %q", *logs)
	}
}

func TestLaterCapturesOfTheWindowWaitWithoutARead(t *testing.T) {
	at := &orderClock{now: followUpAt}
	approvedItem := inboxItem{ID: "c0000000-0000-4000-8000-000000000014", Content: "Call Anna", Source: "web", CreatedAt: followUpAt.Add(2 * time.Minute),
		ProcessingMetadata: map[string]any{"approvedAction": map[string]any{"tool": "task_complete", "arguments": map[string]any{"taskQuery": "Call insurer"}}}}
	tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{
		// Inbox lists them oldest first; the processor sorts them anyway.
		{ID: farItem, Content: "Pick up the dry cleaning", Source: "web", CreatedAt: followUpAt.Add(30 * time.Minute)},
		{ID: laterItem, Content: "Make it tomorrow morning.", Source: "web", CreatedAt: followUpAt.Add(time.Minute)},
		approvedItem,
		{ID: sixItem, Content: "Actually, make it 6.", Source: "web", CreatedAt: followUpAt},
	}, windows: map[string]recentWindow{sixItem: {Unfinished: []unfinishedCapture{coffeeOnItsWay()}}}}
	runner, planner, logs := orderRunner(t, tools, at, true)
	proposals := run(t, runner)

	// The follow-up waits after its read, the capture a minute later waits
	// behind it without one; the approved action runs (no Codex), and the
	// capture 30 minutes later, outside the window, is planned.
	if got := strings.Join(tools.reads, ","); got != sixItem+","+approvedItem.ID+","+farItem {
		t.Fatalf("reads = %s", got)
	}
	waiting := map[string]bool{}
	for _, proposal := range proposals {
		waiting[proposal.ItemID] = proposal.waiting
	}
	if !waiting[sixItem] || !waiting[laterItem] || waiting[farItem] || waiting[approvedItem.ID] {
		t.Fatalf("waiting = %v", waiting)
	}
	if tools.started() != 2 || len(planner.notes) != 1 || !contains(tools.calls, "task_complete") {
		t.Fatalf("started %d, planned %d, calls %v", tools.started(), len(planner.notes), tools.calls)
	}
	if len(*logs) != 2 || !strings.Contains((*logs)[1], laterItem+" waits for the captures just before it") {
		t.Fatalf("logs = %q", *logs)
	}

	// Once its own wait is over, the later capture is read (and planned)
	// even while the one before it still waits.
	tools.items = []inboxItem{tools.items[1], tools.items[3]}
	tools.reads = nil
	runner.Order.since[sixItem] = at.now.Add(time.Minute)
	at.now = at.now.Add(2*time.Minute + time.Second)
	tools.windows[laterItem] = recentWindow{Unfinished: []unfinishedCapture{{ID: sixItem, Content: "Actually, make it 6.", Source: "web", State: "raw", CreatedAt: followUpAt}}}
	run(t, runner)
	if got := strings.Join(tools.reads, ","); got != sixItem+","+laterItem {
		t.Fatalf("reads = %s, want the later capture read once its wait is over", got)
	}
	if tools.started() != 3 || !strings.Contains(*tools.records[len(tools.records)-1]["errorSummary"].(*string), "“Actually, make it 6.” (raw)") {
		t.Fatalf("started %d, records %v", tools.started(), tools.records)
	}
}

func TestWithoutOrderOrContextNothingWaits(t *testing.T) {
	at := &orderClock{now: followUpAt}
	tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{{ID: sixItem, Content: "Actually, make it 6.", Source: "web", CreatedAt: followUpAt}},
		windows: map[string]recentWindow{sixItem: {Unfinished: []unfinishedCapture{coffeeOnItsWay()}}}}
	runner, planner, logs := orderRunner(t, tools, at, false)
	run(t, runner)
	if tools.started() != 1 || len(planner.notes) != 1 || !strings.Contains(planner.notes[0], "still on its way") || len(*logs) != 0 {
		t.Fatalf("no order: started %d, notes %q, logs %q", tools.started(), planner.notes, *logs)
	}

	// Context off: no read, nothing to wait for.
	tools = &orderedTools{fakeTools: &fakeTools{}, items: tools.items, windows: tools.windows}
	runner, _, _ = orderRunner(t, tools, at, true)
	runner.ContextMinutes = 0
	run(t, runner)
	if len(tools.reads) != 0 || tools.started() != 1 {
		t.Fatalf("context off: reads %v, started %d", tools.reads, tools.started())
	}
}

func TestCaptureOrderForgetsCapturesThatStoppedComingUp(t *testing.T) {
	at := &orderClock{now: followUpAt}
	order := &CaptureOrder{Now: at.Now}
	if decision := order.decide("gone", 1); !decision.wait || !decision.first {
		t.Fatalf("first = %+v", decision)
	}
	at.now = at.now.Add(orderMemory + time.Minute)
	order.decide("other", 0)
	if _, kept := order.since["gone"]; kept {
		t.Fatal("a capture discarded while it waited is remembered forever")
	}
	// The default bound is two minutes.
	order.decide("new", 1)
	at.now = at.now.Add(defaultOrderWait - time.Second)
	if !order.decide("new", 1).wait {
		t.Fatal("stopped waiting before two minutes")
	}
	at.now = at.now.Add(time.Second)
	if decision := order.decide("new", 1); decision.wait || decision.overdue != defaultOrderWait {
		t.Fatalf("after two minutes = %+v", decision)
	}
	var none *CaptureOrder
	if decision := none.decide("x", 3); decision.wait {
		t.Fatal("a nil order waits")
	}
	if _, held := none.hold("x"); held {
		t.Fatal("a nil order holds")
	}
}

// TestARepeatInOtherWordsIsPlannedAndMerges runs "Need oat milk." made
// right after "Buy oat milk." through a fake Codex CLI: not the same text,
// so Codex plans it (the exact repeat never reaches it, repeats.go), and the
// prompt tells it to plan a grocery asked for again as usual, which Food
// merges, and anything else as already done.
func TestARepeatInOtherWordsIsPlannedAndMerges(t *testing.T) {
	const oatItem = "a0000000-0000-4000-8000-0000000000aa"
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt")
	planFile := filepath.Join(dir, "plan")
	codex := fakeCodex(t, `cat "`+planFile+`" > "$out"`)
	wrapper := filepath.Join(dir, "codex-prompt")
	script := "#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%s' \"$last\" > " + promptFile + "\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	first := recentCapture{ID: milkCapture, Content: "Buy oat milk.", Source: "web", State: "resolved", CreatedAt: followUpAt.Add(-time.Minute), Outcome: &recentOutcome{Status: "processed", Actions: []map[string]any{{
		"tool": "food_add_shopping_items", "result": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": oatItem, "name": "oat milk"}, "merged": false}}},
	}}}}
	for name, tc := range map[string]struct {
		plan   string
		status string
	}{
		"grocery": {`{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"oat milk"}]}}],"ambiguous":false,"confidence":0.95}`, "processed"},
		"task":    {`{"actions":[],"ambiguous":true,"confidence":0.9,"reason":"Already done for the capture from 17:09: Remind me to call Sophie."}`, "human_review"},
	} {
		if err := os.WriteFile(planFile, []byte(tc.plan), 0o644); err != nil {
			t.Fatal(err)
		}
		tools := &orderedTools{fakeTools: &fakeTools{}, items: []inboxItem{{ID: followUpItem, Content: "Need oat milk.", Source: "web", CreatedAt: followUpAt}},
			windows: map[string]recentWindow{followUpItem: {Items: []recentCapture{first}}}}
		merging := &mergingTools{orderedTools: tools, oatItem: oatItem}
		cli := CodexCLI{Command: wrapper, WorkDir: dir, TimeZone: berlin(t), Now: func() time.Time { return followUpAt }}
		runner := Runner{Planner: cli, Tools: merging, Model: "test", TimeZone: berlin(t), ContextMinutes: 20, Order: &CaptureOrder{Now: func() time.Time { return followUpAt }}}
		if _, err := runner.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		prompt, _ := os.ReadFile(promptFile)
		for _, want := range []string{`"text":"Buy oat milk.","state":"resolved","done":[{"entity":"shopping_item","id":"` + oatItem + `","change":"added","summary":"oat milk"}]`,
			`When this item asks again, in other words, for what a recent capture asked for ("lemons please" after "buy lemons")`, "food_add_shopping_items merges them into the items already on the list", `reason "Already done for the capture from HH:MM: <its text>"`} {
			if !strings.Contains(string(prompt), want) {
				t.Errorf("%s: prompt lacks %s", name, want)
			}
		}
		last := tools.records[len(tools.records)-1]
		if last["status"] != tc.status {
			t.Fatalf("%s: record = %v, calls = %v", name, last, tools.calls)
		}
		if tc.status == "human_review" {
			if contains(tools.calls, "food_add_shopping_items") || contains(tools.calls, "task_create") || merging.review["errorSummary"] != "Already done for the capture from 17:09: Remind me to call Sophie." {
				t.Fatalf("%s: calls = %v, review = %v", name, tools.calls, merging.review)
			}
			continue
		}
		actions := last["actions"].([]map[string]any)
		result, _ := json.Marshal(actions[0]["result"])
		if len(actions) != 1 || actions[0]["tool"] != "food_add_shopping_items" || !strings.Contains(string(result), `"merged":true`) || actions[0]["followsUp"] != nil || !contains(tools.calls, "inbox_resolve") {
			t.Fatalf("%s: actions = %v (result %s), calls = %v", name, actions, result, tools.calls)
		}
	}
}

// mergingTools answers a list add of oat milk as Food does when it is on
// the list already: merged into that item.
type mergingTools struct {
	*orderedTools
	oatItem string
	review  map[string]any
}

func (m *mergingTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	switch name {
	case "food_add_shopping_items":
		m.calls = append(m.calls, name)
		return json.RawMessage(`{"items":[{"item":{"id":"` + m.oatItem + `","name":"oat milk","state":"needed"},"merged":true}]}`), nil
	case "inbox_set_human_review":
		m.review = args
	}
	return m.orderedTools.Call(ctx, name, args)
}
