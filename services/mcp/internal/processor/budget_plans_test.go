package processor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// More of the owner's categories in these tests.
const (
	groceriesID = "c0000000-0000-4000-8000-000000000006"
	rentID      = "c0000000-0000-4000-8000-000000000007"
	planID      = "b0000000-0000-4000-8000-000000000001"
)

// planningFixture is made-up planning data for November 2026: eating out
// grew, with a kiosk whose name carries a card number; groceries and rent
// are steady (rent paid to a name with an IBAN); shopping has no history.
const planningFixture = `{
  "month": "2026-11", "currency": "EUR", "months": ["2026-07", "2026-08", "2026-09"], "firstMonthPartial": true,
  "fullMonths": 2, "rough": false, "medianMonths": ["2026-07", "2026-08", "2026-09"], "merchantMonths": 3,
  "incomeMinor": [0, 200000, 210000], "incomeMedianMinor": 200000, "spendingMinor": [17000, 114000, 131500],
  "uncategorizedMinor": [0, 0, 2500],
  "categories": [
    {"categoryId": "` + eatingOutID + `", "name": "Eating out", "monthlyMinor": [5000, 6000, 16000], "medianMinor": 6000, "lastMonthMinor": 16000,
     "trend": "up", "trendPercent": 167, "plannedMinor": 0, "merchants": [
       {"name": "Pizzeria Roma", "count": 2, "totalMinor": 14000, "monthlyMinor": 4667},
       {"name": "Kiosk 4111 1111 1111 1111", "count": 5, "totalMinor": 1500, "monthlyMinor": 500}]},
    {"categoryId": "` + groceriesID + `", "name": "Groceries", "monthlyMinor": [12000, 28000, 33000], "medianMinor": 28000, "lastMonthMinor": 33000,
     "trend": "flat", "trendPercent": 18, "plannedMinor": 35000, "merchants": [{"name": "REWE Markt", "count": 33, "totalMinor": 70000, "monthlyMinor": 23333}]},
    {"categoryId": "` + rentID + `", "name": "Rent", "monthlyMinor": [0, 80000, 80000], "medianMinor": 80000, "lastMonthMinor": 80000,
     "trend": "flat", "trendPercent": 0, "plannedMinor": 80000, "merchants": [{"name": "Hausverwaltung DE89 3704 0044 0532 0130 00", "count": 2, "totalMinor": 160000, "monthlyMinor": 53333}]},
    {"categoryId": "` + shoppingID + `", "name": "Shopping", "monthlyMinor": [0, 0, 0], "medianMinor": null, "lastMonthMinor": 0,
     "trend": "flat", "trendPercent": 0, "plannedMinor": 0, "merchants": []}
  ],
  "plan": {"source": "regular", "expectedIncomeMinor": 200000, "savingsGoalMinor": 10000, "plannedMinor": 115000},
  "subscriptions": [{"name": "Netflix", "monthlyMinor": 1399, "categoryId": null}],
  "balanceMinor": 10570
}`

// validPlanAnswer is a plan that passes every check for planningFixture
// (categories by the prompt's refs, merchants by the prompt's names).
const validPlanAnswer = `{"currency": "EUR", "headline": "Money is tight: eat out less and keep groceries steady.",
  "lines": [
    {"category": "c1", "proposedMinor": 9000, "reason": "Eating out grew to 160 EUR; bring it back down."},
    {"category": "c2", "proposedMinor": 30000, "reason": "Close to your usual groceries."},
    {"category": "c3", "proposedMinor": 80000, "reason": "Rent stays the same."}],
  "suggestions": [
    {"title": "Cook instead of pizza", "detail": "Pizzeria Roma is your largest eating out spend: cook twice a week.", "category": "c1",
     "merchants": ["pizzeria  roma"], "monthlySavingMinor": 3000, "link": "food_recipes"},
    {"title": "Skip the kiosk", "detail": "Five small kiosk buys a month add up.", "category": "c1", "merchants": ["Kiosk [card]"],
     "monthlySavingMinor": 400, "link": null}],
  "projectedTotalMinor": 119050, "incomeEstimateMinor": 205000, "projectedSavingsMinor": 85950}`

