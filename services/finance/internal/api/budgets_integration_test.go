package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/budget"
)

// categoryByKey finds the category with a system key.
func (c apiClient) categoryByKey(key string) (generated.Category, bool) {
	c.t.Helper()
	for _, category := range expect[generated.CategoryList](c, http.MethodGet, "/v1/categories?includeArchived=true", nil, http.StatusOK).Items {
		if category.SystemKey != nil && string(*category.SystemKey) == key {
			return category, true
		}
	}
	return generated.Category{}, false
}

func (c apiClient) seedDefaults() budget.SeedResult {
	c.t.Helper()
	result, err := c.handler.EnsureBudgetDefaults(c.t.Context())
	if err != nil {
		c.t.Fatal(err)
	}
	return result
}

func TestBudgetDefaultsAreAddedOnceAndLeaveTheOwnersCategoriesAlone(t *testing.T) {
	c := newAPIClient(t)
	// The owner already has "groceries" (an expense, with a color and a
	// rule) and "Savings" as an expense category.
	groceries := expect[generated.Category](c, http.MethodPost, "/v1/categories", map[string]any{"name": "groceries", "kind": "expense", "color": "#123456"}, http.StatusCreated)
	ownSavings := c.category("Savings", "expense")
	ownRule := c.rule(groceries.Id.String(), "counterparty", "contains", "rewe")
	spent := c.spend(map[string]any{"amount": eur(-500), "counterparty": "Kiosk", "categoryId": ownSavings.Id})
	seen := len(c.recorder.all())

	result := c.seedDefaults()
	if !result.Applied || result.CategoriesAdopted != 1 || result.CategoriesSkipped != 1 || result.CategoriesCreated != len(budget.DefaultCategories)-2 {
		t.Fatalf("seed = %+v", result)
	}
	// The owner's groceries became the default one, unchanged otherwise.
	adopted, ok := c.categoryByKey("groceries")
	if !ok || adopted.Id != groceries.Id || adopted.Name != "groceries" || *adopted.Color != "#123456" {
		t.Fatalf("groceries = %+v", adopted)
	}
	// "Savings" exists as an expense: no default Savings is added.
	if _, ok := c.categoryByKey(budget.KeySavings); ok {
		t.Fatal("a second Savings category was added")
	}
	if got := expect[generated.Transaction](c, http.MethodGet, "/v1/transactions/"+spent.Id.String(), nil, http.StatusOK); *got.CategoryId != ownSavings.Id {
		t.Fatalf("the owner's assignment changed: %+v", got)
	}
	transport, ok := c.categoryByKey("transport")
	if !ok || transport.Name != "Transport" || transport.Kind != generated.CategoryKindExpense || *transport.Color != "#3b82f6" {
		t.Fatalf("transport = %+v", transport)
	}
	if income, ok := c.categoryByKey("income"); !ok || income.Kind != generated.CategoryKindIncome {
		t.Fatalf("income = %+v", income)
	}
	// Default rules come after the owner's; REWE was already a rule.
	rules := expect[generated.CategoryRuleList](c, http.MethodGet, "/v1/categories/rules", nil, http.StatusOK).Items
	if len(rules) != 1+result.RulesCreated || rules[0].Id != ownRule.Id || result.RulesCreated != len(budget.DefaultRules)-1 {
		t.Fatalf("rules: %d, created %d", len(rules), result.RulesCreated)
	}
	for _, rule := range rules[1:] {
		if rule.Priority < budget.DefaultRulePriority || strings.EqualFold(rule.Match.Text, "rewe") {
			t.Fatalf("default rule = %+v", rule)
		}
	}
	// A rule the owner adds later still comes first.
	if later := c.rule(transport.Id.String(), "counterparty", "contains", "Taxi"); later.Priority != ownRule.Priority+10 {
		t.Fatalf("later rule priority = %d", later.Priority)
	}
	if got := c.spend(map[string]any{"amount": eur(-1500), "counterparty": "Aral Station 12"}); *got.CategoryId != transport.Id || *got.CategorizedBy != generated.TransactionCategorizedByRule {
		t.Fatalf("a default rule did not categorize: %+v", got)
	}
	if got := c.spend(map[string]any{"amount": eur(-350), "counterparty": "Espresso Bar"}); got.CategoryId != nil {
		t.Fatalf("Esso matched an espresso bar: %+v", got)
	}
	events := c.recorder.all()[seen:]
	if events[0].Type != budgetDefaultsAdded || events[0].Actor != activity.System("finance") ||
		events[0].Summary != "Default budget categories added: 11 created, 1 existing category used; 30 merchant rules added" {
		t.Fatalf("defaults event = %+v", events[0])
	}

	// Renaming keeps the key; running again adds nothing.
	expect[generated.Category](c, http.MethodPatch, "/v1/categories/"+transport.Id.String(), map[string]any{"name": "Getting around"}, http.StatusOK)
	if again := c.seedDefaults(); again.Applied {
		t.Fatalf("second seed = %+v", again)
	}
	if renamed, _ := c.categoryByKey("transport"); renamed.Id != transport.Id || renamed.Name != "Getting around" {
		t.Fatalf("renamed = %+v", renamed)
	}
}

