package subscriptions

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// Recurring income, for the payday suggestion (P13-10): who pays the owner
// every month, found the way subscriptions are, on money in.

// monthlyIncome is a monthly run of two payments or more: a salary needs
// no third month to be recognized.
var monthlyIncome = []cadence{{Monthly, 26, 35, 1, 0, 2}}

// Income finds the money that arrives monthly among payments (money in,
// Amount positive) as of today (a UTC midnight): per counterparty (a
// payer is recognized by it, so known services and intermediaries are left
// out), payments of similar amounts form a group, and a group counts when
// its latest payments came in at least two months in a row (26 to 35 days
// apart), the latest no more than MissedAfterDays past when the next was
// due. Each payer has at most one, the group of its latest regular
// payments; the largest typical amount comes first.
func Income(payments []Payment, today time.Time, exponent Exponent) []Candidate {
	byPayer := map[string][]Payment{}
	for _, payment := range withMerchants(payments) {
		if !strings.HasPrefix(payment.Merchant.Key, "cp:") || payment.Amount <= 0 {
			continue
		}
		byPayer[payment.Merchant.Key] = append(byPayer[payment.Merchant.Key], payment)
	}
	var found []Candidate
	for _, group := range byPayer {
		slices.SortFunc(group, latestFirst)
		currency := group[0].Currency
		group = slices.DeleteFunc(group, func(p Payment) bool { return p.Currency != currency })
		for _, cluster := range clusters(group, exponent(currency)) {
			if candidate, ok := regular(cluster, today, monthlyIncome); ok {
				found = append(found, candidate)
				break
			}
		}
	}
	slices.SortFunc(found, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(b.Amount, a.Amount), b.Last.Compare(a.Last), cmp.Compare(a.Merchant.Key, b.Merchant.Key))
	})
	return found
}

// PayerText is the text that recognizes a candidate's payer: the latest
// payment's counterparty when every payment's counterparty contains it (as
// a rule matches), else the payer's name, else its key's words; "" when
// none matches them all.
func PayerText(candidate Candidate) string {
	options := []string{tidy(candidate.Payments[0].Counterparty), candidate.Merchant.Name, strings.TrimPrefix(candidate.Merchant.Key, "cp:")}
	for _, text := range options {
		rule := rules.Rule{Field: rules.FieldCounterparty, Operator: rules.OperatorContains, Text: text}
		if rules.Normalize(text) != "" && !slices.ContainsFunc(candidate.Payments, func(p Payment) bool {
			return !rule.Matches(rules.Text{Counterparty: p.Counterparty})
		}) {
			return limit(text)
		}
	}
	return ""
}
