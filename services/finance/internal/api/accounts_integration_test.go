package api

import (
	"net/http"
	"sync"
	"testing"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/testdb"
)

func TestTheBuiltInCashAccountIsEnsuredOnce(t *testing.T) {
	c := newAPIClient(t)
	cash := c.cash()
	if cash.Name != "Cash" || cash.Kind != generated.AccountKindCash || cash.Currency != "EUR" || cash.Archived || cash.Source.Kind != generated.AccountSourceKindManual {
		t.Fatalf("cash = %+v", cash)
	}
	// Ensuring again, also concurrently, finds the same account.
	var wg sync.WaitGroup
	ids := make([]string, 4)
	for index := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			account, err := c.handler.EnsureCashAccount(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			ids[index] = account.ID.String()
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != cash.Id.String() {
			t.Fatalf("ensured ids = %v, want %s", ids, cash.Id)
		}
	}
	c.account("Girokonto", "bank", "EUR", 0)
	accounts := expect[generated.AccountList](c, http.MethodGet, "/v1/accounts", nil, http.StatusOK).Items
	if len(accounts) != 2 || !accounts[0].BuiltIn || accounts[1].Name != "Girokonto" {
		t.Fatalf("accounts = %+v, want Cash first", accounts)
	}

	// The owner may rename Cash and change its opening balance, but it stays
	// an active cash account.
	target := "/v1/accounts/" + cash.Id.String()
	renamed := expect[generated.Account](c, http.MethodPatch, target, map[string]any{"name": "Wallet", "openingBalanceMinor": 4000}, http.StatusOK)
	if renamed.Name != "Wallet" || renamed.Balance.Amount != 4000 || !renamed.BuiltIn {
		t.Fatalf("renamed = %+v", renamed)
	}
	c.expectError(http.MethodPatch, target, map[string]any{"archived": true}, http.StatusConflict, "built_in_account")
	c.expectError(http.MethodPatch, target, map[string]any{"kind": "bank"}, http.StatusConflict, "built_in_account")
	if again, err := c.handler.EnsureCashAccount(t.Context()); err != nil || again.ID.Bytes != cash.Id {
		t.Fatalf("ensure after rename = %+v %v", again, err)
	}
}

func TestAnAccountTheOwnerCalledCashBecomesTheBuiltInOne(t *testing.T) {
	pool := testdb.Open(t, "")
	queries := store.New(pool)
	existing, err := queries.CreateAccount(t.Context(), store.CreateAccountParams{Name: "cash", Currency: "EUR", Kind: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpdateAccount(t.Context(), store.UpdateAccountParams{ID: existing.ID, Archived: pointer(true)}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, Options{DefaultCurrency: "USD"})
	cash, err := handler.EnsureCashAccount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cash.ID != existing.ID || cash.Kind != "cash" || cash.SystemKey == nil || cash.ArchivedAt.Valid || cash.Currency != "EUR" {
		t.Fatalf("adopted = %+v", cash)
	}
}

func TestAccountsShowBalancesAndArchive(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 100000)
	if giro.Kind != generated.AccountKindBank || giro.Balance.Amount != 100000 || giro.OpeningBalance.Amount != 100000 || giro.TransactionCount != 0 || giro.BuiltIn {
		t.Fatalf("giro = %+v", giro)
	}
	c.expectError(http.MethodPost, "/v1/accounts", map[string]any{"name": "Girokonto", "currency": "EUR"}, http.StatusConflict, "account_name_taken")
	c.expectError(http.MethodPost, "/v1/accounts", map[string]any{"name": "X", "currency": "euro"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/accounts", map[string]any{"name": " ", "currency": "EUR"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/accounts", map[string]any{"name": "Y", "currency": "EUR", "kind": "crypto"}, http.StatusBadRequest, "invalid_request")
	other := expect[generated.Account](c, http.MethodPost, "/v1/accounts", map[string]any{"name": "Misc", "currency": "usd"}, http.StatusCreated)
	if other.Kind != generated.AccountKindOther || other.Currency != "USD" {
		t.Fatalf("defaults = %+v", other)
	}

	id := giro.Id.String()
	c.spend(map[string]any{"accountId": id, "amount": eur(-2500), "bookingDate": "2026-10-01"})
	c.spend(map[string]any{"accountId": id, "amount": eur(300000), "bookingDate": "2026-10-02"})
	c.spend(map[string]any{"accountId": id, "amount": eur(-999), "bookingDate": "2026-10-03", "status": "pending"})
	giro = expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+id, nil, http.StatusOK)
	if giro.Balance.Amount != 100000-2500+300000 || giro.Pending.Amount != -999 || giro.TransactionCount != 3 {
		t.Fatalf("balances = %+v", giro)
	}
	c.expectError(http.MethodPatch, "/v1/accounts/"+id, map[string]any{"currency": "USD"}, http.StatusConflict, "account_has_transactions")
	c.expectError(http.MethodPatch, "/v1/accounts/"+id, map[string]any{"name": "Misc"}, http.StatusConflict, "account_name_taken")
	c.expectError(http.MethodPatch, "/v1/accounts/"+id, map[string]any{}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, "/v1/accounts/"+id, map[string]any{"name": nil}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/accounts/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", nil, http.StatusNotFound, "not_found")

	archived := expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+id, map[string]any{"archived": true}, http.StatusOK)
	if !archived.Archived || archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v", archived)
	}
	for _, account := range expect[generated.AccountList](c, http.MethodGet, "/v1/accounts", nil, http.StatusOK).Items {
		if account.Id == giro.Id {
			t.Fatal("an archived account is listed")
		}
	}
	if all := expect[generated.AccountList](c, http.MethodGet, "/v1/accounts?includeArchived=true", nil, http.StatusOK).Items; len(all) != 3 {
		t.Fatalf("with archived: %d accounts", len(all))
	}
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"accountId": id, "amount": eur(-100)}, http.StatusConflict, "account_archived")
	// History stays readable, and the account can come back.
	if items := c.list("accountId=" + id).Items; len(items) != 3 {
		t.Fatalf("archived account history = %d", len(items))
	}
	restored := expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+id, map[string]any{"archived": false, "kind": "savings"}, http.StatusOK)
	if restored.Archived || restored.Kind != generated.AccountKindSavings {
		t.Fatalf("restored = %+v", restored)
	}
	// An account without transactions may change its currency.
	if changed := expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+other.Id.String(), map[string]any{"currency": "EUR"}, http.StatusOK); changed.Currency != "EUR" {
		t.Fatalf("currency = %+v", changed)
	}
}