func TestMerchantCategoryCodesCategorizeBankTransactionsAfterTheRules(t *testing.T) {
	c := newAPIClient(t)
	// A sync before the defaults exist leaves a card payment uncategorized.
	c.syncBank(bankSync([]map[string]any{giroInput(0, 0)}, []map[string]any{
		bankTx("acc-1", "early", "booked", "2026-10-01", -1200, "Markthalle", "merchantCategoryCode", "5411"),
	}))
	if early := c.byExternalID("early"); early.CategoryId != nil {
		t.Fatalf("early = %+v", early)
	}
	c.seedDefaults()
	groceries, _ := c.categoryByKey("groceries")
	dining, _ := c.categoryByKey("eating_out")
	shopping, _ := c.categoryByKey("shopping")
	// Apply rules gives it the code's category.
	applied := expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", nil, http.StatusOK)
	if applied.Categorized != 1 {
		t.Fatalf("applied = %+v", applied)
	}
	if early := c.byExternalID("early"); *early.CategoryId != groceries.Id || *early.CategorizedBy != generated.TransactionCategorizedByMcc || early.CategoryRuleId != nil {
		t.Fatalf("early after apply = %+v", early)
	}

	c.syncBank(bankSync([]map[string]any{giroInput(0, 0)}, []map[string]any{
		bankTx("acc-1", "early", "booked", "2026-10-01", -1200, "Markthalle", "merchantCategoryCode", "5411"),
		bankTx("acc-1", "cafe", "booked", "2026-10-02", -800, "Café Rosa", "merchantCategoryCode", "5814"),
		// A rule wins over the code.
		bankTx("acc-1", "rewe", "booked", "2026-10-03", -2300, "REWE Markt", "merchantCategoryCode", "5814"),
		bankTx("acc-1", "atm", "booked", "2026-10-04", -5000, "Geldautomat", "merchantCategoryCode", "6011"),
		bankTx("acc-1", "store", "pending", "2026-10-05", -4000, "Kaufhaus", "merchantCategoryCode", "5311"),
	}))
	cafe := c.byExternalID("cafe")
	if *cafe.CategoryId != dining.Id || *cafe.CategorizedBy != generated.TransactionCategorizedByMcc {
		t.Fatalf("cafe = %+v", cafe)
	}
	if rewe := c.byExternalID("rewe"); *rewe.CategoryId != groceries.Id || *rewe.CategorizedBy != generated.TransactionCategorizedByRule {
		t.Fatalf("rewe = %+v", rewe)
	}
	if atm := c.byExternalID("atm"); atm.CategoryId != nil {
		t.Fatalf("atm = %+v", atm)
	}
	if store := c.byExternalID("store"); *store.CategoryId != shopping.Id {
		t.Fatalf("store = %+v", store)
	}
	// The owner picks another category for the café: re-syncs and apply
	// keep it, while a changed code moves the code's own choice.
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+cafe.Id.String(), map[string]any{"categoryId": shopping.Id}, http.StatusOK)
	c.syncBank(bankSync([]map[string]any{giroInput(0, 0)}, []map[string]any{
		bankTx("acc-1", "cafe", "booked", "2026-10-02", -800, "Café Rosa", "merchantCategoryCode", "5411"),
		bankTx("acc-1", "store", "booked", "2026-10-05", -4000, "Kaufhaus", "merchantCategoryCode", "5812"),
	}))
	if got := c.byExternalID("cafe"); *got.CategoryId != shopping.Id || *got.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("cafe after re-sync = %+v", got)
	}
	if got := c.byExternalID("store"); *got.CategoryId != dining.Id || *got.CategorizedBy != generated.TransactionCategorizedByMcc {
		t.Fatalf("store after re-sync = %+v", got)
	}
	// Archiving the default category turns its code off: apply with scope
	// all clears what the code chose.
	expect[generated.Category](c, http.MethodPatch, "/v1/categories/"+dining.Id.String(), map[string]any{"archived": true}, http.StatusOK)
	applied = expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", map[string]any{"scope": "all"}, http.StatusOK)
	if applied.Cleared != 1 {
		t.Fatalf("applied all = %+v", applied)
	}
}