// budgetTools is a fake MCP for the AI budget plan job: Finance's plans
// (the waiting count and the claim), the planning data, Food, and the
// result and failure writes.
type budgetTools struct {
	waiting  int
	plan     string
	cooks    bool
	fail     string
	finished string
	calls    []string
	actors   []activity.Actor
	results  []map[string]any
	failures []map[string]any
	requests []map[string]any
	// failCtx is the context's error when a failure was handed back.
	failCtx []error
}

func newBudgetTools(attempts int) *budgetTools {
	plan, _ := json.Marshal(map[string]any{"id": planID, "month": "2026-11", "currency": "EUR", "trigger": "request", "attempts": attempts,
		"savingsGoal": map[string]any{"amount": 50000, "currency": "EUR"}})
	return &budgetTools{waiting: 1, plan: string(plan), cooks: true}
}

func (f *budgetTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	f.calls = append(f.calls, name)
	f.actors = append(f.actors, activity.TraceFrom(ctx).Actor)
	if name == f.fail {
		return nil, errors.New("MCP tool " + name + " error: unavailable")
	}
	switch name {
	case "finance_list_budget_plans":
		return json.RawMessage(`{"items": [], "waiting": ` + string(rune('0'+f.waiting)) + `}`), nil
	case "finance_claim_budget_plan":
		if f.waiting == 0 {
			return json.RawMessage(`{"plan": null}`), nil
		}
		f.waiting--
		return json.RawMessage(`{"plan": ` + f.plan + `}`), nil
	case "finance_get_budget_planning_data":
		return json.RawMessage(planningFixture), nil
	case "food_list_pantry_items":
		if f.cooks {
			return json.RawMessage(`{"items": [{"name": "Reis"}, {"name": "Hähnchenbrust"}]}`), nil
		}
		return json.RawMessage(`{"items": []}`), nil
	case "food_list_recipes", "food_list_ideas":
		return json.RawMessage(`{"items": []}`), nil
	case "finance_set_budget_plan_result":
		f.results = append(f.results, args)
		if f.finished != "" {
			return nil, errors.New(f.finished)
		}
		return json.RawMessage(`{"id": "` + planID + `", "state": "ready"}`), nil
	case "finance_fail_budget_plan":
		f.failures = append(f.failures, args)
		f.failCtx = append(f.failCtx, ctx.Err())
		return json.RawMessage(`{}`), nil
	case "finance_request_budget_plan":
		f.requests = append(f.requests, args)
		return json.RawMessage(`{"id": "` + planID + `"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// fakeBudgetPlanner answers with the given JSON and records what it was
// asked.
type fakeBudgetPlanner struct {
	answer  string
	err     error
	during  func()
	prompts []string
	schemas []string
}

func (f *fakeBudgetPlanner) BudgetPlan(_ context.Context, prompt, schema string) (string, error) {
	f.prompts, f.schemas = append(f.prompts, prompt), append(f.schemas, schema)
	if f.during != nil {
		f.during()
	}
	return f.answer, f.err
}

func newBudgetJob(tools *budgetTools, planner *fakeBudgetPlanner, now *time.Time) *BudgetPlanJob {
	return &BudgetPlanJob{Planner: planner, Tools: tools, Enabled: true, Monthly: true, TimeZone: ownerZone, Now: func() time.Time { return *now },
		Retry: RetryPolicy{Now: func() time.Time { return *now }, Jitter: func() float64 { return 0.5 }}}
}

func TestABudgetPlanIsClaimedPlannedCheckedAndLeft(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(1)
	planner := &fakeBudgetPlanner{answer: "```json\n" + validPlanAnswer + "\n```"}
	job := newBudgetJob(tools, planner, &now)
	job.Tick(context.Background())

	if len(planner.prompts) != 1 || planner.schemas[0] != budgetPlanSchema || len(tools.results) != 1 || len(tools.failures) != 0 {
		t.Fatalf("calls %v, %d prompts, %d results, failures %v", tools.calls, len(planner.prompts), len(tools.results), tools.failures)
	}
	result := tools.results[0]
	raw, _ := json.Marshal(result)
	got := string(raw)
	// Finance's ids and names come back; the exact totals replace the
	// rounded ones; shopping, without history, is left to Finance.
	for _, want := range []string{`"planId":"` + planID + `"`, `"categoryId":"` + eatingOutID + `"`, `"categoryId":"` + groceriesID + `"`, `"categoryId":"` + rentID + `"`,
		`"merchants":["Pizzeria Roma"]`, `"merchants":["Kiosk 4111 1111 1111 1111"]`, `"link":"food_recipes"`, `"projectedTotalMinor":119000`,
		`"projectedSavingsMinor":86000`, `"incomeEstimateMinor":205000`, `"currency":"EUR"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the result lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, shoppingID) {
		t.Errorf("shopping was proposed: %s", got)
	}
	// The prompt holds aggregates, never IBANs or card numbers, and asks
	// for what the schema wants.
	prompt := planner.prompts[0]
	for _, secret := range []string{"4111", "3704", "DE89", "0532 0130"} {
		if strings.Contains(prompt, secret) {
			t.Errorf("the prompt holds %q", secret)
		}
	}
	for _, kept := range []string{`"ref":"c1"`, `"name":"Eating out"`, `"name":"Kiosk [card]"`, `"name":"Hausverwaltung [IBAN]"`, `"savingsGoalPerMonthMinor":50000`,
		`"balanceNowMinor":10570`, `"pantryItemsInStock":2`, `"perMonthMinor":4667`, `"medianMinor":6000`, `"trend":"up"`, "food_recipes", "minor units (cents)"} {
		if !strings.Contains(prompt, kept) {
			t.Errorf("the prompt lacks %s", kept)
		}
	}
	status := job.Status()
	run := status.LastRun
	if run == nil || run.Outcome != "ready" || run.Month != "2026-11" || run.Attempt != 1 || run.Categories != 3 || run.Suggestions != 2 || *status.Waiting != 1 {
		t.Fatalf("status = %+v run %+v", status, run)
	}
	for _, actor := range tools.actors {
		if actor != activity.Agent("codex-budget-plan") {
			t.Fatalf("a call by %+v", actor)
		}
	}
	// Nothing waits any more: the next check asks no one.
	job.Tick(context.Background())
	if len(planner.prompts) != 1 || *job.Status().Waiting != 0 {
		t.Fatalf("an empty check planned: %v", tools.calls)
	}
}

func TestALinkToRecipesNeedsFoodToCookWith(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(1)
	tools.cooks = false
	job := newBudgetJob(tools, &fakeBudgetPlanner{answer: validPlanAnswer}, &now)
	job.Tick(context.Background())
	raw, _ := json.Marshal(tools.results)
	if len(tools.results) != 1 || strings.Contains(string(raw), "food_recipes") {
		t.Fatalf("results = %s", raw)
	}
}

func TestBadPlansAreRetriedAndNeverLeft(t *testing.T) {
	for name, change := range map[string]func(string) string{
		"an unknown category": func(a string) string { return strings.Replace(a, `"category": "c3"`, `"category": "c9"`, 1) },
		"a negative amount":   func(a string) string { return strings.Replace(a, `"proposedMinor": 9000`, `"proposedMinor": -100`, 1) },
		"a saving above spending": func(a string) string {
			return strings.Replace(a, `"monthlySavingMinor": 400`, `"monthlySavingMinor": 600`, 1)
		},
		"a saving of nothing": func(a string) string {
			return strings.Replace(a, `"monthlySavingMinor": 400`, `"monthlySavingMinor": 0`, 1)
		},
		"an unlisted merchant": func(a string) string { return strings.Replace(a, `["Kiosk [card]"]`, `["Burger Palace"]`, 1) },
		"another category's":   func(a string) string { return strings.Replace(a, `["Kiosk [card]"]`, `["REWE Markt"]`, 1) },
		"a missing category": func(a string) string {
			return strings.Replace(a, `{"category": "c3", "proposedMinor": 80000, "reason": "Rent stays the same."}`, `{"category": "c1", "proposedMinor": 1, "reason": "twice"}`, 1)
		},
		"totals that do not add": func(a string) string {
			return strings.Replace(a, `"projectedTotalMinor": 119050`, `"projectedTotalMinor": 125000`, 1)
		},
		"savings that do not add": func(a string) string {
			return strings.Replace(a, `"projectedSavingsMinor": 85950`, `"projectedSavingsMinor": 50000`, 1)
		},
		"a negative income": func(a string) string {
			return strings.Replace(a, `"incomeEstimateMinor": 205000`, `"incomeEstimateMinor": -1`, 1)
		},
		"another currency": func(a string) string { return strings.Replace(a, `"currency": "EUR"`, `"currency": "USD"`, 1) },
		"no headline": func(a string) string {
			return strings.Replace(a, "Money is tight: eat out less and keep groceries steady.", "Plan", 1)
		},
		"not JSON": func(string) string { return "You should spend less on pizza." },
		"one way to spend less": func(a string) string {
			return a[:strings.Index(a, `,
    {"title": "Skip the kiosk"`)] + `], "projectedTotalMinor": 119000, "incomeEstimateMinor": 205000, "projectedSavingsMinor": 86000}`
		},
	} {
		now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
		tools := newBudgetTools(1)
		job := newBudgetJob(tools, &fakeBudgetPlanner{answer: change(validPlanAnswer)}, &now)
		job.Tick(context.Background())
		run := job.Status().LastRun
		if len(tools.results) != 0 || len(tools.failures) != 1 || tools.failures[0]["retryAt"] == nil || run.Outcome != "retry" || run.Error == "" {
			t.Errorf("%s: results %v failures %v run %+v", name, tools.results, tools.failures, run)
		}
	}
	// The fifth unusable answer fails the plan for good.
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(maxModelOutputAttempts)
	job := newBudgetJob(tools, &fakeBudgetPlanner{answer: "no"}, &now)
	job.Tick(context.Background())
	if len(tools.failures) != 1 || tools.failures[0]["retryAt"] != nil || !strings.Contains(tools.failures[0]["error"].(string), "gave up after 5 runs") ||
		job.Status().LastRun.Outcome != "failed" {
		t.Fatalf("failures %v run %+v", tools.failures, job.Status().LastRun)
	}
}

func TestCodexFailuresWaitForTheUsageResetOrBackOff(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	reset := now.Add(3 * time.Hour)
	tools := newBudgetTools(2)
	job := newBudgetJob(tools, &fakeBudgetPlanner{err: errors.New("ERROR: You've hit your usage limit. Try again at 12:00.")}, &now)
	job.Retry.RecoveryAt = func(context.Context) (time.Time, bool) { return reset, true }
	job.Tick(context.Background())
	if len(tools.failures) != 1 || tools.failures[0]["retryAt"] != reset.Add(30*time.Second).UTC().Format(time.RFC3339) {
		t.Fatalf("failures = %v", tools.failures)
	}
	// Another failure backs off: 30 s doubled once for the second run.
	tools = newBudgetTools(2)
	job = newBudgetJob(tools, &fakeBudgetPlanner{err: errors.New("run Codex CLI: exit status 1: stream disconnected")}, &now)
	job.Tick(context.Background())
	if len(tools.failures) != 1 || tools.failures[0]["retryAt"] != now.Add(time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("failures = %v", tools.failures)
	}
	// After eight runs the plan fails for good.
	tools = newBudgetTools(budgetPlanMaxRuns)
	job = newBudgetJob(tools, &fakeBudgetPlanner{err: errors.New("run Codex CLI: exit status 1")}, &now)
	job.Tick(context.Background())
	if len(tools.failures) != 1 || tools.failures[0]["retryAt"] != nil || job.Status().LastRun.Outcome != "failed" {
		t.Fatalf("failures = %v", tools.failures)
	}
	// Finance unreachable: no plan is claimed, and the check backs off.
	tools = newBudgetTools(1)
	tools.fail = "finance_list_budget_plans"
	job = newBudgetJob(tools, &fakeBudgetPlanner{answer: validPlanAnswer}, &now)
	job.Tick(context.Background())
	if contains(tools.calls, "finance_claim_budget_plan") || !job.Status().NextCheckAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("calls %v status %+v", tools.calls, job.Status())
	}
}

