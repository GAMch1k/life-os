// Package subscriptions finds recurring payments in Finance's transactions
// and keeps the stored subscriptions up to date. Detection is
// deterministic code, no AI: booked money out to the same merchant at a
// regular interval with a similar amount. The computations are pure; Sync
// reads and writes through a small store interface.
package subscriptions

import (
	"cmp"
	"slices"
	"time"
)

// Cadences.
const (
	Weekly    = "weekly"
	Monthly   = "monthly"
	Quarterly = "quarterly"
	Yearly    = "yearly"
)

// States, sources, and confidences as stored.
const (
	StateSuggested = "suggested"
	StateConfirmed = "confirmed"
	StateIgnored   = "ignored"

	SourceDetected = "detected"
	SourceManual   = "manual"

	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
)

// Renewal states of a confirmed subscription's next payment.
const (
	RenewalScheduled = "scheduled"
	RenewalUpcoming  = "upcoming"
	RenewalOverdue   = "overdue"
	RenewalMissed    = "missed"
)

// MissedAfterDays is how late an expected payment may be before it counts
// as missed.
const MissedAfterDays = 7

// LookbackDays is how far back detection looks: far enough for two yearly
// payments up to 380 days apart, the later one at most a year and a week
// ago.
const LookbackDays = 800

// cadence is a regular interval: the days two payments may lie apart, and
// the period that gives the next expected date.
type cadence struct {
	name           string
	minGap, maxGap int
	months, days   int
	// minPayments is how many payments in a row make it regular.
	minPayments int
}

// cadences in the order they are tried.
var cadences = []cadence{
	{Weekly, 6, 8, 0, 7, 3},
	{Monthly, 26, 35, 1, 0, 3},
	{Quarterly, 84, 98, 3, 0, 3},
	{Yearly, 350, 380, 12, 0, 2},
}

func cadenceOf(name string) (cadence, bool) {
	for _, c := range cadences {
		if c.name == name {
			return c, true
		}
	}
	return cadence{}, false
}

// ValidCadence reports whether name is a cadence.
func ValidCadence(name string) bool {
	_, ok := cadenceOf(name)
	return ok
}

// Next is the day the payment after one on last is expected: a week, or
// one, three, or twelve months later (the 31st becomes the month's last
// day). Unknown cadences add a month.
func Next(last time.Time, name string) time.Time {
	c, ok := cadenceOf(name)
	if !ok {
		c, _ = cadenceOf(Monthly)
	}
	if c.days > 0 {
		return last.AddDate(0, 0, c.days)
	}
	return addMonths(last, c.months)
}

func addMonths(day time.Time, months int) time.Time {
	year, month, date := day.Date()
	first := time.Date(year, month+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	lastDay := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(date, lastDay), 0, 0, 0, 0, time.UTC)
}

// MonthlyEquivalent is what an amount paid at a cadence costs per month:
// weekly times 52/12, quarterly a third, yearly a twelfth, rounded to the
// nearest minor unit.
func MonthlyEquivalent(amount int64, name string) int64 {
	switch name {
	case Weekly:
		return roundDiv(amount*52, 12)
	case Quarterly:
		return roundDiv(amount, 3)
	case Yearly:
		return roundDiv(amount, 12)
	}
	return amount
}

func roundDiv(a, b int64) int64 {
	if a < 0 {
		return -roundDiv(-a, b)
	}
	return (a + b/2) / b
}