func TestBudgetPlansRollForwardAndMonthsKeepTheirOwn(t *testing.T) {
	c := newAPIClient(t)
	groceries := c.category("Groceries", "expense")
	dining := c.category("Eating out", "expense")
	salary := c.category("Salary", "income")
	plan := func(scope string, groceriesMinor, diningMinor int64) map[string]any {
		return map[string]any{"scope": scope, "expectedIncomeMinor": 300000, "savingsGoalMinor": 40000, "lines": []map[string]any{
			{"categoryId": groceries.Id, "amountMinor": groceriesMinor}, {"categoryId": dining.Id, "amountMinor": diningMinor}}}
	}
	get := func(month string) generated.BudgetMonth {
		return expect[generated.BudgetMonth](c, http.MethodGet, "/v1/budgets/"+month, nil, http.StatusOK)
	}
	planned := func(view generated.BudgetMonth, id string) int64 {
		for _, line := range view.Lines {
			if line.CategoryId.String() == id {
				return line.Planned.Amount
			}
		}
		return -1
	}
	wakes := 0
	c.handler.onBudgetChange = func() { wakes++ }
	if none := get("2026-10"); none.Plan != nil || none.Currency != "EUR" || len(none.Lines) != 0 || none.Totals.Percent != nil {
		t.Fatalf("no plan = %+v", none)
	}
	seen := len(c.recorder.all())

	october := expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2026-10", plan("following", 30000, 10000), http.StatusOK)
	if october.Plan == nil || october.Plan.Source != generated.BudgetPlanSourceRegular || october.Plan.Since != "2026-10" ||
		october.Plan.ExpectedIncome.Amount != 300000 || october.Plan.SavingsGoal.Amount != 40000 || october.Plan.Unallocated.Amount != 300000-40000-40000 ||
		october.Totals.Planned.Amount != 40000 || len(october.Lines) != 2 || october.Lines[0].CategoryName != "Eating out" {
		t.Fatalf("october = %+v", october)
	}
	events := c.recorder.all()[seen:]
	if len(events) != 1 || events[0].Type != budgetSaved || events[0].Subject != (activity.Subject{Type: "finance_budget", ID: "2026-10"}) ||
		events[0].Summary != "Regular budget from October 2026 on saved: 2 categories, €400.00 planned, €400.00 savings goal" {
		t.Fatalf("saved event = %+v", events)
	}
	// Saving the same plan again changes and records nothing.
	expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2026-10", plan("following", 30000, 10000), http.StatusOK)
	if len(c.recorder.all()) != seen+1 || wakes != 1 {
		t.Fatalf("a no-op save was recorded (%d alert checks asked)", wakes)
	}
	// Money changes ask the budget alerts for a check too.
	c.spend(map[string]any{"amount": eur(-100), "categoryId": groceries.Id})
	if wakes != 2 {
		t.Fatalf("alert checks asked = %d", wakes)
	}

	// The regular plan rolls forward, not back.
	if november := get("2026-11"); november.Plan == nil || november.Plan.Since != "2026-10" || planned(november, groceries.Id.String()) != 30000 {
		t.Fatalf("november = %+v", november)
	}
	if september := get("2026-09"); september.Plan != nil {
		t.Fatalf("september = %+v", september)
	}
	// December gets its own plan; a 0 leaves eating out out of it.
	december := expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2026-12", plan("month", 50000, 0), http.StatusOK)
	if december.Plan.Source != generated.BudgetPlanSourceMonth || december.Plan.Since != "2026-12" || planned(december, dining.Id.String()) != -1 || december.Totals.Planned.Amount != 50000 {
		t.Fatalf("december = %+v", december)
	}
	if january := get("2027-01"); january.Plan.Source != generated.BudgetPlanSourceRegular || planned(january, groceries.Id.String()) != 30000 {
		t.Fatalf("january = %+v", january)
	}
	// From November on: November and later follow, December's own plan
	// gives way; October keeps the earlier regular plan.
	seen = len(c.recorder.all())
	expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2026-11", plan("following", 35000, 10000), http.StatusOK)
	if events := c.recorder.all()[seen:]; len(events) != 1 || !strings.HasSuffix(events[0].Summary, "; 1 later month's own budget replaced") || events[0].Data["replacedMonths"] != float64(1) {
		t.Fatalf("following event = %+v", events)
	}
	if december = get("2026-12"); december.Plan.Source != generated.BudgetPlanSourceRegular || december.Plan.Since != "2026-11" || planned(december, groceries.Id.String()) != 35000 {
		t.Fatalf("december after = %+v", december)
	}
	if october = get("2026-10"); october.Plan.Since != "2026-10" || planned(october, groceries.Id.String()) != 30000 {
		t.Fatalf("october after = %+v", october)
	}

	// A month's own plan returns to the regular one.
	expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2027-02", plan("month", 1000, 1000), http.StatusOK)
	seen = len(c.recorder.all())
	for range 2 {
		if status, raw := c.do(http.MethodDelete, "/v1/budgets/2027-02", nil); status != http.StatusNoContent {
			t.Fatalf("reset: %d %s", status, raw)
		}
	}
	if events := c.recorder.all()[seen:]; len(events) != 1 || events[0].Type != budgetReset || events[0].Summary != "Budget for February 2027 returned to the regular plan" {
		t.Fatalf("reset events = %+v", events)
	}
	if february := get("2027-02"); february.Plan.Source != generated.BudgetPlanSourceRegular || planned(february, groceries.Id.String()) != 35000 {
		t.Fatalf("february = %+v", february)
	}

	for _, bad := range []map[string]any{
		{"scope": "always", "expectedIncomeMinor": 0, "savingsGoalMinor": 0, "lines": []any{}},
		{"expectedIncomeMinor": 0, "savingsGoalMinor": 0, "lines": []any{}},
		{"scope": "month", "expectedIncomeMinor": -1, "savingsGoalMinor": 0, "lines": []any{}},
		{"scope": "month", "expectedIncomeMinor": 0, "savingsGoalMinor": 0},
		{"scope": "month", "expectedIncomeMinor": 0, "savingsGoalMinor": 0, "lines": []map[string]any{{"categoryId": groceries.Id, "amountMinor": -5}}},
		{"scope": "month", "expectedIncomeMinor": 0, "savingsGoalMinor": 0, "lines": []map[string]any{{"categoryId": groceries.Id, "amountMinor": 5}, {"categoryId": groceries.Id, "amountMinor": 6}}},
		{"scope": "month", "expectedIncomeMinor": 0, "savingsGoalMinor": 0, "lines": []map[string]any{{"categoryId": salary.Id, "amountMinor": 5}}},
	} {
		c.expectError(http.MethodPut, "/v1/budgets/2026-10", bad, http.StatusBadRequest, "invalid_request")
	}
	c.expectError(http.MethodPut, "/v1/budgets/2026-10", map[string]any{"scope": "month", "expectedIncomeMinor": 0, "savingsGoalMinor": 0,
		"lines": []map[string]any{{"categoryId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", "amountMinor": 5}}}, http.StatusBadRequest, "unknown_category")
	for _, month := range []string{"2026-13", "202610", "1999-12"} {
		c.expectError(http.MethodGet, "/v1/budgets/"+month, nil, http.StatusBadRequest, "invalid_request")
	}
}

