package budget

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// readyPlan stores a plan for month that became ready at finished.
func (w *alertWorld) readyPlan(month, trigger string, finished time.Time) store.AiBudgetPlan {
	w.t.Helper()
	parsed, _ := ParseMonth(month)
	params := store.CreateAIBudgetPlanParams{Month: Date(parsed), Currency: "EUR", Trigger: trigger, RequestedByKind: "system"}
	if trigger == "salary" {
		params.SalaryBookedOn = Date(time.Date(2026, time.October, 28, 0, 0, 0, 0, time.UTC))
	}
	row, err := w.q.CreateAIBudgetPlan(w.t.Context(), params)
	if err != nil {
		w.t.Fatal(err)
	}
	result := `{"headline": "Money is tight: eat out less and keep groceries steady.", "rough": false, "fullMonths": 3, "lines": [],
		"suggestions": [{"title": "Cook instead of pizza", "detail": "Twice a week.", "merchants": [], "monthlySavingMinor": 3000},
		                {"title": "Fewer bar nights", "detail": "Two a month.", "merchants": [], "monthlySavingMinor": 2000}],
		"projectedTotalMinor": 119000, "incomeEstimateMinor": 205000, "projectedSavingsMinor": 86000, "savingsGoalMinor": null}`
	row, err = w.q.SetAIBudgetPlanResult(w.t.Context(), store.SetAIBudgetPlanResultParams{Result: []byte(result), Now: pgtype.Timestamptz{Time: finished, Valid: true}, ID: row.ID})
	if err != nil {
		w.t.Fatal(err)
	}
	return row
}

func TestReadyPlansTheOwnerDidNotAskForAreNotifiedOnce(t *testing.T) {
	w := newAlertWorld(t)
	at := time.Date(2026, time.October, 29, 7, 0, 0, 0, time.UTC)
	salary := w.readyPlan("2026-11", "salary", at.Add(-time.Hour))
	scheduled := w.readyPlan("2026-10", "schedule", at.Add(-2*time.Hour))
	// The owner asked for this one: they are watching the Budget tab.
	w.readyPlan("2026-12", "request", at.Add(-time.Hour))
	// Ready for more than a week: Finance was down; it is not notified late.
	w.readyPlan("2027-01", "schedule", at.AddDate(0, 0, -8))
	// Dismissed before the check.
	dismissed := w.readyPlan("2027-02", "salary", at.Add(-time.Hour))
	if _, err := w.q.DismissAIBudgetPlan(t.Context(), store.DismissAIBudgetPlanParams{Now: pgtype.Timestamptz{Time: at, Valid: true}, ID: dismissed.ID}); err != nil {
		t.Fatal(err)
	}

	result, sent := w.run("2026-10-29 08:00")
	if result.Sent != 2 || len(sent) != 2 {
		t.Fatalf("result %+v, sent %q", result, titles(sent))
	}
	// Oldest ready first.
	october, november := sent[0], sent[1]
	if october.Title != "Your budget plan for October is ready" || october.DedupeKey != "finance.ai_budget_plan.ready:"+uuidText(scheduled.ID.Bytes) ||
		october.Link != "/finance?tab=budget&month=2026-10" {
		t.Fatalf("October = %+v", october)
	}
	want := notify.Notification{Title: "Your budget plan for November is ready",
		Body: "Money is tight: eat out less and keep groceries steady. €1,190.00 planned, 2 ways to spend less (≈ €50.00 a month). " +
			"Planned when your salary arrived on 28 Oct. Nothing changes until you apply it.",
		Importance: notify.Medium, Category: "finance.ai_budget_plan.ready", Link: "/finance?tab=budget&month=2026-11",
		DedupeKey: "finance.ai_budget_plan.ready:" + uuidText(salary.ID.Bytes), OccurredAt: at.Add(-time.Hour)}
	if november.Title != want.Title || november.Body != want.Body || november.Importance != want.Importance || november.Category != want.Category ||
		november.Link != want.Link || november.DedupeKey != want.DedupeKey || !november.OccurredAt.Equal(want.OccurredAt) {
		t.Fatalf("November = %+v\nwant %+v", november, want)
	}
	// Once each, across checks.
	if result, sent := w.run("2026-10-29 08:05"); len(sent) != 0 || result.Handled != 2 {
		t.Fatalf("again: result %+v, sent %q", result, titles(sent))
	}
}
