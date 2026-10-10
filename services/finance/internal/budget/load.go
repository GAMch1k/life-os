package budget

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// Store is what Load needs from the repository.
type Store interface {
	ListFinancialCategories(ctx context.Context, includeArchived bool) ([]store.FinancialCategory, error)
	GetEffectiveBudgetPlan(ctx context.Context, month pgtype.Date) (store.BudgetPlan, error)
	ListBudgetPlanLines(ctx context.Context, planID pgtype.UUID) ([]store.BudgetPlanLine, error)
	GetBudgetMonthTotals(ctx context.Context, arg store.GetBudgetMonthTotalsParams) ([]store.GetBudgetMonthTotalsRow, error)
}

// Date is a month (or day) as a database date.
func Date(day time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

// Categories converts stored categories, archived ones included.
func Categories(rows []store.FinancialCategory) []Category {
	categories := make([]Category, 0, len(rows))
	for _, row := range rows {
		category := Category{ID: row.ID.Bytes, Name: row.Name, Kind: row.Kind, Color: row.Color, Archived: row.ArchivedAt.Valid}
		if row.SystemKey != nil {
			category.SystemKey = *row.SystemKey
		}
		categories = append(categories, category)
	}
	return categories
}

// LoadPlan returns the plan that applies to month, or nil.
func LoadPlan(ctx context.Context, q Store, month time.Time) (*Plan, error) {
	row, err := q.GetEffectiveBudgetPlan(ctx, Date(month))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines, err := q.ListBudgetPlanLines(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Scope: row.Scope, Month: row.Month.Time, Currency: row.Currency, ExpectedIncome: row.ExpectedIncomeMinor,
		SavingsGoal: row.SavingsGoalMinor, Lines: make(map[[16]byte]int64, len(lines)), UpdatedAt: row.UpdatedAt.Time.UTC()}
	for _, line := range lines {
		plan.Lines[line.CategoryID.Bytes] = line.AmountMinor
	}
	return plan, nil
}

// Load reads a month's plan and transactions and computes its budget, in
// the plan's currency, or currency when no plan applies.
func Load(ctx context.Context, q Store, month time.Time, currency string) (Month, error) {
	plan, err := LoadPlan(ctx, q, month)
	if err != nil {
		return Month{}, err
	}
	if plan != nil {
		currency = plan.Currency
	}
	rows, err := q.ListFinancialCategories(ctx, true)
	if err != nil {
		return Month{}, err
	}
	stored, err := q.GetBudgetMonthTotals(ctx, store.GetBudgetMonthTotalsParams{Currency: currency, MonthStart: Date(month)})
	if err != nil {
		return Month{}, err
	}
	totals := make([]Total, 0, len(stored))
	for _, row := range stored {
		total := Total{OutBooked: row.OutBookedMinor, OutPending: row.OutPendingMinor, InBooked: row.InBookedMinor, InPending: row.InPendingMinor,
			NetOutsideSavings: row.NetOutsideSavingsMinor, Count: row.TransactionCount}
		if row.CategoryID.Valid {
			id := row.CategoryID.Bytes
			total.CategoryID = &id
		}
		totals = append(totals, total)
	}
	return Compute(month, currency, plan, Categories(rows), totals), nil
}
