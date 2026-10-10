package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

func TestManualTransactionsAreCreatedChangedAndDeleted(t *testing.T) {
	c := newAPIClient(t)
	cash := c.cash()

	// No account: the built-in Cash account; no date: today in Berlin.
	bakery := c.spend(map[string]any{"amount": eur(-1200), "counterparty": " Bäckerei Schmidt ", "notes": "croissants"})
	if bakery.AccountId != cash.Id || bakery.BookingDate.String() != "2026-10-11" || bakery.Status != generated.Booked ||
		*bakery.Counterparty != "Bäckerei Schmidt" || *bakery.Notes != "croissants" || bakery.Description != nil || bakery.ReadOnly ||
		bakery.Source.Kind != generated.TransactionSourceKindManual || bakery.Source.ExternalId != nil || bakery.CategoryId != nil || bakery.CategorizedBy != nil {
		t.Fatalf("bakery = %+v", bakery)
	}
	giro := c.account("Girokonto", "bank", "EUR", 0)
	usd := c.account("Travel", "card", "USD", 0)
	groceries := c.category("Groceries", "expense")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"accountId": usd.Id, "amount": eur(-100)}, http.StatusBadRequest, "currency_mismatch")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"amount": eur(0)}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"counterparty": "x"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"accountId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", "amount": eur(-1)}, http.StatusBadRequest, "unknown_account")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"amount": eur(-1), "categoryId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}, http.StatusBadRequest, "unknown_category")
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"amount": eur(-1), "status": "cleared"}, http.StatusBadRequest, "invalid_request")

	// An Idempotency-Key replay returns the original; a different body is refused.
	body := map[string]any{"accountId": giro.Id, "amount": eur(-4599), "bookingDate": "2026-10-05", "valueDate": "2026-10-06", "counterparty": "REWE", "categoryId": groceries.Id}
	first := expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", body, http.StatusCreated, "Idempotency-Key", "capture-1")
	replay := expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", body, http.StatusCreated, "Idempotency-Key", "capture-1")
	if replay.Id != first.Id || first.ValueDate.String() != "2026-10-06" || *first.CategoryId != groceries.Id || *first.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("first = %+v replay = %+v", first, replay)
	}
	body["amount"] = eur(-1)
	status, raw := c.do(http.MethodPost, "/v1/transactions", body, "Idempotency-Key", "capture-1")
	if status != http.StatusConflict || !json.Valid(raw) {
		t.Fatalf("conflicting replay: %d %s", status, raw)
	}

	// Every field of a manual transaction changes; null clears.
	target := "/v1/transactions/" + first.Id.String()
	changed := expect[generated.Transaction](c, http.MethodPatch, target, map[string]any{
		"accountId": cash.Id, "amount": eur(-4000), "bookingDate": "2026-10-04", "valueDate": nil, "status": "pending",
		"counterparty": "Edeka", "description": "party", "notes": "for Saturday", "categoryId": nil,
	}, http.StatusOK)
	if changed.AccountId != cash.Id || changed.Amount.Amount != -4000 || changed.BookingDate.String() != "2026-10-04" || changed.ValueDate != nil ||
		changed.Status != generated.Pending || *changed.Counterparty != "Edeka" || *changed.Description != "party" ||
		*changed.Notes != "for Saturday" || changed.CategoryId != nil || *changed.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("changed = %+v", changed)
	}
	c.expectError(http.MethodPatch, target, map[string]any{"accountId": usd.Id}, http.StatusBadRequest, "currency_mismatch")
	c.expectError(http.MethodPatch, target, map[string]any{"amount": nil}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, target, map[string]any{"unknown": 1}, http.StatusBadRequest, "invalid_request")
	if got := expect[generated.Transaction](c, http.MethodGet, target, nil, http.StatusOK); got.Amount.Amount != -4000 {
		t.Fatalf("get = %+v", got)
	}
	// Moving it with its new currency to the USD account works.
	moved := expect[generated.Transaction](c, http.MethodPatch, target, map[string]any{"accountId": usd.Id, "amount": map[string]any{"amount": -4400, "currency": "USD"}}, http.StatusOK)
	if moved.AccountId != usd.Id || moved.Amount.Currency != "USD" {
		t.Fatalf("moved = %+v", moved)
	}

	if status, raw := c.do(http.MethodDelete, target, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	c.expectError(http.MethodGet, target, nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodDelete, target, nil, http.StatusNotFound, "not_found")
}