func TestBudgetProgressCountsBookedAndPendingSpending(t *testing.T) {
	c := newAPIClient(t)
	c.seedDefaults()
	groceries, _ := c.categoryByKey("groceries")
	dining, _ := c.categoryByKey("eating_out")
	shopping, _ := c.categoryByKey("shopping")
	income, _ := c.categoryByKey("income")
	savings, _ := c.categoryByKey(budget.KeySavings)
	giro := c.account("Giro", "bank", "EUR", 0)
	tagesgeld := c.account("Tagesgeld", "savings", "EUR", 0)
	dollars := c.account("Dollars", "other", "USD", 0)
	expect[generated.BudgetMonth](c, http.MethodPut, "/v1/budgets/2026-10", map[string]any{"scope": "following", "expectedIncomeMinor": 300000, "savingsGoalMinor": 40000,
		"lines": []map[string]any{{"categoryId": groceries.Id, "amountMinor": 30000}, {"categoryId": dining.Id, "amountMinor": 10000}}}, http.StatusOK)
	spend := func(account generated.Account, amount int64, currency, date, status string, category *generated.Category) {
		body := map[string]any{"accountId": account.Id, "amount": map[string]any{"amount": amount, "currency": currency}, "bookingDate": date, "status": status}
		if category != nil {
			body["categoryId"] = category.Id
		}
		c.spend(body)
	}
	spend(giro, -25000, "EUR", "2026-10-02", "booked", &groceries)
	spend(giro, -2000, "EUR", "2026-10-09", "pending", &groceries)
	spend(giro, 1000, "EUR", "2026-10-04", "booked", &groceries) // a refund
	spend(giro, -12000, "EUR", "2026-10-05", "booked", &dining)
	spend(giro, -5000, "EUR", "2026-10-06", "booked", &shopping)
	spend(giro, -3500, "EUR", "2026-10-07", "booked", nil)
	spend(giro, 300000, "EUR", "2026-10-01", "booked", &income)
	// 500 moved to the savings account: out of the giro, into Tagesgeld.
	spend(giro, -50000, "EUR", "2026-10-03", "booked", &savings)
	spend(tagesgeld, 50000, "EUR", "2026-10-03", "booked", &savings)
	spend(dollars, -999, "USD", "2026-10-03", "booked", &groceries)
	spend(giro, -77700, "EUR", "2026-09-30", "booked", &groceries) // another month

	view := expect[generated.BudgetMonth](c, http.MethodGet, "/v1/budgets/2026-10", nil, http.StatusOK)
	if len(view.Lines) != 3 {
		t.Fatalf("lines = %+v", view.Lines)
	}
	dine, groc, shop := view.Lines[0], view.Lines[1], view.Lines[2]
	if groc.CategoryId != groceries.Id || groc.Spent.Amount != 26000 || groc.Pending.Amount != 2000 || groc.Remaining.Amount != 4000 ||
		*groc.Percent != 86 || groc.Status != generated.Near || groc.TransactionCount != 3 {
		t.Fatalf("groceries = %+v", groc)
	}
	if dine.Status != generated.Over || dine.Remaining.Amount != -2000 || *dine.Percent != 120 {
		t.Fatalf("eating out = %+v", dine)
	}
	if shop.CategoryId != shopping.Id || shop.Status != generated.Unplanned || shop.Planned.Amount != 0 || shop.Percent != nil {
		t.Fatalf("shopping = %+v", shop)
	}
	if view.Uncategorized.Spent.Amount != 3500 || view.Uncategorized.TransactionCount != 1 {
		t.Fatalf("uncategorized = %+v", view.Uncategorized)
	}
	if view.Totals.Planned.Amount != 40000 || view.Totals.Spent.Amount != 46500 || view.Totals.Pending.Amount != 2000 || view.Totals.Remaining.Amount != -6500 || *view.Totals.Percent != 116 {
		t.Fatalf("totals = %+v", view.Totals)
	}
	if view.Income.Actual.Amount != 300000 {
		t.Fatalf("income = %+v", view.Income)
	}
	if view.Savings.MovedToSavings.Amount != 50000 || view.Savings.Saved.Amount != 253500 || view.Savings.Leftover.Amount != 203500 || *view.Savings.Percent != 633 {
		t.Fatalf("savings = %+v", view.Savings)
	}
	if len(view.ExcludedCurrencies) != 1 || view.ExcludedCurrencies[0].Currency != "USD" || view.ExcludedCurrencies[0].TransactionCount != 1 {
		t.Fatalf("excluded = %+v", view.ExcludedCurrencies)
	}
}

