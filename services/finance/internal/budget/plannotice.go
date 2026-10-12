package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/notify"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// PlanReadyCategory is the notification of an AI budget plan that is ready
// (P13-10).
const PlanReadyCategory = "finance.ai_budget_plan.ready"

// PlanReadyKey is a ready plan's dedupe key:
// finance.ai_budget_plan.ready:<planId>.
func PlanReadyKey(planID [16]byte) string { return PlanReadyCategory + ":" + uuidText(planID) }

// readyPlans notifies each plan the AI made without the owner asking (the
// salary's and the schedule's) once it is ready, at medium importance: the
// owner is not waiting for it. A plan the owner asked for is not notified:
// they asked on the Budget tab, where the card follows it. A plan ready for
// more than PlanNoticeDays (Finance or Notifications was down that long),
// or applied or dismissed before the check, is left out.
func (a *Alerts) readyPlans(ctx context.Context, result *AlertResult, now time.Time) error {
	rows, err := a.queries.ListAIBudgetPlansToNotify(ctx, pgtype.Timestamptz{Time: now.AddDate(0, 0, -PlanNoticeDays), Valid: true})
	if err != nil {
		return fmt.Errorf("list ready AI budget plans: %w", err)
	}
	var errs []error
	for _, row := range rows {
		message, err := planReadyMessage(row)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, a.send(ctx, result, row.Month.Time, message))
	}
	return errors.Join(errs...)
}

// planReadyMessage is "Your budget plan for November is ready", with the
// AI's headline, what it plans, and for the salary's plan when it came.
func planReadyMessage(row store.AiBudgetPlan) (notify.Notification, error) {
	var result PlanResult
	if err := json.Unmarshal(row.Result, &result); err != nil {
		return notify.Notification{}, fmt.Errorf("read AI budget plan %s: %w", uuidText(row.ID.Bytes), err)
	}
	format := func(amount int64) string { return financemoney.Format(amount, row.Currency) }
	body := []string{result.Headline}
	planned := format(result.ProjectedTotal) + " planned"
	if count := len(result.Suggestions); count > 0 {
		var savings int64
		for _, suggestion := range result.Suggestions {
			savings += suggestion.MonthlySaving
		}
		ways := "ways"
		if count == 1 {
			ways = "way"
		}
		planned += fmt.Sprintf(", %d %s to spend less (≈ %s a month)", count, ways, format(savings))
	}
	body = append(body, planned+".")
	if row.Trigger == "salary" && row.SalaryBookedOn.Valid {
		body = append(body, "Planned when your salary arrived on "+row.SalaryBookedOn.Time.Format("2 Jan")+".")
	}
	body = append(body, "Nothing changes until you apply it.")
	return notify.Notification{Title: fmt.Sprintf("Your budget plan for %s is ready", row.Month.Time.Month()), Body: strings.Join(body, " "),
		Importance: notify.Medium, Category: PlanReadyCategory, Link: BudgetLink + "&month=" + MonthKey(row.Month.Time),
		DedupeKey: PlanReadyKey(row.ID.Bytes), OccurredAt: row.FinishedAt.Time}, nil
}
