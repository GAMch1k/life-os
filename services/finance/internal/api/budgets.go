package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/budget"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// EnsureBudgetDefaults adds the default categories and merchant rules once
// (see budget.Seed). It is idempotent and safe to run concurrently; a run
// that added anything is one Activity event of the service's own.
func (h *Handler) EnsureBudgetDefaults(ctx context.Context) (budget.SeedResult, error) {
	var result budget.SeedResult
	err := h.inTx(ctx, func(q *store.Queries) error {
		var err error
		result, err = budget.Seed(ctx, q)
		return err
	})
	if err != nil {
		return result, err
	}
	if result.Applied && result.CategoriesCreated+result.CategoriesAdopted+result.RulesCreated > 0 {
		h.recordBudgetDefaults(ctx, result)
	}
	return result, nil
}

// budgetChanged asks the budget alerts for a check soon, and the money
// alerts too: every money change reaches both (a plan change only matters
// to the budget, and an extra check is harmless).
func (h *Handler) budgetChanged() {
	if h.onBudgetChange != nil {
		h.onBudgetChange()
	}
	h.moneyChanged()
}

func parseBudgetMonth(month generated.Month) (time.Time, error) {
	parsed, err := budget.ParseMonth(string(month))
	if err != nil {
		return time.Time{}, invalid(err.Error())
	}
	return parsed, nil
}

