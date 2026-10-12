package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

const planActor = "agent:codex-budget-plan"

// planHistory is made-up spending in the Cash account from July to
// September 2026 (the owner is in October): groceries, eating out at two
// places, rent, and a salary. Shopping has no spending.
type planHistory struct {
	groceries, eatingOut, rent, shopping, salary generated.Category
}

func seedPlanHistory(c apiClient) planHistory {
	c.t.Helper()
	h := planHistory{groceries: c.category("Groceries", "expense"), eatingOut: c.category("Eating out", "expense"), rent: c.category("Rent", "expense"),
		shopping: c.category("Shopping", "expense"), salary: c.category("Salary", "income")}
	cash := c.cash()
	spend := func(amount int64, date, counterparty string, category generated.Category) {
		c.spend(map[string]any{"accountId": cash.Id, "amount": eur(amount), "bookingDate": date, "counterparty": counterparty, "categoryId": category.Id})
	}
	spend(-12000, "2026-07-03", "REWE Markt", h.groceries)
	spend(-28000, "2026-08-03", "REWE Markt", h.groceries)
	spend(-30000, "2026-09-03", "REWE Markt", h.groceries)
	spend(-3000, "2026-09-20", "Lidl", h.groceries)
	spend(-5000, "2026-07-10", "Pizzeria Roma", h.eatingOut)
	spend(-6000, "2026-08-10", "Kneipe41", h.eatingOut)
	spend(-7000, "2026-09-10", "Kneipe41", h.eatingOut)
	spend(-9000, "2026-09-12", "Pizzeria Roma", h.eatingOut)
	spend(-80000, "2026-08-01", "Hausverwaltung", h.rent)
	spend(-80000, "2026-09-01", "Hausverwaltung", h.rent)
	spend(200000, "2026-08-01", "ACME GmbH", h.salary)
	spend(210000, "2026-09-01", "ACME GmbH", h.salary)
	return h
}

// validProposal is a proposal that passes the checks for planHistory.
func validProposal(h planHistory) map[string]any {
	return map[string]any{
		"currency": "EUR", "headline": "Money is tight: eat out less and keep groceries steady.",
		"lines": []map[string]any{
			{"categoryId": h.groceries.Id, "proposedMinor": 30000, "reason": "About what groceries usually cost."},
			{"categoryId": h.eatingOut.Id, "proposedMinor": 9000, "reason": "Eating out grew; bring it back down."},
			{"categoryId": h.rent.Id, "proposedMinor": 80000, "reason": "Rent stays the same."},
		},
		"suggestions": []map[string]any{
			{"title": "Cook instead of pizza", "detail": "Pizzeria Roma is your largest eating out spend: cook twice a week.", "categoryId": h.eatingOut.Id,
				"merchants": []string{"Pizzeria Roma"}, "monthlySavingMinor": 3000, "link": "food_recipes"},
			{"title": "Fewer bar nights", "detail": "Two evenings at Kneipe41 a month are enough.", "categoryId": h.eatingOut.Id,
				"merchants": []string{"Kneipe41"}, "monthlySavingMinor": 2000, "link": nil},
		},
		"projectedTotalMinor": 119000, "incomeEstimateMinor": 205000, "projectedSavingsMinor": 86000,
	}
}

func (c apiClient) requestPlan(body map[string]any, want int, actor string) generated.AiBudgetPlan {
	c.t.Helper()
	return expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans", body, want, activity.ActorHeader, actor)
}

func (c apiClient) claimPlan() *generated.AiBudgetPlan {
	c.t.Helper()
	return expect[generated.AiBudgetPlanClaim](c, http.MethodPost, "/v1/ai-budget-plans/claim", nil, http.StatusOK, activity.ActorHeader, planActor).Plan
}

func (c apiClient) planError(method, target string, body any, actor string, wantStatus int, wantCode string) string {
	c.t.Helper()
	status, raw := c.do(method, target, body, activity.ActorHeader, actor)
	if status != wantStatus || !strings.Contains(string(raw), `"code":"`+wantCode+`"`) {
		c.t.Fatalf("%s %s: %d %s, want %d %s", method, target, status, raw, wantStatus, wantCode)
	}
	return string(raw)
}