func TestAPlanDismissedMeanwhileIsDropped(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(1)
	tools.finished = `MCP tool finance_set_budget_plan_result rejected: service returned 409: {"error":{"code":"plan_not_running","message":"The plan is dismissed"}}`
	job := newBudgetJob(tools, &fakeBudgetPlanner{answer: validPlanAnswer}, &now)
	job.Tick(context.Background())
	if run := job.Status().LastRun; run.Outcome != "dropped" || len(tools.failures) != 0 {
		t.Fatalf("run %+v failures %v", run, tools.failures)
	}
}

func TestAShutdownHandsThePlanBack(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(1)
	ctx, cancel := context.WithCancel(context.Background())
	planner := &fakeBudgetPlanner{err: errors.New("Codex run cancelled: context canceled"), during: cancel}
	job := newBudgetJob(tools, planner, &now)
	job.Tick(ctx)
	if len(tools.failures) != 1 || tools.failures[0]["retryAt"] != now.UTC().Format(time.RFC3339) || tools.failCtx[0] != nil {
		t.Fatalf("failures %v (context %v)", tools.failures, tools.failCtx)
	}
}

func TestOneBudgetPlanRunsAtATime(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(1)
	planner := &fakeBudgetPlanner{answer: validPlanAnswer}
	job := newBudgetJob(tools, planner, &now)
	job.running = true
	job.Tick(context.Background())
	if len(tools.calls) != 0 {
		t.Fatalf("a running job checked again: %v", tools.calls)
	}
	// Finance hands out nothing while another plan runs.
	job.running = false
	tools.plan = "null"
	job.Tick(context.Background())
	if len(planner.prompts) != 0 || !contains(tools.calls, "finance_claim_budget_plan") {
		t.Fatalf("calls %v", tools.calls)
	}
}

