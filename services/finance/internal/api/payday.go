package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/budget"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
)

// Payday (P13-10): "can we also plan budget when new income comes from [my
// employer]? that's my salary". The owner names the payer; a booked payment
// from them that a write creates or books asks for an AI budget plan
// (trigger salary) for the month the money is meant for. The salary comes
// at the end of a month, before the job's own plan on the 1st, which then
// answers with the salary's plan instead of making another.

const (
	// maxPayer bounds the payer text.
	maxPayer = 200
	// incomeLookbackDays is how far back the payday suggestion looks:
	// enough for the last three monthly payments.
	incomeLookbackDays = 120
	// salaryTrigger is the plans the salary asks for.
	salaryTrigger = string(generated.AiBudgetPlanTriggerSalary)
)

// paydayOf is the stored setting as the trigger reads it.
func paydayOf(row store.PaydaySetting) budget.Payday {
	payday := budget.Payday{Enabled: row.Enabled && row.PayerMatch != nil, Minimum: row.MinAmountMinor}
	if row.PayerMatch != nil {
		payday.Payer = *row.PayerMatch
	}
	return payday
}

// loadPayday reads the setting: off when none is stored.
func loadPayday(ctx context.Context, q *store.Queries) (store.PaydaySetting, error) {
	row, err := q.GetPaydaySettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.PaydaySetting{}, nil
	}
	return row, err
}

func (h *Handler) paydayView(ctx context.Context, row store.PaydaySetting) (generated.PaydaySettings, error) {
	currency := h.defaultCurrency
	view := generated.PaydaySettings{Enabled: row.Enabled, PayerMatch: row.PayerMatch, Currency: currency, UpdatedAt: row.UpdatedAt.Time.UTC()}
	if row.MinAmountMinor != nil {
		view.MinAmount = pointer(money(*row.MinAmountMinor, currency))
	}
	suggestion, err := h.paydaySuggestion(ctx)
	view.Suggestion = suggestion
	return view, err
}

// paydaySuggestion is the largest income that arrives monthly in the
// default currency, or nil (see subscriptions.Income).
func (h *Handler) paydaySuggestion(ctx context.Context) (*generated.PaydaySuggestion, error) {
	today := h.ownerDay(h.now())
	rows, err := h.queries.ListIncomePayments(ctx, store.ListIncomePaymentsParams{Currency: h.defaultCurrency, Since: budget.Date(today.AddDate(0, 0, -incomeLookbackDays))})
	if err != nil {
		return nil, err
	}
	payments := make([]subscriptions.Payment, 0, len(rows))
	for _, row := range rows {
		payments = append(payments, subscriptions.Payment{ID: row.ID.Bytes, AccountID: row.AccountID.Bytes, Amount: row.AmountMinor, Currency: row.Currency,
			Date: row.OccurredOn.Time, Counterparty: deref(row.Counterparty), Description: deref(row.Description)})
	}
	for _, income := range subscriptions.Income(payments, today, financemoney.Exponent) {
		if payer := subscriptions.PayerText(income); payer != "" {
			return &generated.PaydaySuggestion{PayerMatch: payer, TypicalAmount: money(income.Amount, income.Currency), Payments: len(income.Payments),
				LastDate: types.Date{Time: income.Last}}, nil
		}
	}
	return nil, nil
}

