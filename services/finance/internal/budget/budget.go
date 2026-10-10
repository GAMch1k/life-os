// Package budget is Finance's monthly budget arithmetic: the month's
// progress against the plan that applies to it, the savings, the starting
// amounts suggested from earlier months, and the default categories and
// merchant rules Finance adds once. The computations are pure; Load and
// Seed read and write what they need through small store interfaces.
package budget

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Category kinds and the system key of the Savings category.
const (
	KindExpense  = "expense"
	KindIncome   = "income"
	KindTransfer = "transfer"
	KeySavings   = "savings"
)

// Plan scopes as stored: a regular plan applies to its month and every
// later month until another regular plan starts; a month plan only to its
// month.
const (
	ScopeRegular = "regular"
	ScopeMonth   = "month"
)

// Thresholds of a category's planned amount that notify the owner.
const (
	NearPercent = 80
	FullPercent = 100
)

// Category is what the budget needs to know about a category.
type Category struct {
	ID        [16]byte
	Name      string
	Kind      string
	Color     *string
	SystemKey string
	Archived  bool
}

// Total is one category's money in a month in the budget currency
// (CategoryID nil: uncategorized): money out and in, booked and pending,
// as positive amounts, and the net amount on accounts that are not savings
// accounts.
type Total struct {
	CategoryID                                 *[16]byte
	OutBooked, OutPending, InBooked, InPending int64
	NetOutsideSavings                          int64
	Count                                      int64
}

// Plan is the plan that applies to a month.
type Plan struct {
	Scope          string
	Month          time.Time // its first month
	Currency       string
	ExpectedIncome int64
	SavingsGoal    int64
	// Lines are the planned amounts per expense category (all positive).
	Lines     map[[16]byte]int64
	UpdatedAt time.Time
}

// Status is a line's state against its planned amount.
type Status string

const (
	Under     Status = "under"
	Near      Status = "near"
	Over      Status = "over"
	Unplanned Status = "unplanned"
)

// Line is one expense category's month: Spent is its money out less
// refunds (never below 0), booked and pending; Pending is the part of it
// still pending.
type Line struct {
	Category                Category
	Planned, Spent, Pending int64
	Count                   int64
}

// Remaining is the planned amount minus the spending; negative when over.
func (l Line) Remaining() int64 { return l.Planned - l.Spent }

// Percent is the spending as a percentage of the planned amount, rounded
// down; nil without a planned amount.
func (l Line) Percent() *int { return percentOf(l.Spent, l.Planned) }

// Reached reports whether the spending reached percent of the planned
// amount; never without a planned amount.
func (l Line) Reached(percent int64) bool {
	return l.Planned > 0 && l.Spent*100 >= percent*l.Planned
}

// Status is the line's state: over when it spent more than planned, near
// from 80%.
func (l Line) Status() Status {
	switch {
	case l.Planned <= 0:
		return Unplanned
	case l.Spent > l.Planned:
		return Over
	case l.Reached(NearPercent):
		return Near
	}
	return Under
}

// Month is a month's budget in one currency.
type Month struct {
	Month    time.Time
	Currency string
	// Plan is nil when no plan applies to the month.
	Plan *Plan
	// Lines are the plan's categories by name, then expense categories
	// with spending the plan has no amount for, largest first.
	Lines         []Line
	Uncategorized struct{ Spent, Pending, Count int64 }
	// Planned is the plan's total; Spent all spending (every expense
	// category and uncategorized money out), of it Pending.
	Planned, Spent, Pending int64
	// Income is money in in income categories (less money out there) and
	// uncategorized money in, of it IncomePending.
	Income, IncomePending int64
	// MovedToSavings is the net money moved out in the Savings category
	// from accounts that are not savings accounts.
	MovedToSavings int64
}

// Remaining is the planned total minus all spending.
func (m Month) Remaining() int64 { return m.Planned - m.Spent }

// Percent is all spending against the planned total, rounded down.
func (m Month) Percent() *int { return percentOf(m.Spent, m.Planned) }

// Saved is the month's income minus its spending: what was moved to
// savings plus what is left over.
func (m Month) Saved() int64 { return m.Income - m.Spent }

// Leftover is the part of Saved not moved to the Savings category.
func (m Month) Leftover() int64 { return m.Saved() - m.MovedToSavings }

// SavedPercent is Saved against the plan's savings goal, rounded down; nil
// without a goal.
func (m Month) SavedPercent() *int {
	if m.Plan == nil {
		return nil
	}
	return percentOf(m.Saved(), m.Plan.SavingsGoal)
}