func TestTheMonthlyPlanIsAskedForOnTheFirst(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 59, 0, 0, ownerZone)
	tools := newBudgetTools(0)
	tools.waiting = 0
	job := newBudgetJob(tools, &fakeBudgetPlanner{}, &now)
	job.Tick(context.Background())
	if len(tools.requests) != 0 || !job.Status().NextMonthlyAt.Equal(time.Date(2026, 11, 1, 6, 0, 0, 0, ownerZone)) {
		t.Fatalf("before 06:00: %v, next %v", tools.requests, job.Status().NextMonthlyAt)
	}
	now = time.Date(2026, 11, 1, 6, 0, 0, 0, ownerZone)
	job.Tick(context.Background())
	job.Tick(context.Background())
	if len(tools.requests) != 1 || tools.requests[0]["month"] != "2026-11" ||
		!job.Status().NextMonthlyAt.Equal(time.Date(2026, 12, 1, 6, 0, 0, 0, ownerZone)) {
		t.Fatalf("requests %v, next %v", tools.requests, job.Status().NextMonthlyAt)
	}
	// A processor that was down asks until the 3rd (Finance keeps one
	// scheduled plan a month), and not after.
	now = time.Date(2026, 11, 3, 23, 0, 0, 0, ownerZone)
	newBudgetJob(tools, &fakeBudgetPlanner{}, &now).Tick(context.Background())
	now = time.Date(2026, 11, 4, 0, 30, 0, 0, ownerZone)
	newBudgetJob(tools, &fakeBudgetPlanner{}, &now).Tick(context.Background())
	if len(tools.requests) != 2 {
		t.Fatalf("requests = %v", tools.requests)
	}
	// A failed request is tried again five minutes later.
	now = time.Date(2026, 12, 1, 6, 0, 0, 0, ownerZone)
	tools.fail = "finance_request_budget_plan"
	job.Tick(context.Background())
	tools.fail = ""
	now = now.Add(time.Minute)
	job.Tick(context.Background())
	now = now.Add(budgetPlanMonthlyRetry)
	job.Tick(context.Background())
	if len(tools.requests) != 3 || tools.requests[2]["month"] != "2026-12" {
		t.Fatalf("requests = %v", tools.requests)
	}
	// Switched off, there is no monthly plan.
	now = time.Date(2027, 1, 1, 7, 0, 0, 0, ownerZone)
	job.Monthly = false
	job.Tick(context.Background())
	if len(tools.requests) != 3 || job.Status().NextMonthlyAt != nil {
		t.Fatalf("monthly off: %v", tools.requests)
	}
}