func TestAIBudgetPlansAreRequestedClaimedCheckedAndStored(t *testing.T) {
	c := newAPIClient(t)
	h := seedPlanHistory(c)
	seen := len(c.recorder.all())

	// It is 11 October in Berlin: by default the plan is for November.
	plan := c.requestPlan(map[string]any{"savingsGoalMinor": 50000}, http.StatusAccepted, "user")
	if plan.Month != "2026-11" || plan.State != generated.AiBudgetPlanStateRequested || plan.Trigger != generated.AiBudgetPlanTriggerRequest || plan.RequestedBy.Kind != generated.User ||
		plan.SavingsGoal.Amount != 50000 || plan.Result != nil || plan.Attempts != 0 {
		t.Fatalf("requested = %+v", plan)
	}
	// Asking again while it waits joins it.
	if again := c.requestPlan(map[string]any{}, http.StatusOK, "user"); again.Id != plan.Id {
		t.Fatalf("a second request made %v", again.Id)
	}
	c.planError(http.MethodPost, "/v1/ai-budget-plans", map[string]any{"month": "2026-09"}, "user", http.StatusBadRequest, "invalid_request")
	c.planError(http.MethodPost, "/v1/ai-budget-plans", map[string]any{"month": "2027-11"}, "user", http.StatusBadRequest, "invalid_request")
	c.planError(http.MethodPost, "/v1/ai-budget-plans", map[string]any{"savingsGoalMinor": -1}, "user", http.StatusBadRequest, "invalid_request")
	events := c.recorder.since(&seen)
	if len(events) != 1 || events[0].Type != "finance.ai_budget_plan.requested" || events[0].Actor != activity.User() ||
		events[0].Summary != "AI budget plan for November 2026 requested, savings goal €500.00" || events[0].Subject.Type != "finance_ai_budget_plan" {
		t.Fatalf("events = %+v", events)
	}
	if list := expect[generated.AiBudgetPlanList](c, http.MethodGet, "/v1/ai-budget-plans?month=2026-11", nil, http.StatusOK); list.Waiting != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// The job claims it; the owner may not, and one plan runs at a time.
	c.planError(http.MethodPost, "/v1/ai-budget-plans/claim", nil, "user", http.StatusForbidden, "actor_not_allowed")
	claimed := c.claimPlan()
	if claimed == nil || claimed.Id != plan.Id || claimed.State != generated.AiBudgetPlanStateRunning || claimed.Attempts != 1 || claimed.ClaimedAt == nil {
		t.Fatalf("claimed = %+v", claimed)
	}
	december := c.requestPlan(map[string]any{"month": "2026-12"}, http.StatusAccepted, "user")
	if next := c.claimPlan(); next != nil {
		t.Fatalf("a second plan ran beside the first: %+v", next)
	}

	// The planning data are aggregates.
	data := expect[generated.BudgetPlanningData](c, http.MethodGet, "/v1/budgets/2026-11/planning-data", nil, http.StatusOK)
	if strings.Join(data.Months, ",") != "2026-07,2026-08,2026-09" || !data.FirstMonthPartial || data.FullMonths != 2 || data.Rough || data.MerchantMonths != 3 ||
		*data.IncomeMedianMinor != 200000 || len(data.Categories) != 4 || data.Plan != nil {
		t.Fatalf("planning data = %+v", data)
	}
	eating := data.Categories[0]
	if eating.Name != "Eating out" || *eating.MedianMinor != 6000 || *eating.LastMonthMinor != 16000 || *eating.Trend != generated.BudgetPlanningCategoryTrendUp ||
		len(eating.Merchants) != 2 || eating.Merchants[0].Name != "Pizzeria Roma" || eating.Merchants[0].Count != 2 || eating.Merchants[0].MonthlyMinor != 4667 {
		t.Fatalf("eating out = %+v", eating)
	}

	// Finance checks the proposal.
	target := "/v1/ai-budget-plans/" + plan.Id.String() + "/result"
	c.planError(http.MethodPut, target, validProposal(h), "user", http.StatusForbidden, "actor_not_allowed")
	unknown := validProposal(h)
	unknown["lines"].([]map[string]any)[0]["categoryId"] = h.salary.Id
	c.planError(http.MethodPut, target, unknown, planActor, http.StatusBadRequest, "invalid_plan")
	negative := validProposal(h)
	negative["lines"].([]map[string]any)[1]["proposedMinor"] = -900
	c.planError(http.MethodPut, target, negative, planActor, http.StatusBadRequest, "invalid_plan")
	tooMuch := validProposal(h)
	// Kneipe41 costs €43.33 a month.
	tooMuch["suggestions"].([]map[string]any)[1]["monthlySavingMinor"] = 5000
	if raw := c.planError(http.MethodPut, target, tooMuch, planActor, http.StatusBadRequest, "invalid_plan"); !strings.Contains(raw, "saves more than the spending it comes from") {
		t.Fatalf("refusal = %s", raw)
	}
	seen = len(c.recorder.all())
	ready := expect[generated.AiBudgetPlan](c, http.MethodPut, target, validProposal(h), http.StatusOK, activity.ActorHeader, planActor)
	result := ready.Result
	if ready.State != generated.AiBudgetPlanStateReady || result == nil || result.ProjectedTotal.Amount != 119000 || result.ProjectedSavings.Amount != 86000 ||
		result.SavingsGoal.Amount != 50000 || result.GoalGap.Amount != 0 || result.Rough || len(result.Lines) != 4 || len(result.Suggestions) != 2 {
		t.Fatalf("ready = %+v result %+v", ready, result)
	}
	if line := result.Lines[0]; line.CategoryName != "Eating out" || line.Proposed.Amount != 9000 || line.Median.Amount != 6000 || line.LastMonth.Amount != 16000 ||
		*line.Trend != generated.AiBudgetPlanLineTrendUp || line.Current.Amount != 0 {
		t.Fatalf("eating out line = %+v", line)
	}
	if shopping := result.Lines[3]; shopping.CategoryName != "Shopping" || shopping.Proposed.Amount != 0 || shopping.Median != nil {
		t.Fatalf("shopping line = %+v", shopping)
	}
	if first := result.Suggestions[0]; *first.CategoryName != "Eating out" || first.Merchants[0] != "Pizzeria Roma" || *first.Link != generated.FoodRecipes ||
		first.MonthlySaving.Amount != 3000 {
		t.Fatalf("suggestion = %+v", first)
	}
	events = c.recorder.since(&seen)
	if len(events) != 1 || events[0].Type != "finance.ai_budget_plan.created" || events[0].Actor != activity.Agent("codex-budget-plan") ||
		events[0].Summary != "AI budget plan for November 2026 ready: €1,190.00 planned for 4 categories, 2 ways to spend less (≈ €50.00 a month)" {
		t.Fatalf("events = %+v", events)
	}
	// Nothing changed the budget.
	if budget := expect[generated.BudgetMonth](c, http.MethodGet, "/v1/budgets/2026-11", nil, http.StatusOK); budget.Plan != nil {
		t.Fatalf("the budget changed: %+v", budget.Plan)
	}
	// A plan that is ready takes no other result or failure.
	c.planError(http.MethodPut, target, validProposal(h), planActor, http.StatusConflict, "plan_not_running")
	c.planError(http.MethodPost, "/v1/ai-budget-plans/"+plan.Id.String()+"/failure", map[string]any{"error": "late"}, planActor, http.StatusConflict, "plan_not_running")
	// December is next.
	if next := c.claimPlan(); next == nil || next.Id != december.Id {
		t.Fatalf("next = %+v", next)
	}
}