// Unallocated is the plan's expected income minus its planned spending and
// savings goal.
func (m Month) Unallocated() int64 {
	if m.Plan == nil {
		return 0
	}
	return m.Plan.ExpectedIncome - m.Planned - m.Plan.SavingsGoal
}

// OverBudget lists the planned lines that spent more than planned.
func (m Month) OverBudget() []Line {
	var over []Line
	for _, line := range m.Lines {
		if line.Status() == Over {
			over = append(over, line)
		}
	}
	return over
}

func percentOf(part, whole int64) *int {
	if whole <= 0 {
		return nil
	}
	percent := int(part * 100 / whole)
	return &percent
}

// Compute is the month's budget: the totals of its transactions in the
// budget currency against the plan (nil: none). The Savings category is
// never spending or income, transfers are neither, and refunds lower their
// expense category's spending.
func Compute(month time.Time, currency string, plan *Plan, categories []Category, totals []Total) Month {
	result := Month{Month: month, Currency: currency, Plan: plan}
	byID := make(map[[16]byte]Category, len(categories))
	for _, category := range categories {
		byID[category.ID] = category
	}
	spending := map[[16]byte]Line{}
	for _, total := range totals {
		out, in := total.OutBooked+total.OutPending, total.InBooked+total.InPending
		if total.CategoryID == nil {
			result.Uncategorized.Spent += out
			result.Uncategorized.Pending += total.OutPending
			result.Uncategorized.Count += total.Count
			result.Income += in
			result.IncomePending += total.InPending
			continue
		}
		category, ok := byID[*total.CategoryID]
		if !ok {
			continue
		}
		switch {
		case category.SystemKey == KeySavings:
			result.MovedToSavings -= total.NetOutsideSavings
		case category.Kind == KindIncome:
			result.Income += in - out
			result.IncomePending += total.InPending
		case category.Kind == KindExpense:
			spent := max(0, out-in)
			spending[category.ID] = Line{Category: category, Spent: spent, Pending: min(spent, total.OutPending), Count: total.Count}
		}
	}
	result.Spent, result.Pending = result.Uncategorized.Spent, result.Uncategorized.Pending
	for _, line := range spending {
		result.Spent += line.Spent
		result.Pending += line.Pending
	}

	var planned, unplanned []Line
	if plan != nil {
		for id, amount := range plan.Lines {
			category, ok := byID[id]
			if !ok || category.Kind != KindExpense || category.SystemKey == KeySavings || amount <= 0 {
				continue
			}
			line := spending[id]
			line.Category, line.Planned = category, amount
			planned = append(planned, line)
			result.Planned += amount
		}
	}
	for id, line := range spending {
		if plan != nil && plan.Lines[id] > 0 {
			continue
		}
		if line.Spent > 0 {
			unplanned = append(unplanned, line)
		}
	}
	slices.SortFunc(planned, func(a, b Line) int { return compareNames(a.Category, b.Category) })
	slices.SortFunc(unplanned, func(a, b Line) int {
		if order := cmp.Compare(b.Spent, a.Spent); order != 0 {
			return order
		}
		return compareNames(a.Category, b.Category)
	})
	result.Lines = append(planned, unplanned...)
	return result
}

func compareNames(a, b Category) int {
	if order := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
		return order
	}
	return slices.Compare(a.ID[:], b.ID[:])
}

// Months.

// ParseMonth reads a YYYY-MM month as the first day of the month (UTC).
func ParseMonth(text string) (time.Time, error) {
	month, err := time.Parse("2006-01", text)
	if err != nil || month.Format("2006-01") != text {
		return time.Time{}, fmt.Errorf("month must use YYYY-MM")
	}
	if month.Year() < 2000 || month.Year() > 2200 {
		return time.Time{}, fmt.Errorf("month must be between 2000 and 2200")
	}
	return month, nil
}

// MonthKey is a month as YYYY-MM.
func MonthKey(month time.Time) string { return month.Format("2006-01") }

// CurrentMonth is the first day of the owner's current month.
func CurrentMonth(now time.Time, location *time.Location) time.Time {
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// AddMonths moves a month (the first of a month) by n months.
func AddMonths(month time.Time, n int) time.Time { return month.AddDate(0, n, 0) }

// MonthLabel is a month as the owner reads it: "October 2026".
func MonthLabel(month time.Time) string { return month.Format("January 2006") }
