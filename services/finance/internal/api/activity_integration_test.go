package api

import (
	"net/http"
	"testing"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

func TestFinanceChangesAreRecordedInActivity(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	// Ensuring the Cash account at startup is the service's own change.
	startup := c.recorder.since(&seen)
	if eventTypes(startup) != accountCreated || startup[0].Actor != activity.System("finance") || startup[0].Summary != "Account “Cash” created (cash, EUR)" {
		t.Fatalf("startup = %+v", startup)
	}
	cause := googleuuid.New()

	// The owner records a bakery purchase in the Dashboard.
	bakery := expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", map[string]any{"amount": eur(-1200), "counterparty": "Bäckerei", "notes": "private note"},
		http.StatusCreated, activity.ActorHeader, "user", activity.CorrelationIDHeader, "corr-dashboard", activity.CausationIDHeader, cause.String())
	events := c.recorder.since(&seen)
	if eventTypes(events) != transactionCreated {
		t.Fatalf("events = %s", eventTypes(events))
	}
	created := events[0]
	if created.Actor != activity.User() || created.CorrelationID != "corr-dashboard" || created.CausationID == nil || *created.CausationID != cause ||
		created.Source != "finance" || created.Subject != (activity.Subject{Type: "finance_transaction", ID: bakery.Id.String()}) || created.Outcome != activity.Succeeded ||
		created.ID != activity.EventID(transactionCreated, bakery.Id.String()) {
		t.Fatalf("created = %+v", created)
	}
	if created.Summary != "Spent €12.00 at Bäckerei (Cash)" || created.Data["accountName"] != "Cash" || created.Data["amount"] != float64(-1200) ||
		created.Data["counterparty"] != "Bäckerei" || created.Data["notes"] != nil || created.Data["source"] != "manual" {
		t.Fatalf("created summary/data = %q %v", created.Summary, created.Data)
	}

	// The AI records income; an update that changes nothing records nothing.
	salary := expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", map[string]any{"amount": eur(250000), "counterparty": "ACME GmbH", "status": "pending"},
		http.StatusCreated, activity.ActorHeader, "agent:codex-inbox-processor")
	if events = c.recorder.since(&seen); len(events) != 1 || events[0].Summary != "Received €2,500.00 from ACME GmbH (Cash), pending" || events[0].Actor != activity.Agent("codex-inbox-processor") {
		t.Fatalf("income = %+v", events)
	}
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+salary.Id.String(), map[string]any{"counterparty": "ACME GmbH"}, http.StatusOK)
	if events = c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("a no-op update recorded %s", eventTypes(events))
	}
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+salary.Id.String(), map[string]any{"status": "booked", "notes": "October"}, http.StatusOK)
	if events = c.recorder.since(&seen); eventTypes(events) != transactionUpdated || events[0].Summary != "Transaction “ACME GmbH” (€2,500.00, Cash) updated: status, notes" {
		t.Fatalf("updated = %+v", events)
	}
	if status, _ := c.do(http.MethodDelete, "/v1/transactions/"+bakery.Id.String(), nil); status != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	if events = c.recorder.since(&seen); eventTypes(events) != transactionDeleted || events[0].Summary != "Transaction “Bäckerei” (-€12.00, Cash) deleted" || events[0].Actor != activity.Dev("test") {
		t.Fatalf("deleted = %+v", events)
	}

	// Accounts, categories, rules, a re-apply, and an import.
	giro := c.account("Girokonto", "bank", "EUR", 0)
	expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+giro.Id.String(), map[string]any{"name": "Giro"}, http.StatusOK)
	expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+giro.Id.String(), map[string]any{"archived": true}, http.StatusOK)
	expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+giro.Id.String(), map[string]any{"archived": false}, http.StatusOK)
	groceries := c.category("Groceries", "expense")
	expect[generated.Category](c, http.MethodPatch, "/v1/categories/"+groceries.Id.String(), map[string]any{"color": "#00ff00"}, http.StatusOK)
	rule := c.rule(groceries.Id.String(), "counterparty", "contains", "REWE")
	expect[generated.CategoryRule](c, http.MethodPatch, "/v1/categories/rules/"+rule.Id.String(), map[string]any{"priority": 1}, http.StatusOK)
	expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", nil, http.StatusOK)
	expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv?accountId="+giro.Id.String(), csvBody("date,amount,counterparty\n2026-10-01,-5.00,REWE\n2026-10-02,-6.00,Lidl\n"), http.StatusOK)
	if status, _ := c.do(http.MethodDelete, "/v1/categories/rules/"+rule.Id.String(), nil); status != http.StatusNoContent {
		t.Fatal("rule delete failed")
	}
	events = c.recorder.since(&seen)
	want := accountCreated + " " + accountUpdated + " " + accountArchived + " " + accountRestored + " " + categoryCreated + " " + categoryUpdated + " " +
		ruleCreated + " " + ruleUpdated + " " + rulesApplied + " " + importCompleted + " " + ruleDeleted
	if eventTypes(events) != want {
		t.Fatalf("recorded %q\nwant     %q", eventTypes(events), want)
	}
	summaries := []string{
		"Account “Girokonto” created (bank, EUR)", "Account “Giro” updated: name", "Account “Giro” archived", "Account “Giro” restored",
		"Category “Groceries” created (expense)", "Category “Groceries” updated: color",
		"Category rule created: counterparty contains “REWE” → Groceries", "Category rule updated: counterparty contains “REWE” → Groceries",
		"Category rules applied to uncategorized transactions: 0 categorized", "Imported 2 transactions into Giro (0 duplicates, 0 failed)",
		"Category rule deleted: counterparty contains “REWE” → Groceries",
	}
	for index, summary := range summaries {
		if events[index].Summary != summary {
			t.Errorf("event %d (%s) summary = %q, want %q", index, events[index].Type, events[index].Summary, summary)
		}
	}
	imported := events[9]
	if imported.Data["imported"] != float64(2) || imported.Data["categorized"] != float64(1) || imported.Data["accountId"] != giro.Id.String() || imported.Subject.Type != "finance_import" {
		t.Fatalf("import event = %+v", imported)
	}
	// An import whose rows all fail is a failed outcome.
	expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv?accountId="+giro.Id.String(), csvBody("date,amount\nbad,1\n"), http.StatusOK)
	if events = c.recorder.since(&seen); eventTypes(events) != importFailed || events[0].Outcome != activity.Failed || events[0].Summary != "CSV import into Giro failed: 1 row could not be imported" {
		t.Fatalf("failed import = %+v", events)
	}
}

func TestIdempotentReplayRecordsNothingNew(t *testing.T) {
	c := newAPIClient(t)
	seen := len(c.recorder.all())
	body := map[string]any{"amount": eur(-500), "counterparty": "Kiosk"}
	first := expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", body, http.StatusCreated, "Idempotency-Key", "k-1")
	expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", body, http.StatusCreated, "Idempotency-Key", "k-1")
	events := c.recorder.since(&seen)
	// The replay re-sends the same deterministic event id, which the
	// Activity service stores once.
	if len(events) != 2 || events[0].ID != events[1].ID || events[0].ID != activity.EventID(transactionCreated, first.Id.String()) {
		t.Fatalf("events = %+v", events)
	}
}