func TestBudgetSuggestionsUseTheLastThreeCompleteMonths(t *testing.T) {
	c := newAPIClient(t)
	groceries := c.category("Groceries", "expense")
	rent := c.category("Rent", "expense")
	salary := c.category("Salary", "income")
	cash := c.cash()
	spend := func(amount int64, date, status string, category generated.Category) {
		c.spend(map[string]any{"accountId": cash.Id, "amount": eur(amount), "bookingDate": date, "status": status, "categoryId": category.Id})
	}
	none := expect[generated.BudgetSuggestions](c, http.MethodGet, "/v1/budgets/2026-10/suggestions", nil, http.StatusOK)
	if len(none.Months) != 0 || none.Income != nil || len(none.Categories) != 2 || none.Categories[0].Suggested != nil {
		t.Fatalf("no history = %+v", none)
	}
	spend(-99900, "2026-06-15", "booked", groceries) // before the window
	spend(-30050, "2026-07-03", "booked", groceries)
	spend(-28000, "2026-08-03", "booked", groceries)
	spend(-41000, "2026-09-03", "booked", groceries)
	spend(-50000, "2026-09-20", "pending", groceries) // pending: not history
	spend(-77700, "2026-10-03", "booked", groceries)  // the current month is not complete
	spend(300000, "2026-07-01", "booked", salary)
	spend(310000, "2026-08-01", "booked", salary)
	spend(290000, "2026-09-01", "booked", salary)

	got := expect[generated.BudgetSuggestions](c, http.MethodGet, "/v1/budgets/2026-10/suggestions", nil, http.StatusOK)
	if strings.Join(got.Months, ",") != "2026-07,2026-08,2026-09" || got.Currency != "EUR" || got.Income.Amount != 300000 {
		t.Fatalf("suggestions = %+v", got)
	}
	byID := map[string]generated.BudgetCategorySuggestion{}
	for _, item := range got.Categories {
		byID[item.CategoryId.String()] = item
	}
	if g := byID[groceries.Id.String()]; g.Suggested == nil || g.Suggested.Amount != 30100 || g.MonthsWithSpending != 3 {
		t.Fatalf("groceries = %+v", g)
	}
	if r := byID[rent.Id.String()]; r.Suggested != nil || r.MonthsWithSpending != 0 {
		t.Fatalf("rent = %+v", r)
	}
	// A later month looks at the same complete months.
	if later := expect[generated.BudgetSuggestions](c, http.MethodGet, "/v1/budgets/2027-03/suggestions", nil, http.StatusOK); strings.Join(later.Months, ",") != "2026-07,2026-08,2026-09" {
		t.Fatalf("later = %+v", later.Months)
	}
}
