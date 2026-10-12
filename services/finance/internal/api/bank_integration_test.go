package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

const connectionID = "3b1f6a52-9c1e-4d0b-8f3a-2a6b7c8d9e01"

func bankSync(accounts []map[string]any, transactions []map[string]any) map[string]any {
	return map[string]any{
		"connectionId": connectionID, "provider": "enablebanking", "institution": "Deutsche Bank",
		"window": map[string]any{"from": "2026-10-01", "to": "2026-10-10"}, "accounts": accounts, "transactions": transactions,
	}
}

func giroInput(booked, available int64) map[string]any {
	return map[string]any{"externalId": "acc-1", "name": "Girokonto", "kind": "bank", "currency": "EUR", "ibanMasked": "DE23…2300",
		"bookedBalance":    map[string]any{"amount": booked, "currency": "EUR", "date": "2026-10-10"},
		"availableBalance": map[string]any{"amount": available, "currency": "EUR", "date": "2026-10-10"}}
}

var cardInput = map[string]any{"externalId": "acc-2", "name": "Kreditkarte", "kind": "card", "currency": "EUR"}

func bankTx(account, id, status, date string, amount int64, counterparty string, extra ...string) map[string]any {
	item := map[string]any{"accountExternalId": account, "externalId": id, "status": status, "bookingDate": date, "amount": eur(amount)}
	if counterparty != "" {
		item["counterparty"] = counterparty
	}
	for index := 0; index+1 < len(extra); index += 2 {
		item[extra[index]] = extra[index+1]
	}
	return item
}

func (c apiClient) syncBank(body map[string]any) generated.BankImportResult {
	c.t.Helper()
	return expect[generated.BankImportResult](c, http.MethodPost, "/v1/imports/bank", body, http.StatusOK, activity.ActorHeader, "system:banking")
}

func (c apiClient) byExternalID(id string) generated.Transaction {
	c.t.Helper()
	for _, item := range c.list("source=bank&limit=100").Items {
		if item.Source.ExternalId != nil && *item.Source.ExternalId == id {
			return item
		}
	}
	c.t.Fatalf("no bank transaction %s", id)
	return generated.Transaction{}
}