func TestBudgetPlansSwitchedOffDoNothing(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, ownerZone)
	tools := newBudgetTools(1)
	job := newBudgetJob(tools, &fakeBudgetPlanner{answer: validPlanAnswer}, &now)
	job.Enabled = false
	var published []BudgetPlansStatus
	job.OnStatus = func(status BudgetPlansStatus) { published = append(published, status) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job.Loop(ctx)
	job.Tick(context.Background())
	if len(tools.calls) != 0 || len(published) != 1 || published[0].Enabled || published[0].NextCheckAt != nil || published[0].NextMonthlyAt != nil {
		t.Fatalf("switched off: calls %v, published %+v", tools.calls, published)
	}

	for _, name := range []string{"LIFE_OS_AI_BUDGET_PLANS", "LIFE_OS_AI_BUDGET_PLAN_MONTHLY"} {
		t.Setenv(name, "")
	}
	if cfg, err := LoadConfig(); err != nil || !cfg.BudgetPlansEnabled || !cfg.BudgetPlanMonthly {
		t.Fatalf("defaults: %+v %+v %v", cfg.BudgetPlansEnabled, cfg.BudgetPlanMonthly, err)
	}
	t.Setenv("LIFE_OS_AI_BUDGET_PLAN_MONTHLY", "off")
	if cfg, err := LoadConfig(); err != nil || !cfg.BudgetPlansEnabled || cfg.BudgetPlanMonthly {
		t.Fatalf("monthly off: %+v %v", cfg.BudgetPlanMonthly, err)
	}
	t.Setenv("LIFE_OS_AI_BUDGET_PLANS", "off")
	if cfg, err := LoadConfig(); err != nil || cfg.BudgetPlansEnabled {
		t.Fatalf("off: %+v %v", cfg.BudgetPlansEnabled, err)
	}
	t.Setenv("LIFE_OS_AI_BUDGET_PLANS", "monthly")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "LIFE_OS_AI_BUDGET_PLANS") {
		t.Fatalf("an unknown switch: %v", err)
	}
}