func (h *Handler) GetPaydaySettings(c fiber.Ctx) error {
	ctx := c.Context()
	row, err := loadPayday(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	view, err := h.paydayView(ctx, row)
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

func (h *Handler) UpdatePaydaySettings(c fiber.Ctx) error {
	var body generated.UpdatePaydaySettingsRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	if _, ok := properties["enabled"]; !ok || isNull(properties, "enabled") {
		return invalid("enabled is required")
	}
	payer, err := cleanText("payerMatch", body.PayerMatch, maxPayer)
	if err != nil {
		return err
	}
	switch {
	case body.Enabled && payer == nil:
		return invalid("payerMatch is required to plan when the salary arrives")
	case body.MinAmountMinor != nil && *body.MinAmountMinor < 1:
		return invalid("minAmountMinor must be at least 1")
	}
	ctx := c.Context()
	before, err := loadPayday(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	row := before
	changes := paydayChanges(before, body.Enabled, payer, body.MinAmountMinor)
	if len(changes) > 0 {
		row, err = h.queries.SavePaydaySettings(ctx, store.SavePaydaySettingsParams{Enabled: body.Enabled, PayerMatch: payer, MinAmountMinor: body.MinAmountMinor})
		if err != nil {
			return internal(err)
		}
		h.recordPayday(c, row, changes)
	}
	view, err := h.paydayView(ctx, row)
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

func paydayChanges(before store.PaydaySetting, enabled bool, payer *string, minimum *int64) []string {
	var changes []string
	if before.Enabled != enabled {
		changes = append(changes, "enabled")
	}
	if !sameText(before.PayerMatch, payer) {
		changes = append(changes, "payerMatch")
	}
	if !equalPointer(before.MinAmountMinor, minimum) {
		changes = append(changes, "minAmount")
	}
	return changes
}

// recordPayday: "Payday updated: plan the budget when money from “ACME
// Robotics GmbH” of at least €1,000.00 arrives", or "… (off)".
func (h *Handler) recordPayday(c fiber.Ctx, row store.PaydaySetting, changes []string) {
	summary := "Payday updated: no payer"
	data := map[string]any{"changes": changes, "enabled": row.Enabled, "currency": h.defaultCurrency}
	if row.PayerMatch != nil {
		summary = fmt.Sprintf("Payday updated: plan the budget when money from “%s” ", activity.Preview(*row.PayerMatch, 120))
		if row.MinAmountMinor != nil {
			summary += "of at least " + financemoney.Format(*row.MinAmountMinor, h.defaultCurrency) + " "
		}
		summary += "arrives"
		data["payerMatch"] = activity.Preview(*row.PayerMatch, maxPayer)
	}
	if row.MinAmountMinor != nil {
		data["minAmountMinor"] = *row.MinAmountMinor
	}
	if !row.Enabled {
		summary += " (off)"
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(settingsUpdated, "payday", version(row.UpdatedAt.Time)), Type: settingsUpdated,
		Subject: activity.Subject{Type: "finance_settings", ID: "payday"}, Summary: summary, Data: data})
}

// planOnSalary asks for an AI budget plan when items, the transactions a
// write just created or booked (in its database transaction), hold the
// salary: of the booked payments in the default currency the payday
// setting recognizes, the latest. A payment booked more than
// budget.SalaryRecentDays ago (a bank's history read) asks for nothing,
// and neither does one for a month already past or after the next (an
// entry dated ahead). The plan is for the month
// the salary is meant for (budget.SalaryMonth); none is asked for when the
// payment asked before, or the month has a plan that is waiting, running,
// ready, or applied (a failed or dismissed one does not count). It returns
// the plan it asked for, if any, for Activity after the commit.
func (h *Handler) planOnSalary(ctx context.Context, q *store.Queries, items []store.Transaction) (*store.AiBudgetPlan, error) {
	var candidates []store.Transaction
	for _, item := range items {
		if h.mayBeSalary(item) {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	setting, err := loadPayday(ctx, q)
	if err != nil {
		return nil, err
	}
	payday := paydayOf(setting)
	var salary *store.Transaction
	for index, item := range candidates {
		if payday.Pays(*item.Counterparty, item.AmountMinor) && (salary == nil || !item.OccurredOn.Time.Before(salary.OccurredOn.Time)) {
			salary = &candidates[index]
		}
	}
	if salary == nil {
		return nil, nil
	}
	now := h.now()
	month, current := budget.SalaryMonth(salary.OccurredOn.Time), budget.CurrentMonth(now, h.location)
	if !budget.SalaryRecent(salary.OccurredOn.Time, h.ownerDay(now)) || month.Before(current) || month.After(budget.AddMonths(current, 1)) {
		return nil, nil
	}
	if err := q.LockAIBudgetPlans(ctx); err != nil {
		return nil, err
	}
	if asked, err := q.AIBudgetPlanForSalaryExists(ctx, salary.ID); err != nil || asked {
		return nil, err
	}
	if planned, err := q.AIBudgetPlanForMonthExists(ctx, budget.Date(month)); err != nil || planned {
		return nil, err
	}
	row, err := q.CreateAIBudgetPlan(ctx, store.CreateAIBudgetPlanParams{Month: budget.Date(month), Currency: h.defaultCurrency, Trigger: salaryTrigger,
		RequestedByKind: "system", RequestedByName: pointer("finance"), SalaryTransactionID: salary.ID, SalaryBookedOn: salary.OccurredOn})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// mayBeSalary reports whether a transaction is booked money in, in the
// default currency, from a counterparty.
func (h *Handler) mayBeSalary(item store.Transaction) bool {
	return item.Status == "booked" && item.AmountMinor > 0 && item.Currency == h.defaultCurrency && item.Counterparty != nil
}

// salaryPlanFor runs planOnSalary in its own database transaction, after a
// write of one transaction that had none.
func (h *Handler) salaryPlanFor(ctx context.Context, item store.Transaction) (*store.AiBudgetPlan, error) {
	if !h.mayBeSalary(item) {
		return nil, nil
	}
	var plan *store.AiBudgetPlan
	err := h.inTx(ctx, func(q *store.Queries) error {
		var err error
		plan, err = h.planOnSalary(ctx, q, []store.Transaction{item})
		return err
	})
	return plan, err
}

// recordSalaryPlan records the plan the salary asked for, if any.
func (h *Handler) recordSalaryPlan(c fiber.Ctx, plan *store.AiBudgetPlan) {
	if plan != nil {
		h.recordPlanRequested(c, *plan)
	}
}