func (h *Handler) GetBudgetMonth(c fiber.Ctx, month generated.Month) error {
	parsed, err := parseBudgetMonth(month)
	if err != nil {
		return err
	}
	view, err := h.budgetView(c.Context(), h.queries, parsed)
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

// budgetView is a month's budget as the API shows it.
func (h *Handler) budgetView(ctx context.Context, q *store.Queries, month time.Time) (generated.BudgetMonth, error) {
	computed, err := budget.Load(ctx, q, month, h.defaultCurrency)
	if err != nil {
		return generated.BudgetMonth{}, err
	}
	currency := computed.Currency
	excluded, err := q.ListExcludedCurrencies(ctx, store.ListExcludedCurrenciesParams{Currency: currency, MonthStart: budget.Date(month)})
	if err != nil {
		return generated.BudgetMonth{}, err
	}
	view := generated.BudgetMonth{
		Month: budget.MonthKey(month), Currency: currency, Lines: make([]generated.BudgetLine, 0, len(computed.Lines)),
		Uncategorized: generated.BudgetUncategorized{Spent: money(computed.Uncategorized.Spent, currency), Pending: money(computed.Uncategorized.Pending, currency),
			TransactionCount: int(computed.Uncategorized.Count)},
		Totals: generated.BudgetTotals{Planned: money(computed.Planned, currency), Spent: money(computed.Spent, currency), Pending: money(computed.Pending, currency),
			Remaining: money(computed.Remaining(), currency), Percent: computed.Percent()},
		Income: generated.BudgetIncome{Actual: money(computed.Income, currency), Pending: money(computed.IncomePending, currency)},
		Savings: generated.BudgetSavings{MovedToSavings: money(computed.MovedToSavings, currency), Leftover: money(computed.Leftover(), currency),
			Saved: money(computed.Saved(), currency), Percent: computed.SavedPercent()},
		ExcludedCurrencies: make([]generated.ExcludedCurrency, 0, len(excluded)),
	}
	if plan := computed.Plan; plan != nil {
		source := generated.BudgetPlanSourceRegular
		if plan.Scope == budget.ScopeMonth {
			source = generated.BudgetPlanSourceMonth
		}
		view.Plan = &generated.BudgetPlan{Source: source, Since: budget.MonthKey(plan.Month), ExpectedIncome: money(plan.ExpectedIncome, currency),
			SavingsGoal: money(plan.SavingsGoal, currency), Unallocated: money(computed.Unallocated(), currency), UpdatedAt: plan.UpdatedAt}
	}
	for _, line := range computed.Lines {
		view.Lines = append(view.Lines, generated.BudgetLine{CategoryId: line.Category.ID, CategoryName: line.Category.Name, CategoryColor: line.Category.Color,
			Archived: line.Category.Archived, Planned: money(line.Planned, currency), Spent: money(line.Spent, currency), Pending: money(line.Pending, currency),
			Remaining: money(line.Remaining(), currency), Percent: line.Percent(), Status: generated.BudgetLineStatus(line.Status()), TransactionCount: int(line.Count)})
	}
	for _, row := range excluded {
		view.ExcludedCurrencies = append(view.ExcludedCurrencies, generated.ExcludedCurrency{Currency: row.Currency, TransactionCount: int(row.TransactionCount)})
	}
	return view, nil
}

// planChange is what a saved plan changed, for Activity.
type planChange struct {
	month          time.Time
	scope          string // the API's: month or following
	plan           store.BudgetPlan
	categories     int
	planned        int64
	replacedMonths int
}

func (h *Handler) SaveBudgetPlan(c fiber.Ctx, month generated.Month) error {
	parsed, err := parseBudgetMonth(month)
	if err != nil {
		return err
	}
	var body generated.SaveBudgetPlanRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	for _, name := range []string{"scope", "expectedIncomeMinor", "savingsGoalMinor", "lines"} {
		if _, ok := properties[name]; !ok || isNull(properties, name) {
			return invalid(name + " is required")
		}
	}
	switch body.Scope {
	case generated.BudgetScopeMonth, generated.BudgetScopeFollowing:
	default:
		return invalid("scope must be month or following")
	}
	if body.ExpectedIncomeMinor < 0 || body.SavingsGoalMinor < 0 {
		return invalid("expectedIncomeMinor and savingsGoalMinor must not be negative")
	}
	if len(body.Lines) > 500 {
		return invalid("a plan has at most 500 lines")
	}
	lines := map[[16]byte]int64{}
	for _, line := range body.Lines {
		if line.AmountMinor < 0 {
			return invalid("a line's amountMinor must not be negative")
		}
		id := [16]byte(line.CategoryId)
		if _, seen := lines[id]; seen {
			return invalid("category " + line.CategoryId.String() + " is named twice")
		}
		lines[id] = line.AmountMinor
	}
	ctx := c.Context()
	var (
		change  planChange
		changed bool
		view    generated.BudgetMonth
	)
	err = h.inTx(ctx, func(q *store.Queries) error {
		var err error
		change, changed, view, err = h.savePlan(ctx, q, parsed, body.Scope, body.ExpectedIncomeMinor, body.SavingsGoalMinor, lines)
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if changed {
		h.recordBudgetSaved(c, change)
		h.budgetChanged()
	}
	return c.JSON(view)
}

// savePlan saves a month's plan in q's database transaction: the budget's
// one write, for the plan editor and for an AI plan the owner applies.
// lines are amounts per expense category (0 leaves one out). It answers
// what changed (for Activity), whether anything did, and the month's
// budget.
func (h *Handler) savePlan(ctx context.Context, q *store.Queries, month time.Time, apiScope generated.BudgetScope, income, goal int64,
	lines map[[16]byte]int64) (change planChange, changed bool, view generated.BudgetMonth, err error) {
	scope := budget.ScopeMonth
	if apiScope == generated.BudgetScopeFollowing {
		scope = budget.ScopeRegular
	}
	lines = maps.Clone(lines)
	change = planChange{month: month, scope: string(apiScope)}
	if err = q.LockBudgetPlans(ctx); err != nil {
		return change, false, view, err
	}
	for id, amount := range lines {
		category, err := q.GetFinancialCategory(ctx, pgtype.UUID{Bytes: id, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return change, false, view, apiError(fiber.StatusBadRequest, "unknown_category", "categoryId "+googleuuid.UUID(id).String()+" is not a Finance category")
		}
		if err != nil {
			return change, false, view, err
		}
		if category.Kind != budget.KindExpense {
			return change, false, view, invalid("the category " + category.Name + " is not an expense category")
		}
		if amount == 0 {
			delete(lines, id)
		}
	}
	for _, amount := range lines {
		change.planned += amount
	}
	change.categories = len(lines)
	before, err := budgetPlanState(ctx, q, month, scope)
	if err != nil {
		return change, false, view, err
	}
	if scope == budget.ScopeRegular {
		removed, err := q.DeleteLaterBudgetPlans(ctx, budget.Date(month))
		if err != nil {
			return change, false, view, err
		}
		for _, row := range removed {
			if row.Scope == budget.ScopeMonth && !row.Month.Time.Equal(month) {
				change.replacedMonths++
			}
		}
		// The month's own plan gives way to the regular plan too.
		changed = len(removed) > 0
	}
	if before == nil || before.plan.Currency != h.defaultCurrency || before.plan.ExpectedIncomeMinor != income ||
		before.plan.SavingsGoalMinor != goal || !maps.Equal(before.lines, lines) {
		changed = true
	}
	if !changed {
		change.plan = before.plan
	} else {
		if change.plan, err = q.UpsertBudgetPlan(ctx, store.UpsertBudgetPlanParams{Month: budget.Date(month), Scope: scope, Currency: h.defaultCurrency,
			ExpectedIncomeMinor: income, SavingsGoalMinor: goal}); err != nil {
			return change, false, view, err
		}
		if err = q.DeleteBudgetPlanLines(ctx, change.plan.ID); err != nil {
			return change, false, view, err
		}
		insert := store.InsertBudgetPlanLinesParams{PlanID: change.plan.ID, CategoryIds: make([]pgtype.UUID, 0, len(lines)), Amounts: make([]int64, 0, len(lines))}
		for id, amount := range lines {
			insert.CategoryIds = append(insert.CategoryIds, pgtype.UUID{Bytes: id, Valid: true})
			insert.Amounts = append(insert.Amounts, amount)
		}
		if len(lines) > 0 {
			if err = q.InsertBudgetPlanLines(ctx, insert); err != nil {
				return change, false, view, err
			}
		}
	}
	view, err = h.budgetView(ctx, q, month)
	return change, changed, view, err
}

type planState struct {
	plan  store.BudgetPlan
	lines map[[16]byte]int64
}

// budgetPlanState is the stored plan of (month, scope) with its lines, or
// nil.
func budgetPlanState(ctx context.Context, q *store.Queries, month time.Time, scope string) (*planState, error) {
	plan, err := q.GetBudgetPlan(ctx, store.GetBudgetPlanParams{Month: budget.Date(month), Scope: scope})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBudgetPlanLines(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	state := &planState{plan: plan, lines: make(map[[16]byte]int64, len(rows))}
	for _, row := range rows {
		state.lines[row.CategoryID.Bytes] = row.AmountMinor
	}
	return state, nil
}

func (h *Handler) ResetBudgetMonth(c fiber.Ctx, month generated.Month) error {
	parsed, err := parseBudgetMonth(month)
	if err != nil {
		return err
	}
	removed, err := h.queries.DeleteMonthBudgetPlan(c.Context(), budget.Date(parsed))
	if errors.Is(err, pgx.ErrNoRows) {
		return c.SendStatus(fiber.StatusNoContent)
	}
	if err != nil {
		return internal(err)
	}
	h.recordBudgetReset(c, parsed, removed)
	h.budgetChanged()
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) GetBudgetSuggestions(c fiber.Ctx, month generated.Month) error {
	parsed, err := parseBudgetMonth(month)
	if err != nil {
		return err
	}
	ctx := c.Context()
	currency := h.defaultCurrency
	from, to := budget.SuggestionWindow(parsed, budget.CurrentMonth(h.now(), h.location))
	rows, err := h.queries.GetBudgetHistory(ctx, store.GetBudgetHistoryParams{Currency: currency, FromMonth: budget.Date(from), ToMonth: budget.Date(to)})
	if err != nil {
		return internal(err)
	}
	stored, err := h.queries.ListFinancialCategories(ctx, true)
	if err != nil {
		return internal(err)
	}
	history := make([]budget.HistoryRow, 0, len(rows))
	for _, row := range rows {
		item := budget.HistoryRow{Month: row.Month.Time, Out: row.OutMinor, In: row.InMinor}
		if row.CategoryID.Valid {
			id := row.CategoryID.Bytes
			item.CategoryID = &id
		}
		history = append(history, item)
	}
	categories := budget.Categories(stored)
	suggestions := budget.Suggest(categories, history, financemoney.Exponent(currency))
	result := generated.BudgetSuggestions{Currency: currency, Months: make([]string, 0, len(suggestions.Months)),
		Categories: make([]generated.BudgetCategorySuggestion, 0, len(suggestions.Categories))}
	for _, month := range suggestions.Months {
		result.Months = append(result.Months, budget.MonthKey(month))
	}
	optionalMoney := func(amount *int64) *generated.Money {
		if amount == nil {
			return nil
		}
		return pointer(money(*amount, currency))
	}
	result.Income, result.Saved = optionalMoney(suggestions.Income), optionalMoney(suggestions.Saved)
	// In the categories' order: by kind, then name.
	for _, category := range categories {
		suggestion, ok := suggestions.Categories[category.ID]
		if !ok {
			continue
		}
		result.Categories = append(result.Categories, generated.BudgetCategorySuggestion{CategoryId: category.ID,
			Suggested: optionalMoney(suggestion.Amount), MonthsWithSpending: suggestion.MonthsWithSpending})
	}
	return c.JSON(result)
}

func (h *Handler) recordBudgetSaved(c fiber.Ctx, change planChange) {
	monthKey := budget.MonthKey(change.month)
	label := budget.MonthLabel(change.month)
	currency := change.plan.Currency
	format := func(amount int64) string { return financemoney.Format(amount, currency) }
	summary := fmt.Sprintf("Budget for %s saved", label)
	if change.scope == string(generated.BudgetScopeFollowing) {
		summary = fmt.Sprintf("Regular budget from %s on saved", label)
	}
	summary += fmt.Sprintf(": %d %s, %s planned, %s savings goal", change.categories, categoryWord(change.categories),
		format(change.planned), format(change.plan.SavingsGoalMinor))
	if change.replacedMonths > 0 {
		summary += fmt.Sprintf("; %d later %s own budget replaced", change.replacedMonths, map[bool]string{true: "month's", false: "months'"}[change.replacedMonths == 1])
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(budgetSaved, monthKey, change.scope, version(change.plan.UpdatedAt.Time)), Type: budgetSaved,
		Subject: activity.Subject{Type: "finance_budget", ID: monthKey}, Summary: summary,
		Data: map[string]any{"month": monthKey, "scope": change.scope, "categories": change.categories, "planned": change.planned,
			"expectedIncome": change.plan.ExpectedIncomeMinor, "savingsGoal": change.plan.SavingsGoalMinor, "currency": currency,
			"replacedMonths": change.replacedMonths}})
}

func (h *Handler) recordBudgetReset(c fiber.Ctx, month time.Time, removed store.BudgetPlan) {
	monthKey := budget.MonthKey(month)
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(budgetReset, monthKey, version(removed.UpdatedAt.Time)), Type: budgetReset,
		Subject: activity.Subject{Type: "finance_budget", ID: monthKey}, Summary: fmt.Sprintf("Budget for %s returned to the regular plan", budget.MonthLabel(month)),
		Data: map[string]any{"month": monthKey}})
}

func (h *Handler) recordBudgetDefaults(ctx context.Context, result budget.SeedResult) {
	summary := fmt.Sprintf("Default budget categories added: %d created", result.CategoriesCreated)
	if result.CategoriesAdopted > 0 {
		summary += fmt.Sprintf(", %d existing %s used", result.CategoriesAdopted, categoryWord(result.CategoriesAdopted))
	}
	summary += fmt.Sprintf("; %d merchant %s added", result.RulesCreated, plural(result.RulesCreated, "rule"))
	h.emit(ctx, nil, activity.Event{ID: activity.EventID(budgetDefaultsAdded, budget.DefaultsSeed), Type: budgetDefaultsAdded,
		Subject: activity.Subject{Type: "finance_category_defaults", ID: budget.DefaultsSeed}, Summary: summary,
		Data: map[string]any{"categoriesCreated": result.CategoriesCreated, "categoriesAdopted": result.CategoriesAdopted,
			"categoriesSkipped": result.CategoriesSkipped, "rulesCreated": result.RulesCreated, "rulesSkipped": result.RulesSkipped}})
}

func categoryWord(count int) string {
	if count == 1 {
		return "category"
	}
	return "categories"
}