func TestTheMonitorShowsTheBudgetPlanJob(t *testing.T) {
	monitor := NewMonitor("gpt-5.6-luna")
	planner := monitor.MonitoredBudgetPlans(&fakeBudgetPlanner{err: errors.New("ERROR: You've hit your usage limit")})
	if _, err := planner.BudgetPlan(context.Background(), "prompt", budgetPlanSchema); err == nil {
		t.Fatal("the failure was lost")
	}
	waiting := 2
	monitor.SetBudgetPlans(BudgetPlansStatus{Enabled: true, Monthly: true, Waiting: &waiting})
	status := monitor.Snapshot()
	if status.Codex.State != CodexUsageLimited || status.BudgetPlans == nil || *status.BudgetPlans.Waiting != 2 {
		t.Fatalf("status = %+v", status)
	}
}

func TestBudgetPlansRunWithoutWebSearchToTheSchema(t *testing.T) {
	dir := t.TempDir()
	argsFile, schemaCopy := filepath.Join(dir, "args"), filepath.Join(dir, "schema")
	codex := fakeCodex(t, `printf '{"lines":[]}' > "$out"`)
	wrapper := filepath.Join(dir, "codex-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprev=''\nfor arg in \"$@\"; do\n  if [ \"$prev\" = '--output-schema' ]; then cp \"$arg\" " + schemaCopy + "; fi\n  prev=$arg\ndone\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := CodexCLI{Command: wrapper, WorkDir: dir, Model: "gpt-5.6-luna"}
	if out, err := cli.BudgetPlan(context.Background(), "plan", budgetPlanSchema); err != nil || out != `{"lines":[]}` {
		t.Fatalf("plan = %q, %v", out, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "web_search=\"disabled\"\n") || !strings.Contains(string(args), "--output-schema\n") {
		t.Fatalf("args = %q", args)
	}
	if schema, _ := os.ReadFile(schemaCopy); string(schema) != budgetPlanSchema {
		t.Fatalf("schema given = %q", schema)
	}
}

func TestCapturesCannotUseTheBudgetPlanTools(t *testing.T) {
	for _, tool := range []string{"finance_list_budget_plans", "finance_get_budget_planning_data", "finance_request_budget_plan", "finance_claim_budget_plan",
		"finance_set_budget_plan_result", "finance_fail_budget_plan"} {
		if allowed(tool) || validAction(plannedAction{Tool: tool, Arguments: map[string]any{"planId": planID}}) {
			t.Errorf("a capture may plan %s", tool)
		}
	}
}