func TestBankSyncsUpsertAccountsAndTransactions(t *testing.T) {
	c := newAPIClient(t)
	c.account("Girokonto", "bank", "EUR", 0) // a manual account already has the bank's name
	groceries := c.category("Groceries", "expense")
	c.rule(groceries.Id.String(), "counterparty", "contains", "rewe")
	seen := len(c.recorder.all())

	first := c.syncBank(bankSync([]map[string]any{giroInput(123456, 120000), cardInput}, []map[string]any{
		bankTx("acc-1", "t1", "booked", "2026-10-02", -2345, "REWE Markt", "description", "Einkauf", "merchantCategoryCode", "5411", "bankReference", "REF-1"),
		bankTx("acc-1", "t2", "pending", "2026-10-08", -1999, "Amazon"),
		bankTx("acc-1", "t3", "booked", "2026-10-01", 250000, "ACME GmbH", "description", "Gehalt"),
		bankTx("acc-1", "t4", "booked", "2026-10-03", 0, "Bank", "description", "Zero adjustment"),
		bankTx("acc-2", "t5", "pending", "2026-10-09", -6000, "Shell"),
		bankTx("acc-1", "t6", "pending", "2026-09-20", -100, "Old pending"),
	}))
	if first.Accounts.Created != 2 || first.Accounts.Updated != 0 || first.Transactions.Created != 5 || first.Transactions.Skipped != 1 ||
		first.Transactions.Updated != 0 || first.Transactions.RemovedPending != 0 || len(first.AccountIds) != 2 || first.AccountIds[0].ExternalId != "acc-1" {
		t.Fatalf("first sync = %+v", first)
	}
	giroID := first.AccountIds[0].Id.String()
	giro := expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+giroID, nil, http.StatusOK)
	// The bank's balance of 10 October has every booking so far; the
	// balance adds the pending ones, and it is as of the sync.
	if giro.Name != "Girokonto (Deutsche Bank)" || giro.Kind != generated.AccountKindBank || giro.BookedBalance.Amount != 123456 || giro.Balance.Amount != 123456-2099 ||
		giro.BankBalance == nil || giro.BankBalance.Amount != 123456 || giro.BankBalance.Date.String() != "2026-10-10" || giro.Source.SyncedAt == nil ||
		giro.BalanceAsOf == nil || giro.BalanceAsOf.String() != c.handler.ownerDay(*giro.Source.SyncedAt).Format(time.DateOnly) ||
		giro.Available == nil || giro.Available.Amount != 120000 || giro.Pending.Amount != -2099 || giro.Source.Kind != generated.AccountSourceKindBank ||
		giro.Source.ConnectionId.String() != connectionID || *giro.Source.ExternalId != "acc-1" || *giro.Source.Provider != "enablebanking" ||
		*giro.Source.Institution != "Deutsche Bank" || *giro.Source.IbanMasked != "DE23…2300" || giro.Source.SyncedAt == nil {
		t.Fatalf("giro = %+v", giro)
	}
	card := expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+first.AccountIds[1].Id.String(), nil, http.StatusOK)
	// Without a reported balance, a bank account's balance is computed.
	if card.Name != "Kreditkarte" || card.Kind != generated.AccountKindCard || card.BookedBalance.Amount != 0 || card.Balance.Amount != -6000 ||
		card.BankBalance != nil || card.BalanceAsOf == nil || card.Available != nil {
		t.Fatalf("card = %+v", card)
	}
	shop := c.byExternalID("t1")
	if !shop.ReadOnly || shop.Source.Kind != generated.TransactionSourceKindBank || shop.Source.ConnectionId.String() != connectionID ||
		*shop.MerchantCategoryCode != "5411" || *shop.BankReference != "REF-1" || *shop.CategoryId != groceries.Id || *shop.CategorizedBy != generated.TransactionCategorizedByRule {
		t.Fatalf("shop = %+v", shop)
	}

	// The owner edits what is theirs; the bank's fields stay read-only.
	dining := c.category("Dining", "expense")
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+shop.Id.String(), map[string]any{"categoryId": dining.Id, "notes": "party"}, http.StatusOK)
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+c.byExternalID("t2").Id.String(), map[string]any{"notes": "birthday present"}, http.StatusOK)
	c.expectError(http.MethodPatch, "/v1/transactions/"+shop.Id.String(), map[string]any{"description": "x"}, http.StatusConflict, "managed_by_source")
	c.expectError(http.MethodDelete, "/v1/transactions/"+shop.Id.String(), nil, http.StatusConflict, "managed_by_source")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"accountId": giroID, "amount": eur(-1)}, http.StatusConflict, "managed_by_source")
	c.expectError(http.MethodPost, "/v1/imports/csv?accountId="+giroID, csvBody("date,amount\n2026-10-01,-1\n"), http.StatusConflict, "managed_by_source")
	c.expectError(http.MethodPatch, "/v1/accounts/"+giroID, map[string]any{"currency": "USD"}, http.StatusConflict, "managed_by_source")
	expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+giroID, map[string]any{"name": "DB Giro"}, http.StatusOK)

	// The next sync: the bank changed t1's text, t3 is the same, the pending
	// Amazon order was booked under a new id, and the pending Shell payment
	// disappeared. The pending one from September is outside the window.
	second := c.syncBank(bankSync([]map[string]any{giroInput(118000, 115000), cardInput}, []map[string]any{
		bankTx("acc-1", "t1", "booked", "2026-10-02", -2345, "REWE Markt", "description", "Einkauf REWE 123", "merchantCategoryCode", "5411", "bankReference", "REF-1"),
		bankTx("acc-1", "t3", "booked", "2026-10-01", 250000, "ACME GmbH", "description", "Gehalt"),
		bankTx("acc-1", "t7", "booked", "2026-10-09", -1999, "AMAZON "),
	}))
	if second.Accounts.Created != 0 || second.Accounts.Updated != 2 || second.Transactions.Created != 1 || second.Transactions.Updated != 1 ||
		second.Transactions.Unchanged != 1 || second.Transactions.RemovedPending != 2 || second.AccountIds[0].Id.String() != giroID {
		t.Fatalf("second sync = %+v", second)
	}
	giro = expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+giroID, nil, http.StatusOK)
	if giro.Name != "DB Giro" || giro.BookedBalance.Amount != 118000 || giro.Balance.Amount != 118000-100 || giro.Available.Amount != 115000 || giro.Pending.Amount != -100 {
		t.Fatalf("giro after re-sync = %+v", giro)
	}
	shop = c.byExternalID("t1")
	if *shop.Description != "Einkauf REWE 123" || *shop.CategoryId != dining.Id || *shop.Notes != "party" {
		t.Fatalf("owner edits did not survive: %+v", shop)
	}
	amazon := c.byExternalID("t7")
	if amazon.Status != generated.Booked || amazon.Notes == nil || *amazon.Notes != "birthday present" {
		t.Fatalf("booked amazon = %+v", amazon)
	}
	pending := c.list("source=bank&status=pending").Items
	if len(pending) != 1 || *pending[0].Source.ExternalId != "t6" {
		t.Fatalf("pending after re-sync = %+v", pending)
	}
	// A pending transaction booked under its own id simply becomes booked.
	third := c.syncBank(bankSync([]map[string]any{giroInput(118000, 115000)}, []map[string]any{bankTx("acc-1", "t6", "booked", "2026-09-21", -100, "Old pending")}))
	if third.Transactions.Updated != 1 || third.Transactions.RemovedPending != 0 || c.byExternalID("t6").Status != generated.Booked {
		t.Fatalf("third sync = %+v", third)
	}

	events := c.recorder.since(&seen)
	var syncs []activity.Event
	for _, event := range events {
		if event.Type == bankImportCompleted {
			syncs = append(syncs, event)
		}
	}
	if len(syncs) != 3 || syncs[0].Actor != activity.System("banking") || syncs[0].Summary != "Deutsche Bank sync: 5 new, 0 updated (2 accounts)" ||
		syncs[1].Summary != "Deutsche Bank sync: 1 new, 1 updated, 2 pending removed (2 accounts)" || syncs[0].Subject.Type != "finance_bank_connection" ||
		syncs[0].Data["counterparty"] != nil || syncs[0].Data["created"] != float64(5) {
		t.Fatalf("bank sync events = %+v", syncs)
	}
}