func TestAPlanChangesTheBudgetOnlyWhenTheOwnerAppliesIt(t *testing.T) {
	c := newAPIClient(t)
	h := seedPlanHistory(c)
	// The regular plan already has rent and groceries.
	expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2026-10", map[string]any{"scope": "following", "expectedIncomeMinor": 200000, "savingsGoalMinor": 10000,
		"lines": []map[string]any{{"categoryId": h.groceries.Id, "amountMinor": 35000}, {"categoryId": h.rent.Id, "amountMinor": 80000}}}, http.StatusOK)
	plan := c.requestPlan(map[string]any{"month": "2026-11"}, http.StatusAccepted, "user")
	apply := "/v1/ai-budget-plans/" + plan.Id.String() + "/apply"
	c.planError(http.MethodPost, apply, map[string]any{"lines": []map[string]any{{"categoryId": h.groceries.Id, "amountMinor": 30000}}}, "user", http.StatusConflict, "plan_not_ready")
	c.claimPlan()
	ready := expect[generated.AiBudgetPlan](c, http.MethodPut, "/v1/ai-budget-plans/"+plan.Id.String()+"/result", validProposal(h), http.StatusOK, activity.ActorHeader, planActor)
	if line := ready.Result.Lines[1]; line.CategoryName != "Groceries" || line.Current.Amount != 35000 || ready.Result.SavingsGoal.Amount != 10000 {
		t.Fatalf("groceries = %+v, goal %+v", line, ready.Result.SavingsGoal)
	}

	// The AI never applies its own plan.
	c.planError(http.MethodPost, apply, map[string]any{"lines": []map[string]any{{"categoryId": h.groceries.Id, "amountMinor": 30000}}}, planActor, http.StatusForbidden, "actor_not_allowed")
	// The owner unticks rent and lowers eating out further: partly applied,
	// for November only; the regular plan's other lines stay.
	seen := len(c.recorder.all())
	partly := map[string]any{"lines": []map[string]any{{"categoryId": h.groceries.Id, "amountMinor": 30000}, {"categoryId": h.eatingOut.Id, "amountMinor": 8000},
		{"categoryId": h.shopping.Id, "amountMinor": 0}}}
	got := expect[generated.AiBudgetPlanApplyResult](c, http.MethodPost, apply, partly, http.StatusOK, activity.ActorHeader, "user")
	if got.Plan.State != generated.AiBudgetPlanStatePartlyApplied || got.Plan.Applied == nil || len(got.Plan.Applied.Lines) != 3 ||
		got.Plan.Applied.Scope != generated.BudgetScopeMonth || got.Budget.Plan == nil || got.Budget.Plan.Source != generated.BudgetPlanSourceMonth {
		t.Fatalf("applied = %+v budget %+v", got.Plan, got.Budget.Plan)
	}
	planned := map[string]int64{}
	for _, line := range got.Budget.Lines {
		planned[line.CategoryName] = line.Planned.Amount
	}
	if planned["Groceries"] != 30000 || planned["Eating out"] != 8000 || planned["Rent"] != 80000 || planned["Shopping"] != 0 ||
		got.Budget.Plan.ExpectedIncome.Amount != 200000 || got.Budget.Plan.SavingsGoal.Amount != 10000 {
		t.Fatalf("planned = %v, plan %+v", planned, got.Budget.Plan)
	}
	if october := expect[generated.BudgetMonth](c, http.MethodGet, "/v1/budgets/2026-10", nil, http.StatusOK); october.Totals.Planned.Amount != 115000 {
		t.Fatalf("October changed: %+v", october.Totals)
	}
	events := c.recorder.since(&seen)
	if eventTypes(events) != "finance.budget.saved finance.ai_budget_plan.applied" || events[1].Actor != activity.User() ||
		events[1].Summary != "AI budget plan for November 2026 applied: 3 of 4 categories (€380.00), for November 2026 only" {
		t.Fatalf("events = %+v", events)
	}
	// The same choice again changes and records nothing.
	again := expect[generated.AiBudgetPlanApplyResult](c, http.MethodPost, apply, partly, http.StatusOK, activity.ActorHeader, "user")
	if again.Plan.State != generated.AiBudgetPlanStatePartlyApplied || !again.Plan.Applied.At.Equal(got.Plan.Applied.At) || len(c.recorder.since(&seen)) != 0 {
		t.Fatalf("a repeat changed something: %+v", again.Plan)
	}

	// All lines, as the regular plan with the plan's income and goal: applied.
	all := map[string]any{"scope": "following", "expectedIncomeMinor": 205000, "savingsGoalMinor": 20000, "lines": []map[string]any{
		{"categoryId": h.groceries.Id, "amountMinor": 30000}, {"categoryId": h.eatingOut.Id, "amountMinor": 9000},
		{"categoryId": h.rent.Id, "amountMinor": 80000}, {"categoryId": h.shopping.Id, "amountMinor": 0}}}
	full := expect[generated.AiBudgetPlanApplyResult](c, http.MethodPost, apply, all, http.StatusOK, activity.ActorHeader, "user")
	if full.Plan.State != generated.AiBudgetPlanStateApplied || full.Budget.Plan.Source != generated.BudgetPlanSourceRegular ||
		full.Budget.Totals.Planned.Amount != 119000 || full.Budget.Plan.ExpectedIncome.Amount != 205000 || full.Budget.Plan.SavingsGoal.Amount != 20000 {
		t.Fatalf("applied all = %+v budget %+v", full.Plan, full.Budget.Plan)
	}

	// Only the plan's categories, each once, never negative.
	c.planError(http.MethodPost, apply, map[string]any{"lines": []map[string]any{{"categoryId": h.salary.Id, "amountMinor": 100}}}, "user", http.StatusBadRequest, "invalid_request")
	c.planError(http.MethodPost, apply, map[string]any{"lines": []map[string]any{{"categoryId": h.rent.Id, "amountMinor": -1}}}, "user", http.StatusBadRequest, "invalid_request")
	c.planError(http.MethodPost, apply, map[string]any{"lines": []map[string]any{}}, "user", http.StatusBadRequest, "invalid_request")

	// Dismissing hides the plan; what it applied stays.
	seen = len(c.recorder.all())
	dismissed := expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans/"+plan.Id.String()+"/dismiss", nil, http.StatusOK, activity.ActorHeader, "user")
	expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans/"+plan.Id.String()+"/dismiss", nil, http.StatusOK, activity.ActorHeader, "user")
	if dismissed.State != generated.AiBudgetPlanStateDismissed || dismissed.Applied == nil || eventTypes(c.recorder.since(&seen)) != "finance.ai_budget_plan.dismissed" {
		t.Fatalf("dismissed = %+v", dismissed)
	}
	c.planError(http.MethodPost, apply, all, "user", http.StatusConflict, "plan_dismissed")
	if november := expect[generated.BudgetMonth](c, http.MethodGet, "/v1/budgets/2026-11", nil, http.StatusOK); november.Totals.Planned.Amount != 119000 {
		t.Fatalf("November = %+v", november.Totals)
	}
}

