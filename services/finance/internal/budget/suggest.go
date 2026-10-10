package budget

import (
	"slices"
	"time"
)

// SuggestionMonths is how many complete months suggestions look back.
const SuggestionMonths = 3

// HistoryRow is one category's booked money in one month (CategoryID nil:
// uncategorized), as positive amounts.
type HistoryRow struct {
	Month      time.Time
	CategoryID *[16]byte
	Out, In    int64
}

// Suggestion is a category's suggested amount: nil when it had no spending
// in the months looked at.
type Suggestion struct {
	Amount             *int64
	MonthsWithSpending int
}

// Suggestions are starting amounts for a plan.
type Suggestions struct {
	// Months are the months with booked transactions the suggestions are
	// based on, oldest first; empty when there is no history yet.
	Months []time.Time
	// Income and Saved are the medians of the monthly income and of income
	// minus spending; nil without history (Income also when there was no
	// income).
	Income, Saved *int64
	// Categories has every active expense category but Savings.
	Categories map[[16]byte]Suggestion
}

// SuggestionWindow is the first and the month after the last of the
// complete months suggestions for month look at: the three months before
// it, and never later than the three before the current month.
func SuggestionWindow(month, current time.Time) (from, to time.Time) {
	to = month
	if to.After(current) {
		to = current
	}
	return AddMonths(to, -SuggestionMonths), to
}

// Suggest computes starting amounts from booked history: per category the
// median of its monthly spending (less refunds) over the months that have
// any booked transaction, a month without spending in the category
// counting as 0, rounded up to a whole unit of the currency (10^exponent
// minor units). Income and savings are rounded down.
func Suggest(categories []Category, rows []HistoryRow, exponent int) Suggestions {
	unit := int64(1)
	for range exponent {
		unit *= 10
	}
	byMonth := map[time.Time][]Total{}
	for _, row := range rows {
		byMonth[row.Month] = append(byMonth[row.Month], Total{CategoryID: row.CategoryID, OutBooked: row.Out, InBooked: row.In})
	}
	result := Suggestions{Categories: map[[16]byte]Suggestion{}}
	for month := range byMonth {
		result.Months = append(result.Months, month)
	}
	slices.SortFunc(result.Months, func(a, b time.Time) int { return a.Compare(b) })

	spending := map[[16]byte][]int64{}
	var incomes, saved []int64
	for _, month := range result.Months {
		computed := Compute(month, "", nil, categories, byMonth[month])
		incomes = append(incomes, computed.Income)
		saved = append(saved, computed.Saved())
		for _, line := range computed.Lines {
			spending[line.Category.ID] = append(spending[line.Category.ID], line.Spent)
		}
	}
	for _, category := range categories {
		if category.Kind != KindExpense || category.Archived || category.SystemKey == KeySavings {
			continue
		}
		values := spending[category.ID]
		suggestion := Suggestion{MonthsWithSpending: len(values)}
		if len(values) > 0 {
			// Months with history but no spending here count as 0.
			for len(values) < len(result.Months) {
				values = append(values, 0)
			}
			amount := roundUp(median(values, true), unit)
			suggestion.Amount = &amount
		}
		result.Categories[category.ID] = suggestion
	}
	if len(result.Months) > 0 {
		value := roundDown(median(saved, false), unit)
		result.Saved = &value
		if slices.ContainsFunc(incomes, func(income int64) bool { return income > 0 }) {
			income := roundDown(median(incomes, false), unit)
			result.Income = &income
		}
	}
	return result
}

// median of values; between two middle values it rounds up or down.
func median(values []int64, up bool) int64 {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	sum := sorted[middle-1] + sorted[middle]
	if up {
		return ceilDiv(sum, 2)
	}
	return floorDiv(sum, 2)
}

func roundUp(value, unit int64) int64   { return ceilDiv(value, unit) * unit }
func roundDown(value, unit int64) int64 { return floorDiv(value, unit) * unit }

func floorDiv(a, b int64) int64 {
	quotient := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		quotient--
	}
	return quotient
}

func ceilDiv(a, b int64) int64 { return -floorDiv(-a, b) }
