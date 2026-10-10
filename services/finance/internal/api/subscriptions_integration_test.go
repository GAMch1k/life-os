package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/finance/internal/alerts"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

// detect runs subscription detection as the background loop would, on the
// owner's today of fixedNow (11 October 2026 in Berlin).
func (c apiClient) detect() {
	c.t.Helper()
	if _, err := alerts.Detect(c.t.Context(), c.handler.pool, time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		c.t.Fatal(err)
	}
}

func (c apiClient) subscriptionList(query string) generated.SubscriptionList {
	c.t.Helper()
	return expect[generated.SubscriptionList](c, http.MethodGet, "/v1/subscriptions"+query, nil, http.StatusOK)
}

func subscriptionNames(items []generated.Subscription) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name+" ("+string(item.State)+")")
	}
	return strings.Join(out, ", ")
}

func TestSubscriptionsAreConfirmedIgnoredEditedAndDeleted(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Giro", "bank", "EUR", 500000)
	streaming := c.category("Streaming", "expense")
	for _, day := range []string{"2026-07-15", "2026-08-14", "2026-09-15"} {
		c.spend(map[string]any{"accountId": giro.Id, "amount": eur(-1399), "bookingDate": day, "counterparty": "NETFLIX.COM", "categoryId": streaming.Id})
	}
	for _, day := range []string{"2026-07-20", "2026-08-20", "2026-09-21"} {
		c.spend(map[string]any{"accountId": giro.Id, "amount": eur(-4900), "bookingDate": day, "counterparty": "FitX Deutschland GmbH"})
	}
	for _, day := range []string{"2026-08-01", "2026-09-01", "2026-10-01"} {
		c.spend(map[string]any{"accountId": giro.Id, "amount": eur(-500), "bookingDate": day, "counterparty": "Cloud Storage Ltd"})
	}
	c.detect()
	list := c.subscriptionList("")
	if subscriptionNames(list.Items) != "Cloud Storage Ltd (suggested), FitX Deutschland GmbH (suggested), Netflix (suggested)" ||
		list.Totals != (generated.SubscriptionTotals{Currency: "EUR", Monthly: money(0, "EUR")}) {
		t.Fatalf("suggested = %s, totals %+v", subscriptionNames(list.Items), list.Totals)
	}
	cloud, fitx, netflix := list.Items[0], list.Items[1], list.Items[2]
	if netflix.Amount != money(1399, "EUR") || netflix.MonthlyAmount != money(1399, "EUR") || netflix.Cadence != generated.Monthly ||
		netflix.CategoryId == nil || *netflix.CategoryId != streaming.Id || netflix.AccountId == nil || *netflix.AccountId != giro.Id ||
		netflix.LastDate.String() != "2026-09-15" || netflix.NextDate.String() != "2026-10-15" || netflix.Renewal != nil ||
		netflix.Confidence != generated.High || netflix.Source != generated.SubscriptionSourceDetected || len(netflix.TransactionIds) != 3 {
		t.Fatalf("netflix = %+v", netflix)
	}
	seen := len(c.recorder.all())

	confirmed := expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions/"+netflix.Id.String()+"/confirm", nil, http.StatusOK)
	if confirmed.State != generated.Confirmed || confirmed.Renewal == nil || *confirmed.Renewal != generated.Scheduled {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions/"+netflix.Id.String()+"/confirm", nil, http.StatusOK)
	expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions/"+cloud.Id.String()+"/confirm", nil, http.StatusOK)
	ignored := expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions/"+fitx.Id.String()+"/ignore", nil, http.StatusOK)
	if ignored.State != generated.Ignored || ignored.Renewal != nil {
		t.Fatalf("ignored = %+v", ignored)
	}
	events := c.recorder.since(&seen)
	if eventTypes(events) != "finance.subscription.confirmed finance.subscription.confirmed finance.subscription.ignored" ||
		events[0].Summary != "Subscription “Netflix” confirmed (€13.99 monthly)" || events[0].Subject.Type != "finance_subscription" ||
		events[2].Summary != "Subscription “FitX Deutschland GmbH” ignored" {
		t.Fatalf("events = %s / %+v", eventTypes(events), events)
	}
	// Today is 11 October: Cloud Storage renews on 1 November (scheduled).
	list = c.subscriptionList("?state=confirmed")
	if subscriptionNames(list.Items) != "Cloud Storage Ltd (confirmed), Netflix (confirmed)" || list.Totals.Monthly.Amount != 1899 || list.Totals.Confirmed != 2 {
		t.Fatalf("confirmed list = %s, totals %+v", subscriptionNames(list.Items), list.Totals)
	}
	if ignoredList := c.subscriptionList("?state=ignored"); subscriptionNames(ignoredList.Items) != "FitX Deutschland GmbH (ignored)" || ignoredList.Totals.Monthly.Amount != 1899 {
		t.Fatalf("ignored list = %s", subscriptionNames(ignoredList.Items))
	}
	// Detection again: the ignored one is never suggested again.
	c.detect()
	if all := c.subscriptionList(""); subscriptionNames(all.Items) != "Cloud Storage Ltd (confirmed), Netflix (confirmed), FitX Deutschland GmbH (ignored)" {
		t.Fatalf("after detection = %s", subscriptionNames(all.Items))
	}

	// Editing: a yearly cadence moves the next date; the amount and name
	// stay the owner's.
	edited := expect[generated.Subscription](c, http.MethodPatch, "/v1/subscriptions/"+cloud.Id.String(),
		map[string]any{"name": " iCloud+ ", "cadence": "yearly", "amountMinor": 5988, "categoryId": streaming.Id}, http.StatusOK)
	if edited.Name != "iCloud+" || edited.Cadence != generated.Yearly || edited.NextDate.String() != "2027-10-01" ||
		edited.Amount.Amount != 5988 || edited.MonthlyAmount.Amount != 499 || *edited.CategoryId != streaming.Id {
		t.Fatalf("edited = %+v", edited)
	}
	if events := c.recorder.since(&seen); eventTypes(events) != "finance.subscription.updated" ||
		events[0].Summary != "Subscription “iCloud+” updated: name, cadence, amount, category" {
		t.Fatalf("edit events = %+v", events)
	}
	c.detect()
	cleared := expect[generated.Subscription](c, http.MethodPatch, "/v1/subscriptions/"+cloud.Id.String(), map[string]any{"categoryId": nil}, http.StatusOK)
	if cleared.Name != "iCloud+" || cleared.Amount.Amount != 5988 || cleared.CategoryId != nil {
		t.Fatalf("after detection and clearing the category = %+v", cleared)
	}
	// Repeating a change records nothing.
	expect[generated.Subscription](c, http.MethodPatch, "/v1/subscriptions/"+cloud.Id.String(), map[string]any{"categoryId": nil}, http.StatusOK)
	if events := c.recorder.since(&seen); eventTypes(events) != "finance.subscription.updated" {
		t.Fatalf("repeat events = %s", eventTypes(events))
	}
	target := "/v1/subscriptions/" + cloud.Id.String()
	c.expectError(http.MethodPatch, target, map[string]any{}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, target, map[string]any{"amountMinor": 0}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, target, map[string]any{"name": "  "}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, target, map[string]any{"cadence": "daily"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, target, map[string]any{"categoryId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}, http.StatusBadRequest, "unknown_category")
	c.expectError(http.MethodGet, "/v1/subscriptions?state=active", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/subscriptions/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10/confirm", nil, http.StatusNotFound, "not_found")

	if status, raw := c.do(http.MethodDelete, target, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	c.expectError(http.MethodDelete, target, nil, http.StatusNotFound, "not_found")
	if events := c.recorder.since(&seen); eventTypes(events) != "finance.subscription.deleted" || events[0].Summary != "Subscription “iCloud+” deleted" {
		t.Fatalf("delete events = %+v", events)
	}
}

func TestATransactionIsMarkedAsASubscription(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Giro", "bank", "EUR", 500000)
	salary := c.spend(map[string]any{"accountId": giro.Id, "amount": eur(250000), "bookingDate": "2026-10-01", "counterparty": "ACME GmbH"})
	gym := c.spend(map[string]any{"accountId": giro.Id, "amount": eur(-2999), "bookingDate": "2026-10-02", "counterparty": "Urban Sports Club"})
	earlier := c.spend(map[string]any{"accountId": giro.Id, "amount": eur(-2999), "bookingDate": "2026-09-02", "counterparty": "URBAN SPORTS CLUB"})
	seen := len(c.recorder.all())

	c.expectError(http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": salary.Id}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}, http.StatusBadRequest, "unknown_transaction")
	c.expectError(http.MethodPost, "/v1/subscriptions", map[string]any{}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": gym.Id, "cadence": "daily"}, http.StatusBadRequest, "invalid_request")

	created := expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": gym.Id}, http.StatusCreated)
	// It follows the counterparty's other similar payment at once.
	if created.Name != "Urban Sports Club" || created.State != generated.Confirmed || created.Source != generated.SubscriptionSourceManual ||
		created.Cadence != generated.Monthly || created.Amount.Amount != 2999 || created.LastDate.String() != "2026-10-02" ||
		created.NextDate.String() != "2026-11-02" || len(created.TransactionIds) != 2 || created.TransactionIds[0] != gym.Id ||
		created.TransactionIds[1] != earlier.Id || created.Renewal == nil || *created.Renewal != generated.Scheduled {
		t.Fatalf("created = %+v", created)
	}
	// Marking it again returns the same subscription.
	again := expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": earlier.Id}, http.StatusOK)
	if again.Id != created.Id {
		t.Fatalf("again = %+v", again)
	}
	events := c.recorder.since(&seen)
	if eventTypes(events) != "finance.subscription.created" || events[0].Summary != "“Urban Sports Club” marked as a subscription (€29.99 monthly)" {
		t.Fatalf("events = %+v", events)
	}
	// An ignored one of the same counterparty and cadence is confirmed.
	expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions/"+created.Id.String()+"/ignore", nil, http.StatusOK)
	confirmed := expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": gym.Id, "name": "Gym"}, http.StatusOK)
	if confirmed.Id != created.Id || confirmed.State != generated.Confirmed || confirmed.Name != "Gym" {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	if events := c.recorder.since(&seen); eventTypes(events) != "finance.subscription.ignored finance.subscription.confirmed" {
		t.Fatalf("events = %s", eventTypes(events))
	}
	// Another cadence is another subscription.
	quarterly := expect[generated.Subscription](c, http.MethodPost, "/v1/subscriptions", map[string]any{"transactionId": gym.Id, "cadence": "quarterly"}, http.StatusCreated)
	if quarterly.Id == created.Id || quarterly.MonthlyAmount.Amount != 1000 || quarterly.NextDate.String() != "2027-01-02" {
		t.Fatalf("quarterly = %+v", quarterly)
	}
}

func TestAlertSettingsAreReadAndSaved(t *testing.T) {
	c := newAPIClient(t)
	settings := expect[generated.FinanceSettings](c, http.MethodGet, "/v1/settings", nil, http.StatusOK)
	if settings.Currency != "EUR" || settings.LargeDebit != (generated.AmountAlert{Enabled: true, Threshold: money(25000, "EUR")}) ||
		settings.LowBalance != (generated.AmountAlert{Enabled: true, Threshold: money(10000, "EUR")}) ||
		settings.Renewals != (generated.RenewalAlert{Enabled: true, LeadDays: 3}) {
		t.Fatalf("defaults = %+v", settings)
	}
	valid := func() map[string]any {
		return map[string]any{
			"largeDebit": map[string]any{"enabled": true, "thresholdMinor": 30000},
			"lowBalance": map[string]any{"enabled": false, "thresholdMinor": 10000},
			"renewals":   map[string]any{"enabled": true, "leadDays": 1},
		}
	}
	for _, change := range []func(map[string]any){
		func(body map[string]any) { delete(body, "renewals") },
		func(body map[string]any) { delete(body["largeDebit"].(map[string]any), "enabled") },
		func(body map[string]any) { body["largeDebit"].(map[string]any)["thresholdMinor"] = 0 },
		func(body map[string]any) { body["lowBalance"].(map[string]any)["thresholdMinor"] = -1 },
		func(body map[string]any) { body["renewals"].(map[string]any)["leadDays"] = 31 },
		func(body map[string]any) { body["renewals"] = "soon" },
	} {
		body := valid()
		change(body)
		c.expectError(http.MethodPut, "/v1/settings", body, http.StatusBadRequest, "invalid_request")
	}
	seen := len(c.recorder.all())
	saved := expect[generated.FinanceSettings](c, http.MethodPut, "/v1/settings", valid(), http.StatusOK)
	if saved.LargeDebit.Threshold.Amount != 30000 || saved.LowBalance.Enabled || saved.Renewals.LeadDays != 1 {
		t.Fatalf("saved = %+v", saved)
	}
	expect[generated.FinanceSettings](c, http.MethodPut, "/v1/settings", valid(), http.StatusOK)
	events := c.recorder.since(&seen)
	if eventTypes(events) != "finance.settings.updated" || events[0].Subject.Type != "finance_settings" ||
		events[0].Summary != "Alert settings updated: large debits from €300.00, low balance below €100.00 (off), renewal reminders 1 day ahead" {
		t.Fatalf("events = %+v", events)
	}
	if read := expect[generated.FinanceSettings](c, http.MethodGet, "/v1/settings", nil, http.StatusOK); read != saved {
		t.Fatalf("read = %+v, saved %+v", read, saved)
	}
}

func TestAccountsSayWhenTheirBalanceIsLow(t *testing.T) {
	c := newAPIClient(t)
	lowOf := func(id string) bool {
		return expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+id, nil, http.StatusOK).LowBalance
	}
	// Cash and other manual accounts are never low, whatever their balance:
	// the owner logs cash spending without the withdrawals.
	wallet := c.account("Wallet", "bank", "EUR", 0)
	c.spend(map[string]any{"accountId": wallet.Id, "amount": eur(-100)})
	c.spend(map[string]any{"amount": eur(-2500)})
	if lowOf(wallet.Id.String()) || lowOf(c.cash().Id.String()) {
		t.Fatal("manual accounts are low")
	}
	// A bank account with the bank's balance below the threshold is; a bank
	// card without a reported balance is not.
	result := c.syncBank(bankSync([]map[string]any{giroInput(5000, 5000), cardInput}, []map[string]any{
		bankTx("acc-2", "t1", "booked", "2026-10-09", -6000, "Shell"),
	}))
	giro, card := result.AccountIds[0].Id.String(), result.AccountIds[1].Id.String()
	if !lowOf(giro) || lowOf(card) {
		t.Fatal("the bank account should be low and the card not")
	}
	expect[generated.FinanceSettings](c, http.MethodPut, "/v1/settings", map[string]any{
		"largeDebit": map[string]any{"enabled": true, "thresholdMinor": 25000},
		"lowBalance": map[string]any{"enabled": true, "thresholdMinor": 4000},
		"renewals":   map[string]any{"enabled": true, "leadDays": 3},
	}, http.StatusOK)
	for _, account := range expect[generated.AccountList](c, http.MethodGet, "/v1/accounts", nil, http.StatusOK).Items {
		if account.LowBalance {
			t.Fatalf("%s is low at a 40.00 threshold", account.Name)
		}
	}
}

func TestABookedBankTransactionKeepsItsPendingOnesLargeDebitClaim(t *testing.T) {
	c := newAPIClient(t)
	result := c.syncBank(bankSync([]map[string]any{giroInput(500000, 500000)}, []map[string]any{
		bankTx("acc-1", "p1", "pending", "2026-10-09", -48000, "MediaMarkt"),
	}))
	pending := c.byExternalID("p1")
	// The alerts sent the large debit for the pending transaction.
	if claimed, err := c.queries.ClaimAlertNotification(t.Context(), alerts.LargeDebitKey(pending.Id)); err != nil || claimed != 1 {
		t.Fatalf("claim = %d, %v", claimed, err)
	}
	second := c.syncBank(bankSync([]map[string]any{giroInput(452000, 452000)}, []map[string]any{
		bankTx("acc-1", "b1", "booked", "2026-10-10", -48000, "MediaMarkt"),
	}))
	if len(result.AccountIds) != 1 || second.Transactions.RemovedPending != 1 {
		t.Fatalf("second sync = %+v", second)
	}
	booked := c.byExternalID("b1")
	keys, err := c.queries.ListAlertNotifications(t.Context(), "finance.large_debit:")
	if err != nil || strings.Join(keys, " ") != strings.Join(sortedKeys(alerts.LargeDebitKey(pending.Id), alerts.LargeDebitKey(booked.Id)), " ") {
		t.Fatalf("claims = %v, %v", keys, err)
	}
}

func sortedKeys(a, b string) []string {
	if a > b {
		return []string{b, a}
	}
	return []string{a, b}
}