func TestImportedTransactionsAreReadOnlyExceptCategoryAndNotes(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 0)
	dining := c.category("Dining", "expense")
	result := expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv?accountId="+giro.Id.String(),
		csvBody("date,amount,counterparty,description,external_id\n2026-10-02,-8.90,Pizzeria,Dinner,bank-1\n"), http.StatusOK)
	if result.Imported != 1 {
		t.Fatalf("import = %+v", result)
	}
	imported := c.list("source=csv").Items[0]
	if !imported.ReadOnly || imported.Source.Kind != generated.TransactionSourceKindCsv || *imported.Source.ExternalId != "bank-1" || imported.Status != generated.Booked {
		t.Fatalf("imported = %+v", imported)
	}
	target := "/v1/transactions/" + imported.Id.String()
	for _, field := range []map[string]any{{"description": "x"}, {"amount": eur(-1)}, {"bookingDate": "2026-10-01"}, {"counterparty": nil}, {"notes": "ok", "status": "pending"}} {
		c.expectError(http.MethodPatch, target, field, http.StatusConflict, "managed_by_source")
	}
	edited := expect[generated.Transaction](c, http.MethodPatch, target, map[string]any{"categoryId": dining.Id, "notes": "with Anna"}, http.StatusOK)
	if *edited.CategoryId != dining.Id || *edited.Notes != "with Anna" || *edited.Description != "Dinner" {
		t.Fatalf("edited = %+v", edited)
	}
	c.expectError(http.MethodDelete, target, nil, http.StatusConflict, "managed_by_source")
}

func TestTransactionsAreSearchedFilteredAndPaged(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 0)
	groceries := c.category("Groceries", "expense")
	gid := giro.Id.String()
	c.spend(map[string]any{"accountId": gid, "amount": eur(-2345), "bookingDate": "2026-10-01", "counterparty": "REWE Markt", "description": "Wocheneinkauf", "categoryId": groceries.Id})
	c.spend(map[string]any{"accountId": gid, "amount": eur(250000), "bookingDate": "2026-10-01", "counterparty": "ACME GmbH", "description": "Gehalt Oktober"})
	c.spend(map[string]any{"amount": eur(-320), "bookingDate": "2026-10-02", "counterparty": "Bäckerei", "notes": "Brötchen 50% off"})
	c.spend(map[string]any{"amount": eur(-1599), "bookingDate": "2026-10-03", "counterparty": "Kino", "status": "pending"})
	c.spend(map[string]any{"accountId": gid, "amount": eur(-95000), "bookingDate": "2026-09-30", "description": "Miete Oktober"})
	expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv?accountId="+gid,
		csvBody("date,amount,counterparty\n2026-10-04,-12.00,REWE To Go\n"), http.StatusOK)

	count := func(query string) int { t.Helper(); return len(c.list(query).Items) }
	cases := map[string]int{
		"":                                    6,
		"q=rewe":                              2,
		"q=" + url.QueryEscape("REWE markt"):  1,
		"q=oktober":                           2,
		"q=" + url.QueryEscape("50%"):         1,
		"q=" + url.QueryEscape("5_%"):         0,
		"q=brötchen":                          1,
		"accountId=" + gid:                    4,
		"categoryId=" + groceries.Id.String(): 1,
		"categoryId=uncategorized":            5,
		"direction=in":                        1,
		"direction=out":                       5,
		"amountMin=1500&amountMax=2400":       2,
		"amountMin=100000":                    1,
		"status=pending":                      1,
		"status=booked":                       5,
		"source=csv":                          1,
		"source=manual":                       5,
		"source=bank":                         0,
		"from=2026-10-02&to=2026-10-03":       2,
		"to=2026-09-30":                       1,
		"q=rewe&source=manual":                1,
	}
	for query, want := range cases {
		if got := count(query); got != want {
			t.Errorf("%q: %d transactions, want %d", query, got, want)
		}
	}
	for _, query := range []string{"categoryId=food", "direction=sideways", "amountMin=5&amountMax=1", "amountMin=-1", "status=done", "source=paypal", "limit=0", "limit=101", "q=%20"} {
		c.expectError(http.MethodGet, "/v1/transactions?"+query, nil, http.StatusBadRequest, "invalid_request")
	}
	c.expectError(http.MethodGet, "/v1/transactions?cursor=bm9wZQ", nil, http.StatusBadRequest, "invalid_cursor")

	// Newest booking date first; pages of two walk every transaction once.
	all := c.list("").Items
	if all[0].BookingDate.String() != "2026-10-04" || all[len(all)-1].BookingDate.String() != "2026-09-30" {
		t.Fatalf("order = %v … %v", all[0].BookingDate, all[len(all)-1].BookingDate)
	}
	var paged []string
	query := "limit=2"
	for pages := 0; pages < 10; pages++ {
		page := c.list(query)
		for _, item := range page.Items {
			paged = append(paged, item.Id.String())
		}
		if page.NextCursor == nil {
			break
		}
		query = "limit=2&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if len(paged) != len(all) {
		t.Fatalf("paged %d of %d", len(paged), len(all))
	}
	for index, item := range all {
		if paged[index] != item.Id.String() {
			t.Fatalf("page order differs at %d: %v", index, paged)
		}
	}
	// A filter keeps its cursor consistent.
	first := c.list("direction=out&limit=3")
	if first.NextCursor == nil || len(first.Items) != 3 {
		t.Fatalf("first filtered page = %+v", first)
	}
	rest := c.list(fmt.Sprintf("direction=out&limit=3&cursor=%s", url.QueryEscape(*first.NextCursor)))
	if len(rest.Items) != 2 || rest.NextCursor != nil {
		t.Fatalf("second filtered page = %+v", rest)
	}
}