func TestBankSyncsAreValidatedAndAtomic(t *testing.T) {
	c := newAPIClient(t)
	refuse := func(body map[string]any, code string) {
		t.Helper()
		status, raw := c.do(http.MethodPost, "/v1/imports/bank", body, activity.ActorHeader, "system:banking")
		if status != http.StatusBadRequest || !contains(raw, code) {
			t.Fatalf("%d %s, want 400 %s", status, raw, code)
		}
	}
	refuse(bankSync([]map[string]any{cardInput}, []map[string]any{bankTx("acc-9", "t1", "booked", "2026-10-02", -1, "")}), "unknown_account")
	refuse(bankSync([]map[string]any{cardInput}, []map[string]any{{"accountExternalId": "acc-2", "externalId": "t1", "status": "booked", "bookingDate": "2026-10-02", "amount": map[string]any{"amount": -1, "currency": "USD"}}}), "currency_mismatch")
	badBalance := giroInput(1, 1)
	badBalance["bookedBalance"] = map[string]any{"amount": 1, "currency": "USD", "date": "2026-10-10"}
	refuse(bankSync([]map[string]any{badBalance}, []map[string]any{}), "currency_mismatch")
	refuse(bankSync([]map[string]any{cardInput}, []map[string]any{bankTx("acc-2", "t1", "booked", "2026-10-02", -1, ""), bankTx("acc-2", "t1", "booked", "2026-10-02", -2, "")}), "invalid_request")
	refuse(bankSync([]map[string]any{cardInput, cardInput}, []map[string]any{}), "invalid_request")
	refuse(bankSync([]map[string]any{{"externalId": "x", "name": "X", "kind": "cash", "currency": "EUR"}}, []map[string]any{}), "invalid_request")
	refuse(bankSync([]map[string]any{cardInput}, []map[string]any{bankTx("acc-2", "t1", "cleared", "2026-10-02", -1, "")}), "invalid_request")
	reversed := bankSync([]map[string]any{cardInput}, []map[string]any{})
	reversed["window"] = map[string]any{"from": "2026-10-10", "to": "2026-10-01"}
	refuse(reversed, "invalid_request")
	missing := bankSync([]map[string]any{cardInput}, []map[string]any{})
	delete(missing, "transactions")
	refuse(missing, "invalid_request")
	// Nothing of a refused sync was stored.
	if accounts := expect[generated.AccountList](c, http.MethodGet, "/v1/accounts?includeArchived=true", nil, http.StatusOK).Items; len(accounts) != 1 {
		t.Fatalf("a refused sync stored accounts: %+v", accounts)
	}
	// A later sync cannot change an account's currency; a transaction may
	// name an account the connection already has.
	c.syncBank(bankSync([]map[string]any{cardInput}, []map[string]any{}))
	usdCard := map[string]any{"externalId": "acc-2", "name": "Kreditkarte", "kind": "card", "currency": "USD"}
	refuse(bankSync([]map[string]any{usdCard}, []map[string]any{}), "currency_mismatch")
	if result := c.syncBank(bankSync([]map[string]any{}, []map[string]any{bankTx("acc-2", "t1", "booked", "2026-10-02", -500, "Shell")})); result.Transactions.Created != 1 {
		t.Fatalf("known account sync = %+v", result)
	}
}

func TestRemovingABankConnectionArchivesItsAccounts(t *testing.T) {
	c := newAPIClient(t)
	result := c.syncBank(bankSync([]map[string]any{giroInput(5000, 5000), cardInput}, []map[string]any{bankTx("acc-1", "t1", "booked", "2026-10-02", -500, "Kiosk")}))
	seen := len(c.recorder.all())
	for attempt := 0; attempt < 2; attempt++ {
		if status, raw := c.do(http.MethodDelete, "/v1/imports/bank/"+connectionID, nil, activity.ActorHeader, "system:banking"); status != http.StatusNoContent {
			t.Fatalf("remove: %d %s", status, raw)
		}
	}
	for _, id := range result.AccountIds {
		if account := expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+id.Id.String(), nil, http.StatusOK); !account.Archived {
			t.Fatalf("account %s not archived", id.ExternalId)
		}
	}
	if items := c.list("source=bank").Items; len(items) != 1 {
		t.Fatalf("history after removal = %+v", items)
	}
	events := c.recorder.since(&seen)
	if eventTypes(events) != bankConnectionRemoved || events[0].Summary != "Bank connection to Deutsche Bank removed: 2 accounts archived" {
		t.Fatalf("removal events = %+v", events)
	}
}

func contains(raw []byte, text string) bool { return strings.Contains(string(raw), text) }
