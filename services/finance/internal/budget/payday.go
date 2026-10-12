package budget

import (
	"time"

	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// Payday (P13-10): the AI plans the budget when the salary arrives. The
// owner names who pays it; a booked payment from them asks for an AI budget
// plan (trigger salary) for the month the money is meant for. The salary
// comes at the end of a month, just before the month it pays for, while
// the job's own plan comes on the 1st, after it.

const (
	// SalaryNextMonthFrom: a salary booked on this day of a month or later
	// is for the next month; earlier, for its own month.
	SalaryNextMonthFrom = 15
	// SalaryRecentDays is how old a payment may be (by its booking date,
	// before the owner's today) and still ask for a plan, so a bank's
	// history read never does.
	SalaryRecentDays = 40
	// PlanNoticeDays is how long after a plan became ready its notification
	// is still sent (after Finance or Notifications was down).
	PlanNoticeDays = 7
)

// Payday is the payday setting.
type Payday struct {
	Enabled bool
	// Payer is text the payment's counterparty contains, as a rule
	// matches: case-insensitively, ignoring surrounding and repeated
	// whitespace.
	Payer string
	// Minimum leaves out smaller payments from the payer (refunds); nil
	// is any amount.
	Minimum *int64
}

// Pays reports whether a booked payment of amount (money in is positive)
// from counterparty is the salary.
func (p Payday) Pays(counterparty string, amount int64) bool {
	if !p.Enabled || amount <= 0 || (p.Minimum != nil && amount < *p.Minimum) {
		return false
	}
	rule := rules.Rule{Field: rules.FieldCounterparty, Operator: rules.OperatorContains, Text: p.Payer}
	return rule.Matches(rules.Text{Counterparty: counterparty})
}

// SalaryMonth is the month a salary booked on day (a date) is for: the
// next month from the SalaryNextMonthFrom-th on, else day's own month.
func SalaryMonth(day time.Time) time.Time {
	month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	if day.Day() >= SalaryNextMonthFrom {
		return AddMonths(month, 1)
	}
	return month
}

// SalaryRecent reports whether a payment booked on day is recent enough to
// ask for a plan on today (both dates as UTC midnights).
func SalaryRecent(day, today time.Time) bool {
	return !day.Before(today.AddDate(0, 0, -SalaryRecentDays))
}
