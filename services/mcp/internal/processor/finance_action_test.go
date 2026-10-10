package processor

import "testing"

// Cash spending needs only the amount: Finance records a transaction without
// an account in the built-in Cash account, dated today.
func TestValidActionChecksCashSpending(t *testing.T) {
	money := func(amount any, currency any) map[string]any {
		return map[string]any{"amount": amount, "currency": currency}
	}
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		valid     bool
	}{
		{"amount only", map[string]any{"amount": money(-1200.0, "EUR")}, true},
		{"with counterparty and description", map[string]any{"amount": money(-1200.0, "EUR"), "counterparty": "Bäckerei", "description": "Bread"}, true},
		{"with an explicit account and day", map[string]any{"accountId": "account-cash", "amount": money(-2000.0, "EUR"), "occurredOn": "2026-10-07", "note": "Lidl"}, true},
		{"income", map[string]any{"amount": money(5000.0, "EUR"), "counterparty": "Anna"}, true},
		{"no amount", map[string]any{"counterparty": "Bäckerei"}, false},
		{"zero amount", map[string]any{"amount": money(0.0, "EUR")}, false},
		{"fractional cents", map[string]any{"amount": money(-12.5, "EUR")}, false},
		{"no currency", map[string]any{"amount": money(-1200.0, "")}, false},
		{"blank account", map[string]any{"accountId": " ", "amount": money(-1200.0, "EUR")}, false},
		{"bad day", map[string]any{"amount": money(-1200.0, "EUR"), "occurredOn": "yesterday"}, false},
		{"unknown field", map[string]any{"amount": money(-1200.0, "EUR"), "merchant": "Bäckerei"}, false},
	} {
		if got := validAction(plannedAction{Tool: "finance_create_manual_transaction", Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}
