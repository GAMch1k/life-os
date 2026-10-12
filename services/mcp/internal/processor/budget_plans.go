package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// The AI budget plan job (P13-09) runs next to capture processing: the AI
// plans a month's budget and says where to spend less. Finance keeps every
// plan from the owner's request on ("Plan my budget" on the Budget tab),
// and the job asks for one itself on the 1st of each month. Every 30
// seconds the job asks Finance whether a plan waits (a count: cheap),
// claims it, reads the month's planning data through MCP (aggregates only:
// spending per category and month, the top merchants, income,
// subscriptions, the plan that applies) and what Food has (to point to
// cooking), asks Codex for the plan as structured output
// (CodexCLI.BudgetPlan, never with web search), checks it strictly, and
// leaves it with finance_set_budget_plan_result, where Finance checks it
// again and keeps it for the owner to apply or dismiss. It never applies a
// plan.

// budgetPlanActor is who the job's reads and writes are attributed to.
var budgetPlanActor = activity.Agent("codex-budget-plan")

const (
	// budgetPlanTick is how often the job checks whether a plan waits or
	// the monthly plan is due.
	budgetPlanTick = 30 * time.Second
	// budgetPlanMonthlyAt is the minute of the 1st (owner-local) the
	// monthly plan is asked for; a processor that was down then still asks
	// until the budgetPlanMonthlyDays-th.
	budgetPlanMonthlyAt   = 6 * 60
	budgetPlanMonthlyDays = 3
	// budgetPlanMonthlyRetry is how soon a failed monthly request is tried
	// again.
	budgetPlanMonthlyRetry = 5 * time.Minute
	// budgetPlanMaxRuns is how many runs a plan gets when Codex or a
	// service keeps failing before it fails for good; unusable answers get
	// maxModelOutputAttempts.
	budgetPlanMaxRuns = 8
	// budgetPlanReleaseTimeout bounds handing a plan back at shutdown.
	budgetPlanReleaseTimeout = 5 * time.Second
	// Limits of what a merchant or subscription tells Codex.
	maxPromptMerchant = 60
)

// budgetPlanner plans budgets; CodexCLI is the real one.
type budgetPlanner interface {
	BudgetPlan(ctx context.Context, prompt, schema string) (string, error)
}

// BudgetPlanJob runs the AI's budget plans. What waits lives in Finance, so
// a restart loses nothing; the monthly request is idempotent in Finance (one
// scheduled plan a month).
type BudgetPlanJob struct {
	Planner budgetPlanner
	Tools   toolCaller
	// Enabled is LIFE_OS_AI_BUDGET_PLANS; off, the job reads and changes
	// nothing (the owner's requests wait in Finance).
	Enabled bool
	// Monthly is LIFE_OS_AI_BUDGET_PLAN_MONTHLY: the plan on the 1st.
	Monthly bool
	// TimeZone is the owner's, for the 1st and the prompt's today; nil
	// means UTC.
	TimeZone *time.Location
	// Retry schedules a failed run again, as capture processing retries:
	// at the Codex usage reset, else with backoff.
	Retry RetryPolicy
	// Now is the clock; tests replace it.
	Now func() time.Time
	// OnStatus, when set, receives the job's status after every change.
	OnStatus func(BudgetPlansStatus)
	// Logf, when set, logs a line per run.
	Logf func(format string, args ...any)

	mu      sync.Mutex
	running bool
	// nextAt is when the job checks Finance next; failures counts the
	// failed checks in a row (for the backoff).
	nextAt   time.Time
	failures int
	// monthlyDone is the month (YYYY-MM) the monthly plan was asked for;
	// monthlyRetryAt is when a failed request is tried again.
	monthlyDone    string
	monthlyRetryAt time.Time
	waiting        *int
	lastCheckAt    *time.Time
	lastRun        *BudgetPlanRun
}

// BudgetPlansStatus is the job's part of the processor's /status.
type BudgetPlansStatus struct {
	Enabled bool `json:"enabled"`
	Monthly bool `json:"monthly"`
	Running bool `json:"running"`
	// NextCheckAt is when the job asks Finance next (now: at the next
	// tick; later after Finance or MCP failed); null when off.
	NextCheckAt *time.Time `json:"nextCheckAt"`
	// NextMonthlyAt is when the monthly plan is asked for next; null when
	// the job or the monthly plan is off.
	NextMonthlyAt *time.Time `json:"nextMonthlyAt"`
	// Waiting is how many plans waited at the last check; null before it.
	Waiting     *int           `json:"waiting"`
	LastCheckAt *time.Time     `json:"lastCheckAt"`
	LastRun     *BudgetPlanRun `json:"lastRun"`
}