func TestFailedRunsWaitToRetryOrFailAndLapsedClaimsAreTakenOver(t *testing.T) {
	c := newAPIClient(t)
	plan := c.requestPlan(map[string]any{"month": "2026-10"}, http.StatusAccepted, "user")
	failure := "/v1/ai-budget-plans/" + plan.Id.String() + "/failure"
	c.claimPlan()
	retryAt := fixedNow.Add(10 * time.Minute)
	waiting := expect[generated.AiBudgetPlan](c, http.MethodPost, failure, map[string]any{"error": "ERROR: You've hit your usage limit", "retryAt": retryAt}, http.StatusOK,
		activity.ActorHeader, planActor)
	if waiting.State != generated.AiBudgetPlanStateRequested || !waiting.RetryAt.Equal(retryAt) || *waiting.Error != "ERROR: You've hit your usage limit" || waiting.FinishedAt != nil {
		t.Fatalf("waiting = %+v", waiting)
	}
	if early := c.claimPlan(); early != nil {
		t.Fatalf("claimed before its retry: %+v", early)
	}
	c.handler.now = func() time.Time { return retryAt }
	second := c.claimPlan()
	if second == nil || second.Attempts != 2 {
		t.Fatalf("second = %+v", second)
	}
	// The processor stopped mid-run: after 15 minutes another claim takes
	// the plan over.
	c.handler.now = func() time.Time { return retryAt.Add(14 * time.Minute) }
	if c.claimPlan() != nil {
		t.Fatal("a fresh claim was taken over")
	}
	c.handler.now = func() time.Time { return retryAt.Add(16 * time.Minute) }
	third := c.claimPlan()
	if third == nil || third.Id != plan.Id || third.Attempts != 3 {
		t.Fatalf("third = %+v", third)
	}
	seen := len(c.recorder.all())
	failed := expect[generated.AiBudgetPlan](c, http.MethodPost, failure, map[string]any{"error": "unusable answers 5 times"}, http.StatusOK, activity.ActorHeader, planActor)
	events := c.recorder.since(&seen)
	if failed.State != generated.AiBudgetPlanStateFailed || failed.FinishedAt == nil || len(events) != 1 || events[0].Type != "finance.ai_budget_plan.failed" ||
		events[0].Outcome != activity.Failed {
		t.Fatalf("failed = %+v events %+v", failed, events)
	}
	c.planError(http.MethodPost, failure, map[string]any{"error": "again"}, "user", http.StatusForbidden, "actor_not_allowed")
	c.planError(http.MethodPost, failure, map[string]any{"error": " "}, planActor, http.StatusBadRequest, "invalid_request")
	// A failed plan can be asked for again.
	if again := c.requestPlan(map[string]any{"month": "2026-10"}, http.StatusAccepted, "user"); again.Id == plan.Id {
		t.Fatal("the failed plan was reused")
	}
}