// Similar reports whether amount is within 10% of reference, or within two
// units of the currency (€2 for EUR), whichever is more; exponent is the
// currency's number of minor-unit digits.
func Similar(amount, reference int64, exponent int) bool {
	unit := int64(1)
	for range exponent {
		unit *= 10
	}
	tolerance := max(reference/10, 2*unit)
	diff := amount - reference
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

// Renewal is where a confirmed subscription's next payment stands on
// today: upcoming from leadDays before it (today included), overdue up to
// MissedAfterDays after it, missed later, scheduled before.
func Renewal(next, today time.Time, leadDays int) string {
	switch {
	case today.After(next.AddDate(0, 0, MissedAfterDays)):
		return RenewalMissed
	case today.After(next):
		return RenewalOverdue
	case !today.Before(next.AddDate(0, 0, -leadDays)):
		return RenewalUpcoming
	}
	return RenewalScheduled
}

// Payment is one booked money-out transaction (money in for Income).
// Amount is positive; Date is a UTC midnight.
type Payment struct {
	ID           [16]byte
	AccountID    [16]byte
	Amount       int64
	Currency     string
	Date         time.Time
	Counterparty string
	Description  string
	CategoryID   *[16]byte
	// Merchant is filled by Detect and Track when empty.
	Merchant Merchant
}

// Candidate is a recurring payment detection found.
type Candidate struct {
	Merchant   Merchant
	Cadence    string
	Currency   string
	Amount     int64 // typical: the median of the last three payments
	AccountID  [16]byte
	CategoryID *[16]byte
	Last, Next time.Time
	Confidence string
	// Payments, latest first.
	Payments []Payment
}

// Exponent tells the minor-unit digits of a currency.
type Exponent func(currency string) int

func withMerchants(payments []Payment) []Payment {
	out := make([]Payment, len(payments))
	for index, payment := range payments {
		if payment.Merchant.Key == "" {
			payment.Merchant = MerchantOf(payment.Counterparty, payment.Description)
		}
		out[index] = payment
	}
	return out
}

func daysBetween(earlier, later time.Time) int {
	return int(later.Sub(earlier).Hours() / 24)
}

func latestFirst(a, b Payment) int {
	if c := b.Date.Compare(a.Date); c != 0 {
		return c
	}
	return slices.Compare(b.ID[:], a.ID[:])
}

// Detect finds the recurring payments among payments as of today (a UTC
// midnight): per merchant (in the currency of its latest payment), payments
// of similar amounts form a group, starting from the latest; a group is
// regular when its latest payments follow each other at one cadence's
// interval (weekly 6-8 days, monthly 26-35, quarterly 84-98, yearly
// 350-380), at least three in a row (two for yearly, with low
// confidence). A gap shorter than the cadence's ends the run, so a shop
// paid at random is not a subscription. A run whose next payment is
// already more than MissedAfterDays late is left out. Each merchant has at
// most one candidate per cadence, the latest.
func Detect(payments []Payment, today time.Time, exponent Exponent) []Candidate {
	byMerchant := map[string][]Payment{}
	for _, payment := range withMerchants(payments) {
		if payment.Merchant.Key == "" || payment.Amount <= 0 {
			continue
		}
		byMerchant[payment.Merchant.Key] = append(byMerchant[payment.Merchant.Key], payment)
	}
	var candidates []Candidate
	for _, group := range byMerchant {
		slices.SortFunc(group, latestFirst)
		currency := group[0].Currency
		group = slices.DeleteFunc(group, func(p Payment) bool { return p.Currency != currency })
		seen := map[string]bool{}
		for _, cluster := range clusters(group, exponent(currency)) {
			candidate, ok := regular(cluster, today, cadences)
			if !ok || seen[candidate.Cadence] {
				continue
			}
			seen[candidate.Cadence] = true
			candidates = append(candidates, candidate)
		}
	}
	slices.SortFunc(candidates, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(a.Merchant.Key, b.Merchant.Key), cmp.Compare(a.Cadence, b.Cadence))
	})
	return candidates
}

// clusters splits a merchant's payments (latest first) into groups of
// similar amounts: the latest payment not yet grouped starts a group with
// every other one similar to it. Each group stays latest first.
func clusters(payments []Payment, exponent int) [][]Payment {
	grouped := make([]bool, len(payments))
	var out [][]Payment
	for index, seed := range payments {
		if grouped[index] {
			continue
		}
		var cluster []Payment
		for other := index; other < len(payments); other++ {
			if !grouped[other] && Similar(payments[other].Amount, seed.Amount, exponent) {
				grouped[other] = true
				cluster = append(cluster, payments[other])
			}
		}
		out = append(out, cluster)
	}
	return out
}

// regular tries the cadences on a group of payments (latest first) and
// returns the first that its latest run follows.
func regular(cluster []Payment, today time.Time, tried []cadence) (Candidate, bool) {
	for _, c := range tried {
		run := []Payment{cluster[0]}
		for _, payment := range cluster[1:] {
			gap := daysBetween(payment.Date, run[len(run)-1].Date)
			if gap < c.minGap || gap > c.maxGap {
				break
			}
			run = append(run, payment)
		}
		if len(run) < c.minPayments {
			continue
		}
		latest := run[0]
		next := Next(latest.Date, c.name)
		if today.After(next.AddDate(0, 0, MissedAfterDays)) {
			return Candidate{}, false
		}
		confidence := ConfidenceHigh
		if c.name == Yearly && len(run) < 3 {
			confidence = ConfidenceLow
		}
		return Candidate{Merchant: latest.Merchant, Cadence: c.name, Currency: latest.Currency, Amount: Typical(run),
			AccountID: latest.AccountID, CategoryID: latest.CategoryID, Last: latest.Date, Next: next, Confidence: confidence, Payments: run}, true
	}
	return Candidate{}, false
}

// Typical is the median of the latest three payments (latest first); with
// two, the latest.
func Typical(payments []Payment) int64 {
	switch len(payments) {
	case 0:
		return 0
	case 1, 2:
		return payments[0].Amount
	}
	amounts := []int64{payments[0].Amount, payments[1].Amount, payments[2].Amount}
	slices.Sort(amounts)
	return amounts[1]
}

// Track finds a subscription's payments: those to its merchant in its
// currency with an amount similar to its amount, latest first.
func Track(merchantKey, currency string, amount int64, payments []Payment, exponent Exponent) []Payment {
	var matched []Payment
	for _, payment := range withMerchants(payments) {
		if payment.Merchant.Key == merchantKey && payment.Currency == currency && Similar(payment.Amount, amount, exponent(currency)) {
			matched = append(matched, payment)
		}
	}
	slices.SortFunc(matched, latestFirst)
	return matched
}