// BudgetPlanRun is what one run did.
type BudgetPlanRun struct {
	PlanID     string    `json:"planId"`
	Month      string    `json:"month"`
	Trigger    string    `json:"trigger"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// Attempt is the plan's run number.
	Attempt int `json:"attempt"`
	// Outcome: ready (the plan waits for the owner), retry (it runs again
	// later), failed (for good), or dropped (dismissed meanwhile).
	Outcome     string `json:"outcome"`
	Categories  int    `json:"categories"`
	Suggestions int    `json:"suggestions"`
	Error       string `json:"error,omitempty"`
}

func (j *BudgetPlanJob) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *BudgetPlanJob) location() *time.Location {
	if j.TimeZone != nil {
		return j.TimeZone
	}
	return time.UTC
}

// Loop checks every budgetPlanTick until ctx ends. Switched off, it only
// reports so. Runs never overlap, and Codex's slots keep a plan from
// running beside a capture beyond LIFE_OS_CODEX_MAX_CONCURRENCY.
func (j *BudgetPlanJob) Loop(ctx context.Context) {
	j.mu.Lock()
	j.publish()
	j.mu.Unlock()
	if !j.Enabled {
		return
	}
	// The first check comes a tick after startup, when MCP is up.
	ticker := time.NewTicker(budgetPlanTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.Tick(ctx)
		}
	}
}

// Tick asks for the monthly plan when it is due, then runs a waiting plan,
// if any.
func (j *BudgetPlanJob) Tick(ctx context.Context) {
	monthly, check, ok := j.take()
	if !ok {
		return
	}
	ctx = activity.WithTrace(ctx, activity.Trace{CorrelationID: "budget-plan-" + runKey(j.now().UTC().Format(time.RFC3339Nano)), Actor: budgetPlanActor})
	if monthly != "" {
		_, err := j.Tools.Call(ctx, "finance_request_budget_plan", map[string]any{"month": monthly})
		j.mu.Lock()
		if err == nil {
			j.monthlyDone = monthly
		} else if ctx.Err() == nil {
			j.monthlyRetryAt = j.now().Add(budgetPlanMonthlyRetry)
		}
		j.mu.Unlock()
		if err != nil && j.Logf != nil {
			j.Logf("AI budget plan: the monthly plan for %s could not be asked for: %v", monthly, err)
		}
	}
	var (
		waiting int
		checked bool
		run     *BudgetPlanRun
		err     error
	)
	if check {
		waiting, checked, run, err = j.check(ctx)
	}
	if run != nil && j.Logf != nil {
		j.Logf("AI budget plan: %s plan for %s (run %d) %s: %d categories, %d ways to spend less%s", run.Trigger, run.Month, run.Attempt, run.Outcome, run.Categories, run.Suggestions, errorSuffix(run.Error))
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	now := j.now()
	if checked {
		at := now.UTC()
		j.lastCheckAt, j.waiting = &at, &waiting
	}
	if run != nil {
		j.lastRun = run
	}
	switch {
	case ctx.Err() != nil:
	case err != nil:
		// Finance or MCP unreachable: back off.
		j.nextAt, _ = j.Retry.next(ctx, failureDependency, j.failures, err.Error())
		j.failures++
	default:
		// Due again at the next tick.
		j.failures, j.nextAt = 0, time.Time{}
	}
	j.publish()
}

// take claims the job for this tick: the month whose monthly plan is due
// (or ""), and whether Finance is due a check.
func (j *BudgetPlanJob) take() (monthly string, check bool, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.Enabled || j.running {
		return "", false, false
	}
	now := j.now()
	if j.monthlyDue(now) {
		monthly = now.In(j.location()).Format("2006-01")
	}
	check = !now.Before(j.nextAt)
	if monthly == "" && !check {
		return "", false, false
	}
	j.running = true
	j.publish()
	return monthly, check, true
}

// monthlyDue reports whether the monthly plan is due at now: from 06:00 on
// the 1st to the end of the 3rd, once a month.
func (j *BudgetPlanJob) monthlyDue(now time.Time) bool {
	if !j.Monthly {
		return false
	}
	local := now.In(j.location())
	if local.Format("2006-01") == j.monthlyDone || local.Day() > budgetPlanMonthlyDays || now.Before(j.monthlyRetryAt) {
		return false
	}
	return local.Day() > 1 || local.Hour()*60+local.Minute() >= budgetPlanMonthlyAt
}

// Status is the job's status now.
func (j *BudgetPlanJob) Status() BudgetPlansStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status()
}

func (j *BudgetPlanJob) status() BudgetPlansStatus {
	status := BudgetPlansStatus{Enabled: j.Enabled, Monthly: j.Monthly, Running: j.running, Waiting: j.waiting, LastCheckAt: j.lastCheckAt, LastRun: j.lastRun}
	if !j.Enabled {
		return status
	}
	now := j.now()
	next := j.nextAt
	if next.Before(now) {
		next = now
	}
	status.NextCheckAt = &next
	if j.Monthly {
		local := now.In(j.location())
		first := time.Date(local.Year(), local.Month(), 1, budgetPlanMonthlyAt/60, budgetPlanMonthlyAt%60, 0, 0, j.location())
		switch {
		case local.Format("2006-01") == j.monthlyDone || local.Day() > budgetPlanMonthlyDays:
			first = first.AddDate(0, 1, 0)
		case now.After(first):
			first = now
			if first.Before(j.monthlyRetryAt) {
				first = j.monthlyRetryAt
			}
		}
		status.NextMonthlyAt = &first
	}
	return status
}

func (j *BudgetPlanJob) publish() {
	if j.OnStatus != nil {
		j.OnStatus(j.status())
	}
}

// Finance's answers, as far as the job reads them.
type (
	budgetPlan struct {
		ID          string `json:"id"`
		Month       string `json:"month"`
		Currency    string `json:"currency"`
		Trigger     string `json:"trigger"`
		Attempts    int    `json:"attempts"`
		SavingsGoal *struct {
			Amount int64 `json:"amount"`
		} `json:"savingsGoal"`
	}
	planningData struct {
		Month              string             `json:"month"`
		Currency           string             `json:"currency"`
		Months             []string           `json:"months"`
		FirstMonthPartial  bool               `json:"firstMonthPartial"`
		FullMonths         int                `json:"fullMonths"`
		Rough              bool               `json:"rough"`
		MedianMonths       []string           `json:"medianMonths"`
		MerchantMonths     int                `json:"merchantMonths"`
		IncomeMinor        []int64            `json:"incomeMinor"`
		IncomeMedianMinor  *int64             `json:"incomeMedianMinor"`
		SpendingMinor      []int64            `json:"spendingMinor"`
		UncategorizedMinor []int64            `json:"uncategorizedMinor"`
		Categories         []planningCategory `json:"categories"`
		Plan               *struct {
			ExpectedIncomeMinor int64 `json:"expectedIncomeMinor"`
			SavingsGoalMinor    int64 `json:"savingsGoalMinor"`
			PlannedMinor        int64 `json:"plannedMinor"`
		} `json:"plan"`
		Subscriptions []struct {
			Name         string  `json:"name"`
			MonthlyMinor int64   `json:"monthlyMinor"`
			CategoryID   *string `json:"categoryId"`
		} `json:"subscriptions"`
		BalanceMinor int64 `json:"balanceMinor"`
	}
	planningCategory struct {
		ID             string             `json:"categoryId"`
		Name           string             `json:"name"`
		MonthlyMinor   []int64            `json:"monthlyMinor"`
		MedianMinor    *int64             `json:"medianMinor"`
		LastMonthMinor *int64             `json:"lastMonthMinor"`
		Trend          *string            `json:"trend"`
		TrendPercent   *int               `json:"trendPercent"`
		PlannedMinor   int64              `json:"plannedMinor"`
		Merchants      []planningMerchant `json:"merchants"`
	}
	planningMerchant struct {
		Name         string `json:"name"`
		Count        int    `json:"count"`
		TotalMinor   int64  `json:"totalMinor"`
		MonthlyMinor int64  `json:"monthlyMinor"`
	}
)

// history reports whether the category had any spending in the window.
func (c planningCategory) history() bool {
	return slices.ContainsFunc(c.MonthlyMinor, func(amount int64) bool { return amount > 0 })
}

// baseline is what the category typically spends in a month: the larger
// of its median and last month (Finance's rule too).
func (c planningCategory) baseline() int64 {
	var baseline int64
	if c.MedianMinor != nil {
		baseline = *c.MedianMinor
	}
	if c.LastMonthMinor != nil && *c.LastMonthMinor > baseline {
		baseline = *c.LastMonthMinor
	}
	return baseline
}

// kitchen is what Food has that cooking more could use; nil when Food
// could not say.
type kitchen struct {
	PantryInStock int      `json:"pantryItemsInStock"`
	PantryItems   []string `json:"pantryItems"`
	SavedRecipes  int      `json:"savedRecipes"`
	NewIdeas      []string `json:"newRecipeIdeas"`
}

func (k *kitchen) hasFood() bool {
	return k != nil && (k.PantryInStock > 0 || k.SavedRecipes > 0 || len(k.NewIdeas) > 0)
}

// check reads how many plans wait and runs one when some do. err is set
// when Finance or MCP could not be reached for the check.
func (j *BudgetPlanJob) check(ctx context.Context) (waiting int, checked bool, run *BudgetPlanRun, err error) {
	var list struct {
		Waiting int `json:"waiting"`
	}
	if err := j.read(ctx, "finance_list_budget_plans", map[string]any{"limit": 1}, &list); err != nil {
		return 0, false, nil, err
	}
	if list.Waiting == 0 {
		return 0, true, nil, nil
	}
	var claim struct {
		Plan *budgetPlan `json:"plan"`
	}
	raw, err := j.Tools.Call(ctx, "finance_claim_budget_plan", map[string]any{})
	if err != nil {
		return list.Waiting, true, nil, fmt.Errorf("finance_claim_budget_plan: %w", err)
	}
	if err := json.Unmarshal(toolData(raw), &claim); err != nil {
		return list.Waiting, true, nil, fmt.Errorf("decode finance_claim_budget_plan: %w", err)
	}
	if claim.Plan == nil || claim.Plan.ID == "" {
		// Another runs, or it was taken meanwhile.
		return list.Waiting, true, nil, nil
	}
	return list.Waiting, true, j.run(ctx, *claim.Plan), nil
}

// run is one run of a claimed plan; its outcome is returned, never an
// error. A failure is handed back to Finance: to run again later, or failed
// for good.
func (j *BudgetPlanJob) run(ctx context.Context, plan budgetPlan) *BudgetPlanRun {
	run := &BudgetPlanRun{PlanID: plan.ID, Month: plan.Month, Trigger: plan.Trigger, StartedAt: j.now(), Attempt: plan.Attempts}
	ctx = activity.WithTrace(ctx, activity.Trace{CorrelationID: "budget-plan-" + runKey(fmt.Sprintf("%s:%d", plan.ID, plan.Attempts)), Actor: budgetPlanActor})
	finish := func(outcome string, err error) *BudgetPlanRun {
		run.Outcome, run.FinishedAt = outcome, j.now()
		if err != nil {
			run.Error = activity.Preview(err.Error(), 500)
		}
		return run
	}
	fail := func(kind failureKind, err error) *BudgetPlanRun {
		return finish(j.handBack(ctx, plan, kind, err), err)
	}
	var data planningData
	if err := j.read(ctx, "finance_get_budget_planning_data", map[string]any{"month": plan.Month}, &data); err != nil {
		return fail(failureDependency, err)
	}
	food := j.kitchen(ctx)
	var goal *int64
	if plan.SavingsGoal != nil {
		goal = &plan.SavingsGoal.Amount
	}
	prompt, refs := budgetPlanPrompt(data, food, goal, j.now().In(j.location()))
	raw, err := j.Planner.BudgetPlan(ctx, prompt, budgetPlanSchema)
	if err != nil {
		return fail(failureCodex, err)
	}
	checked, err := checkBudgetPlan(raw, data, refs, food)
	if err != nil {
		return fail(failureModelOutput, err)
	}
	run.Categories, run.Suggestions = len(checked.Lines), len(checked.Suggestions)
	arguments := checked.arguments()
	arguments["planId"] = plan.ID
	if _, err := j.Tools.Call(ctx, "finance_set_budget_plan_result", arguments); err != nil {
		if strings.Contains(err.Error(), "plan_not_running") {
			// Dismissed while the AI planned: nothing to leave.
			return finish("dropped", err)
		}
		return fail(classifyActionError(err), err)
	}
	return finish("ready", nil)
}

// handBack tells Finance that a run failed: to run again at the time the
// retry policy says, or failed for good after too many runs. A shutdown
// hands the plan back to run again at once. It answers the run's outcome.
func (j *BudgetPlanJob) handBack(ctx context.Context, plan budgetPlan, kind failureKind, cause error) string {
	arguments := map[string]any{"planId": plan.ID, "error": activity.Preview(cause.Error(), 500)}
	outcome := "retry"
	callCtx := ctx
	limit := budgetPlanMaxRuns
	if kind == failureModelOutput {
		limit = maxModelOutputAttempts
	}
	switch {
	case ctx.Err() != nil:
		// Shutting down: the next processor runs it.
		arguments["retryAt"] = j.now().UTC().Format(time.RFC3339)
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), budgetPlanReleaseTimeout)
		defer cancel()
	case plan.Attempts >= limit:
		outcome = "failed"
		arguments["error"] = activity.Preview(fmt.Sprintf("gave up after %d runs: %s", plan.Attempts, cause.Error()), 500)
	default:
		at, _ := j.Retry.next(ctx, kind, max(plan.Attempts-1, 0), cause.Error())
		arguments["retryAt"] = at.UTC().Format(time.RFC3339)
	}
	if _, err := j.Tools.Call(callCtx, "finance_fail_budget_plan", arguments); err != nil && j.Logf != nil {
		// Finance takes the plan back when the claim lapses.
		j.Logf("AI budget plan: could not hand plan %s back: %v", plan.ID, err)
	}
	return outcome
}

// kitchen reads what Food has; nil when Food cannot say (the plan does
// without).
func (j *BudgetPlanJob) kitchen(ctx context.Context) *kitchen {
	food := &kitchen{PantryItems: []string{}, NewIdeas: []string{}}
	var pantry struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := j.read(ctx, "food_list_pantry_items", map[string]any{"state": "in_stock", "limit": 200}, &pantry); err != nil {
		return nil
	}
	food.PantryInStock = len(pantry.Items)
	for _, item := range pantry.Items {
		if len(food.PantryItems) < 20 {
			food.PantryItems = append(food.PantryItems, promptText(item.Name, maxPromptMerchant))
		}
	}
	var recipes struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := j.read(ctx, "food_list_recipes", map[string]any{"limit": 50}, &recipes); err != nil {
		return nil
	}
	food.SavedRecipes = len(recipes.Items)
	var ideas struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := j.read(ctx, "food_list_ideas", map[string]any{"state": "new", "limit": 5}, &ideas); err != nil {
		return nil
	}
	for _, idea := range ideas.Items {
		food.NewIdeas = append(food.NewIdeas, promptText(idea.Title, maxPromptMerchant))
	}
	return food
}

// read calls an MCP read tool and decodes its data into out.
func (j *BudgetPlanJob) read(ctx context.Context, tool string, arguments map[string]any, out any) error {
	raw, err := j.Tools.Call(ctx, tool, arguments)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if err := json.Unmarshal(toolData(raw), out); err != nil {
		return fmt.Errorf("decode %s: %w", tool, err)
	}
	return nil
}

// budgetPlanSchema is the JSON Schema of Codex's answer (--output-schema).
// Every field is required, as structured output wants; categoryId and link
// may be null. Everything else is checked afterwards (checkBudgetPlan).
const budgetPlanSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["currency", "headline", "lines", "suggestions", "projectedTotalMinor", "incomeEstimateMinor", "projectedSavingsMinor"],
  "properties": {
    "currency": {"type": "string"},
    "headline": {"type": "string"},
    "lines": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["category", "proposedMinor", "reason"],
        "properties": {
          "category": {"type": "string"},
          "proposedMinor": {"type": "integer"},
          "reason": {"type": "string"}
        }
      }
    },
    "suggestions": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "detail", "category", "merchants", "monthlySavingMinor", "link"],
        "properties": {
          "title": {"type": "string"},
          "detail": {"type": "string"},
          "category": {"type": ["string", "null"]},
          "merchants": {"type": "array", "items": {"type": "string"}},
          "monthlySavingMinor": {"type": "integer"},
          "link": {"type": ["string", "null"], "enum": ["food_recipes", null]}
        }
      }
    },
    "projectedTotalMinor": {"type": "integer"},
    "incomeEstimateMinor": {"type": "integer"},
    "projectedSavingsMinor": {"type": "integer"}
  }
}`

// budgetPlanPromptRules are the job's fixed instructions; the month's data
// follow.
const budgetPlanPromptRules = "You plan a month's budget for the owner of Life OS, who lives in Germany, and show where they can spend less. Return only JSON matching the given schema. The data below are the owner's aggregates, never single payments: the complete months of history (months, oldest first; the first may be partial), income and spending per month, and per expense category (ref c1, c2, …) its spending per month, the median of the last three months, last month, the trend, its amount in the current plan, and its top merchants (payments, total, and per month over merchantMonths); the confirmed subscriptions; the balance now, pending payments included; the owner's savings goal per month when they gave one; and what Food has (pantry items in stock, saved recipes, new recipe ideas). All amounts are integers in minor units (cents) of the currency: 12345 is 123.45. Rules: give every category a line (category is its ref) with proposedMinor, 0 or more: a realistic amount for the month, from the median and last month and the trend; keep fixed costs (rent, utilities and phone, insurance, the subscriptions) as they are and lower flexible ones (eating out, shopping, fun) where the data show room; round to whole units of the currency. reason: one short sentence naming the numbers that matter. suggestions: 2 to 5 concrete ways to spend less, the largest first, each for a category (its ref) and, where the data show them, merchants named exactly as listed for that category (the merchants array), with monthlySavingMinor above 0 and never more than those merchants cost a month (or the category, without merchants); for example eating out versus groceries, or many small payments at one merchant. When Food has saved recipes, recipe ideas, or pantry items, a suggestion to cook more instead of eating out or ordering in may say so with link \"food_recipes\"; otherwise link is null. Never invent categories, merchants, or numbers. incomeEstimateMinor: the month's expected income, the median income unless the history says otherwise. projectedTotalMinor: exactly the sum of the lines. projectedSavingsMinor: exactly incomeEstimateMinor minus projectedTotalMinor (negative when the plan spends more than the income). With a savings goal, aim to reach it by lowering flexible categories, never below what must be paid, and say in the headline when it cannot be reached. Mind the balance now: when it is low, be strict. When the history is rough (fewer than 2 full months), stay close to what was spent and say that the plan is rough. headline: one concrete sentence for the owner (the main saving, and the goal). Write in English. currency: the data's currency. "

// promptCategory is what Codex is told about one category.
type promptCategory struct {
	Ref          string           `json:"ref"`
	Name         string           `json:"name"`
	Monthly      []int64          `json:"monthlyMinor"`
	Median       *int64           `json:"medianMinor"`
	LastMonth    *int64           `json:"lastMonthMinor"`
	Trend        string           `json:"trend,omitempty"`
	TrendPercent *int             `json:"trendPercent,omitempty"`
	Planned      int64            `json:"currentPlanMinor"`
	Merchants    []promptMerchant `json:"merchants"`
}

type promptMerchant struct {
	Name     string `json:"name"`
	Payments int    `json:"payments"`
	Total    int64  `json:"totalMinor"`
	PerMonth int64  `json:"perMonthMinor"`
}

// planRefs maps the prompt's category refs and merchant names back to
// Finance's.
type planRefs struct {
	// categories are by ref (c1, …).
	categories map[string]planningCategory
	// merchants are per category ref, by the prompt's name compared
	// without case or repeated spaces, Finance's name.
	merchants map[string]map[string]planningMerchant
}

// budgetPlanPrompt is the prompt of one run: the rules and the month's
// data, merchant and subscription names without IBANs, card numbers, or
// other long numbers.
func budgetPlanPrompt(data planningData, food *kitchen, goal *int64, now time.Time) (string, planRefs) {
	refs := planRefs{categories: map[string]planningCategory{}, merchants: map[string]map[string]planningMerchant{}}
	type promptSubscription struct {
		Name     string `json:"name"`
		PerMonth int64  `json:"perMonthMinor"`
		Category string `json:"category,omitempty"`
	}
	owner := struct {
		Month             string               `json:"month"`
		Today             string               `json:"today"`
		Currency          string               `json:"currency"`
		SavingsGoal       *int64               `json:"savingsGoalPerMonthMinor"`
		Balance           int64                `json:"balanceNowMinor"`
		Months            []string             `json:"months"`
		FirstMonthPartial bool                 `json:"firstMonthPartial"`
		FullMonths        int                  `json:"fullMonths"`
		Rough             bool                 `json:"rough"`
		MerchantMonths    int                  `json:"merchantMonths"`
		Income            []int64              `json:"incomeMinor"`
		IncomeMedian      *int64               `json:"incomeMedianMinor"`
		Spending          []int64              `json:"spendingMinor"`
		Uncategorized     []int64              `json:"uncategorizedMinor"`
		Categories        []promptCategory     `json:"categories"`
		CurrentPlan       any                  `json:"currentPlan"`
		Subscriptions     []promptSubscription `json:"subscriptions"`
		Food              *kitchen             `json:"food"`
	}{Month: data.Month, Today: now.Format("Monday 2006-01-02"), Currency: data.Currency, SavingsGoal: goal, Balance: data.BalanceMinor,
		Months: nonNil(data.Months), FirstMonthPartial: data.FirstMonthPartial, FullMonths: data.FullMonths, Rough: data.Rough, MerchantMonths: data.MerchantMonths,
		Income: nonNilAmounts(data.IncomeMinor), IncomeMedian: data.IncomeMedianMinor, Spending: nonNilAmounts(data.SpendingMinor),
		Uncategorized: nonNilAmounts(data.UncategorizedMinor), Categories: []promptCategory{}, Subscriptions: []promptSubscription{}, Food: food}
	if data.Plan != nil {
		owner.CurrentPlan = map[string]int64{"expectedIncomeMinor": data.Plan.ExpectedIncomeMinor, "savingsGoalMinor": data.Plan.SavingsGoalMinor,
			"plannedMinor": data.Plan.PlannedMinor}
	}
	byID := map[string]string{}
	for index, category := range data.Categories {
		ref := fmt.Sprintf("c%d", index+1)
		refs.categories[ref] = category
		refs.merchants[ref] = map[string]planningMerchant{}
		byID[category.ID] = ref
		item := promptCategory{Ref: ref, Name: promptText(category.Name, maxPromptMerchant), Monthly: nonNilAmounts(category.MonthlyMinor), Median: category.MedianMinor,
			LastMonth: category.LastMonthMinor, TrendPercent: category.TrendPercent, Planned: category.PlannedMinor, Merchants: []promptMerchant{}}
		if category.Trend != nil {
			item.Trend = *category.Trend
		}
		for _, merchant := range category.Merchants {
			name := promptText(merchant.Name, maxPromptMerchant)
			if name == "" {
				continue
			}
			refs.merchants[ref][comparableName(name)] = merchant
			item.Merchants = append(item.Merchants, promptMerchant{Name: name, Payments: merchant.Count, Total: merchant.TotalMinor, PerMonth: merchant.MonthlyMinor})
		}
		owner.Categories = append(owner.Categories, item)
	}
	for _, sub := range data.Subscriptions {
		item := promptSubscription{Name: promptText(sub.Name, maxPromptMerchant), PerMonth: sub.MonthlyMinor}
		if sub.CategoryID != nil {
			item.Category = byID[*sub.CategoryID]
		}
		owner.Subscriptions = append(owner.Subscriptions, item)
	}
	encoded, _ := json.Marshal(owner)
	return budgetPlanPromptRules + "The month's data: " + string(encoded), refs
}

func nonNilAmounts(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	return values
}

// comparableName is a name compared without case or repeated spaces.
func comparableName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// Limits of a checked plan, Finance's too.
const (
	minPlanHeadline, maxPlanHeadline = 10, 200
	minPlanReason, maxPlanReason     = 5, 240
	minPlanTitle, maxPlanTitle       = 3, 80
	minPlanDetail, maxPlanDetail     = 10, 300
	minPlanSuggestions               = 2
	maxPlanSuggestions               = 5
	maxPlanMerchants                 = 5
)

// checkedPlan is a plan that passed every check, with Finance's ids and
// names.
type checkedPlan struct {
	Currency                                         string
	Headline                                         string
	Lines                                            []checkedLine
	Suggestions                                      []checkedSuggestion
	ProjectedTotal, IncomeEstimate, ProjectedSavings int64
}

type checkedLine struct {
	CategoryID string
	Proposed   int64
	Reason     string
}

type checkedSuggestion struct {
	Title, Detail string
	CategoryID    *string
	Merchants     []string
	MonthlySaving int64
	Link          *string
}

// arguments is the finance_set_budget_plan_result call for a checked plan.
func (p checkedPlan) arguments() map[string]any {
	lines := make([]any, 0, len(p.Lines))
	for _, line := range p.Lines {
		lines = append(lines, map[string]any{"categoryId": line.CategoryID, "proposedMinor": line.Proposed, "reason": line.Reason})
	}
	suggestions := make([]any, 0, len(p.Suggestions))
	for _, suggestion := range p.Suggestions {
		suggestions = append(suggestions, map[string]any{"title": suggestion.Title, "detail": suggestion.Detail, "categoryId": suggestion.CategoryID,
			"merchants": suggestion.Merchants, "monthlySavingMinor": suggestion.MonthlySaving, "link": suggestion.Link})
	}
	return map[string]any{"currency": p.Currency, "headline": p.Headline, "lines": lines, "suggestions": suggestions,
		"projectedTotalMinor": p.ProjectedTotal, "incomeEstimateMinor": p.IncomeEstimate, "projectedSavingsMinor": p.ProjectedSavings}
}

// checkBudgetPlan decodes Codex's answer and checks it as Finance will:
//
//   - the data's currency, and a headline;
//   - a line for every category of the data (by ref, once each, an amount
//     of 0 or more, and a reason), but a category without spending and
//     without an amount in the plan may be left out (Finance gives it 0);
//   - 2 to 5 suggestions (none needed without spending), each saving more
//     than 0 and no more than the spending it comes from: its category's
//     larger of median and last month, the named merchants' monthly
//     spending (they must be its listed merchants, mapped back to
//     Finance's names), or all spending without a category; together no
//     more than all spending. A link to Food's recipes is kept only when
//     Food has something to cook;
//   - totals that add up to within one unit of the currency, kept exact.
//
// An answer that fails is an error, and the run is retried.
func checkBudgetPlan(raw string, data planningData, refs planRefs, food *kitchen) (checkedPlan, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var answer struct {
		Currency string `json:"currency"`
		Headline string `json:"headline"`
		Lines    []struct {
			Category string `json:"category"`
			Proposed *int64 `json:"proposedMinor"`
			Reason   string `json:"reason"`
		} `json:"lines"`
		Suggestions []struct {
			Title     string   `json:"title"`
			Detail    string   `json:"detail"`
			Category  *string  `json:"category"`
			Merchants []string `json:"merchants"`
			Saving    *int64   `json:"monthlySavingMinor"`
			Link      *string  `json:"link"`
		} `json:"suggestions"`
		ProjectedTotal   *int64 `json:"projectedTotalMinor"`
		IncomeEstimate   *int64 `json:"incomeEstimateMinor"`
		ProjectedSavings *int64 `json:"projectedSavingsMinor"`
	}
	if err := json.Unmarshal([]byte(clean), &answer); err != nil {
		return checkedPlan{}, fmt.Errorf("decode budget plan: %w", err)
	}
	if answer.ProjectedTotal == nil || answer.IncomeEstimate == nil || answer.ProjectedSavings == nil {
		return checkedPlan{}, errors.New("budget plan: the totals are missing")
	}
	if strings.ToUpper(strings.TrimSpace(answer.Currency)) != data.Currency {
		return checkedPlan{}, fmt.Errorf("budget plan: in %q, not %s", answer.Currency, data.Currency)
	}
	plan := checkedPlan{Currency: data.Currency, Lines: []checkedLine{}, Suggestions: []checkedSuggestion{}}
	var err error
	if plan.Headline, err = planText("the headline", answer.Headline, minPlanHeadline, maxPlanHeadline); err != nil {
		return checkedPlan{}, err
	}
	seen := map[string]bool{}
	for _, line := range answer.Lines {
		ref := strings.TrimSpace(line.Category)
		category, known := refs.categories[ref]
		switch {
		case !known:
			return checkedPlan{}, fmt.Errorf("budget plan: unknown category %q", ref)
		case seen[ref]:
			return checkedPlan{}, fmt.Errorf("budget plan: %s has two lines", category.Name)
		case line.Proposed == nil || *line.Proposed < 0:
			return checkedPlan{}, fmt.Errorf("budget plan: the amount for %s is missing or negative", category.Name)
		}
		seen[ref] = true
		reason, err := planText("the reason for "+category.Name, line.Reason, minPlanReason, maxPlanReason)
		if err != nil {
			return checkedPlan{}, err
		}
		plan.Lines = append(plan.Lines, checkedLine{CategoryID: category.ID, Proposed: *line.Proposed, Reason: reason})
		plan.ProjectedTotal += *line.Proposed
	}
	var everything int64
	for ref, category := range refs.categories {
		everything += category.baseline()
		if !seen[ref] && (category.history() || category.PlannedMinor > 0) {
			return checkedPlan{}, fmt.Errorf("budget plan: no amount for %s", category.Name)
		}
	}
	uncategorized := int64(0)
	if n := len(data.UncategorizedMinor); n > 0 {
		uncategorized = data.UncategorizedMinor[n-1]
	}
	everything += uncategorized
	if count := len(answer.Suggestions); count > maxPlanSuggestions || (everything > 0 && count < minPlanSuggestions) {
		return checkedPlan{}, fmt.Errorf("budget plan: %d ways to spend less, not %d to %d", count, minPlanSuggestions, maxPlanSuggestions)
	}
	var savings int64
	for index, in := range answer.Suggestions {
		number := index + 1
		title, err := planText(fmt.Sprintf("suggestion %d's title", number), in.Title, minPlanTitle, maxPlanTitle)
		if err != nil {
			return checkedPlan{}, err
		}
		detail, err := planText(fmt.Sprintf("suggestion %d's detail", number), in.Detail, minPlanDetail, maxPlanDetail)
		if err != nil {
			return checkedPlan{}, err
		}
		if in.Saving == nil || *in.Saving <= 0 {
			return checkedPlan{}, fmt.Errorf("budget plan: suggestion %d saves nothing", number)
		}
		if len(in.Merchants) > maxPlanMerchants {
			return checkedPlan{}, fmt.Errorf("budget plan: suggestion %d names more than %d merchants", number, maxPlanMerchants)
		}
		out := checkedSuggestion{Title: title, Detail: detail, Merchants: []string{}, MonthlySaving: *in.Saving}
		limit := uncategorized
		pool := []string{}
		if in.Category != nil && strings.TrimSpace(*in.Category) != "" {
			ref := strings.TrimSpace(*in.Category)
			category, known := refs.categories[ref]
			if !known {
				return checkedPlan{}, fmt.Errorf("budget plan: suggestion %d is for unknown category %q", number, ref)
			}
			id := category.ID
			out.CategoryID, limit, pool = &id, category.baseline(), []string{ref}
		} else {
			for ref, category := range refs.categories {
				limit += category.baseline()
				pool = append(pool, ref)
			}
		}
		if len(in.Merchants) > 0 {
			var merchantsMonthly int64
			for _, name := range in.Merchants {
				merchant, found := planMerchant(refs, pool, name)
				if !found {
					return checkedPlan{}, fmt.Errorf("budget plan: suggestion %d names %q, not a listed merchant", number, promptText(name, maxPromptMerchant))
				}
				if slices.Contains(out.Merchants, merchant.Name) {
					continue
				}
				out.Merchants = append(out.Merchants, merchant.Name)
				merchantsMonthly += merchant.MonthlyMinor
			}
			limit = min(limit, merchantsMonthly)
		}
		if out.MonthlySaving > limit {
			return checkedPlan{}, fmt.Errorf("budget plan: suggestion %d saves %d, more than the %d it comes from", number, out.MonthlySaving, limit)
		}
		if in.Link != nil && *in.Link == "food_recipes" && food.hasFood() {
			link := *in.Link
			out.Link = &link
		}
		savings += out.MonthlySaving
		plan.Suggestions = append(plan.Suggestions, out)
	}
	if savings > everything {
		return checkedPlan{}, errors.New("budget plan: the savings add up to more than all the spending")
	}
	tolerance := int64(math.Pow10(currencyExponent(data.Currency)))
	if *answer.IncomeEstimate < 0 {
		return checkedPlan{}, errors.New("budget plan: the income estimate is negative")
	}
	if diff := *answer.ProjectedTotal - plan.ProjectedTotal; diff > tolerance || diff < -tolerance {
		return checkedPlan{}, fmt.Errorf("budget plan: the projected total %d is not the sum of the lines, %d", *answer.ProjectedTotal, plan.ProjectedTotal)
	}
	plan.IncomeEstimate = *answer.IncomeEstimate
	plan.ProjectedSavings = plan.IncomeEstimate - plan.ProjectedTotal
	if diff := *answer.ProjectedSavings - plan.ProjectedSavings; diff > tolerance || diff < -tolerance {
		return checkedPlan{}, fmt.Errorf("budget plan: the projected savings %d are not the income estimate minus the total, %d", *answer.ProjectedSavings, plan.ProjectedSavings)
	}
	return plan, nil
}

// planMerchant finds a merchant of the categories by the name the prompt
// gave it.
func planMerchant(refs planRefs, pool []string, name string) (planningMerchant, bool) {
	wanted := comparableName(name)
	for _, ref := range pool {
		if merchant, ok := refs.merchants[ref][wanted]; ok {
			return merchant, true
		}
	}
	return planningMerchant{}, false
}

// planText trims a text and checks its length.
func planText(name, value string, minimum, maximum int) (string, error) {
	value = strings.Join(strings.Fields(value), " ")
	if count := utf8.RuneCountInString(value); count < minimum || count > maximum {
		return "", fmt.Errorf("budget plan: %s must have %d to %d characters", name, minimum, maximum)
	}
	return value, nil
}

// currencyExponent is the number of minor-unit digits of a currency, as
// Finance counts them.
func currencyExponent(currency string) int {
	switch currency {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}