func TestTheScheduledPlanIsOnePerMonthAndRequestsAreLimited(t *testing.T) {
	c := newAPIClient(t)
	scheduled := c.requestPlan(map[string]any{"month": "2026-11", "trigger": "schedule"}, http.StatusAccepted, planActor)
	if scheduled.Trigger != generated.AiBudgetPlanTriggerSchedule || scheduled.RequestedBy.Kind != generated.Agent || *scheduled.RequestedBy.Name != "codex-budget-plan" {
		t.Fatalf("scheduled = %+v", scheduled)
	}
	// Dismissed or not, a month has one scheduled plan.
	expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans/"+scheduled.Id.String()+"/dismiss", nil, http.StatusOK, activity.ActorHeader, "user")
	if again := c.requestPlan(map[string]any{"month": "2026-11", "trigger": "schedule"}, http.StatusOK, planActor); again.Id != scheduled.Id {
		t.Fatalf("a second scheduled plan: %+v", again)
	}
	// The owner asked for October this month: the schedule joins it.
	asked := c.requestPlan(map[string]any{"month": "2026-10"}, http.StatusAccepted, "user")
	if joined := c.requestPlan(map[string]any{"month": "2026-10", "trigger": "schedule"}, http.StatusOK, planActor); joined.Id != asked.Id {
		t.Fatalf("the schedule did not join the owner's plan: %+v", joined)
	}
	// Ten requests a day.
	for month := 12; month <= 20; month++ {
		key := time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		c.requestPlan(map[string]any{"month": key}, http.StatusAccepted, "user")
	}
	c.planError(http.MethodPost, "/v1/ai-budget-plans", map[string]any{"month": "2027-09"}, "user", http.StatusTooManyRequests, "daily_limit")
	// Unknown states and limits are refused.
	c.expectError(http.MethodGet, "/v1/ai-budget-plans?state=thinking", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/ai-budget-plans?limit=51", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/ai-budget-plans/c0000000-0000-4000-8000-000000000001", nil, http.StatusNotFound, "not_found")
}

func TestOnlyTheOwnersRequestsAreLimited(t *testing.T) {
	c := newAPIClient(t)
	for month := 10; month <= 21; month++ {
		key := time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		c.requestPlan(map[string]any{"month": key}, http.StatusAccepted, "dev:e2e")
	}
	// A development tool's requests leave the owner's ten.
	c.requestPlan(map[string]any{"month": "2027-10"}, http.StatusAccepted, "user")
}
