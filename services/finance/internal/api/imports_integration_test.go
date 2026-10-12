package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

func TestCsvImportGoesIntoTheChosenAccount(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 0)
	groceries := c.category("Groceries", "expense")
	c.rule(groceries.Id.String(), "any", "contains", "rewe")
	file := csvBody("Date;Amount;Counterparty;Description;Category;External_ID\n" +
		"01.10.2026;-23,45;REWE Markt;Einkauf;;r1\n" +
		"02.10.2026;2.500,00;ACME;Gehalt;;r2\n" +
		"03.10.2026;-3,20;Bäckerei;;groceries;\n" +
		"03.10.2026;-1,00;X;;;\n" +
		"03.10.2026;-1,00;X;;;\n" +
		"oops;-1;Y;;;\n")
	target := "/v1/imports/csv?accountId=" + giro.Id.String()
	result := expect[generated.CsvImport](c, http.MethodPost, target, file, http.StatusOK)
	if result.Status != "completed" || result.Imported != 5 || result.Duplicates != 0 || result.Failed != 1 || result.Categorized != 1 ||
		result.AccountId == nil || *result.AccountId != giro.Id || len(result.Errors) != 1 || result.Errors[0].Row != 7 {
		t.Fatalf("import = %+v", result)
	}
	if account := expect[generated.Account](c, http.MethodGet, "/v1/accounts/"+giro.Id.String(), nil, http.StatusOK); account.Balance.Amount != -2345+250000-320-200 {
		t.Fatalf("balance after import = %+v", account)
	}
	bakery := c.list("q=bäckerei").Items[0]
	if *bakery.CategoryId != groceries.Id || *bakery.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("category by name = %+v", bakery)
	}

	again := expect[generated.CsvImport](c, http.MethodPost, target, file, http.StatusOK)
	if again.Imported != 0 || again.Duplicates != 5 || again.Failed != 1 {
		t.Fatalf("re-import = %+v", again)
	}
	allFail := expect[generated.CsvImport](c, http.MethodPost, target, csvBody("date,amount\nnope,1\n"), http.StatusOK)
	if allFail.Status != "failed" || allFail.Failed != 1 {
		t.Fatalf("all rows failing = %+v", allFail)
	}

	c.expectError(http.MethodPost, "/v1/imports/csv", csvBody("date,amount\n2026-10-01,-1\n"), http.StatusBadRequest, "account_required")
	c.expectError(http.MethodPost, "/v1/imports/csv?accountId=7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", csvBody("date,amount\n2026-10-01,-1\n"), http.StatusBadRequest, "unknown_account")
	c.expectError(http.MethodPost, target, csvBody("when,how much\n1,2\n"), http.StatusBadRequest, "invalid_csv")
	c.expectError(http.MethodPost, target, csvBody(""), http.StatusBadRequest, "invalid_csv")
	expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+giro.Id.String(), map[string]any{"archived": true}, http.StatusOK)
	c.expectError(http.MethodPost, target, csvBody("date,amount\n2026-10-01,-1\n"), http.StatusConflict, "account_archived")
}

func TestOlderFormatCsvStillImportsWithoutAnAccountParameter(t *testing.T) {
	c := newAPIClient(t)
	usd := c.account("Smoke account", "other", "USD", 0)
	file := csvBody("account_id,occurred_on,amount_minor,currency,external_id,note\n" + usd.Id.String() + ",2026-10-07,-1250,USD,e2e-1,Coffee\n")
	if result := expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv", file, http.StatusOK); result.Imported != 1 || result.AccountId != nil {
		t.Fatalf("legacy import = %+v", result)
	}
	if result := expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv", file, http.StatusOK); result.Duplicates != 1 {
		t.Fatalf("legacy re-import = %+v", result)
	}
	coffee := c.list("q=coffee").Items
	if len(coffee) != 1 || *coffee[0].Notes != "Coffee" || coffee[0].Amount.Amount != -1250 || !strings.HasPrefix(*coffee[0].Source.ExternalId, "e2e-") {
		t.Fatalf("coffee = %+v", coffee)
	}
}

func TestMonthlySummaryTotalsPerCategory(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 100000)
	usd := c.account("Travel", "card", "USD", 0)
	groceries := c.category("Groceries", "expense")
	salary := c.category("Salary", "income")
	transfer := c.category("To savings", "transfer")
	gid := giro.Id.String()
	c.spend(map[string]any{"accountId": gid, "amount": eur(300000), "bookingDate": "2026-10-01", "categoryId": salary.Id})
	c.spend(map[string]any{"accountId": gid, "amount": eur(-5000), "bookingDate": "2026-10-02", "categoryId": groceries.Id})
	c.spend(map[string]any{"amount": eur(-1500), "bookingDate": "2026-10-03", "categoryId": groceries.Id})
	c.spend(map[string]any{"accountId": gid, "amount": eur(-50000), "bookingDate": "2026-10-04", "categoryId": transfer.Id})
	c.spend(map[string]any{"amount": eur(-700), "bookingDate": "2026-10-05"})
	c.spend(map[string]any{"amount": eur(-9999), "bookingDate": "2026-10-06", "status": "pending"})
	c.spend(map[string]any{"amount": eur(-100), "bookingDate": "2026-09-30"})
	c.spend(map[string]any{"accountId": usd.Id, "amount": map[string]any{"amount": -2000, "currency": "USD"}, "bookingDate": "2026-10-02"})

	summary := expect[generated.MonthlySummary](c, http.MethodGet, "/v1/summaries/monthly?month=2026-10", nil, http.StatusOK)
	if summary.Currency != "EUR" || summary.Income.Amount != 300000 || summary.Spending.Amount != 5000+1500+700 || summary.Net.Amount != 300000-7200 {
		t.Fatalf("summary = %+v", summary)
	}
	// Booked: Girokonto 100000+300000-5000-50000, Cash -1500-700-100; the
	// total balance adds Cash's pending -9999.
	if summary.TotalBookedBalance.Amount != 345000-2300 || summary.TotalPending.Amount != -9999 || summary.TotalBalance.Amount != 345000-2300-9999 {
		t.Fatalf("total balance = %+v, booked %+v, pending %+v", summary.TotalBalance, summary.TotalBookedBalance, summary.TotalPending)
	}
	if len(summary.SpendingByCategory) != 1 || summary.SpendingByCategory[0].CategoryId != groceries.Id || summary.SpendingByCategory[0].Amount.Amount != 6500 {
		t.Fatalf("spending by category = %+v", summary.SpendingByCategory)
	}
	totals := map[string]generated.CategoryTotal{}
	for _, total := range summary.ByCategory {
		key := "uncategorized"
		if total.CategoryName != nil {
			key = *total.CategoryName
		}
		totals[key] = total
	}
	if len(totals) != 4 || totals["To savings"].Spending.Amount != 50000 || *totals["To savings"].CategoryKind != generated.CategoryTotalCategoryKindTransfer ||
		totals["Groceries"].TransactionCount != 2 || totals["Salary"].Net.Amount != 300000 || totals["uncategorized"].Spending.Amount != 700 || totals["uncategorized"].CategoryId != nil {
		t.Fatalf("by category = %+v", totals)
	}
	if summary.ByCategory[0].CategoryName == nil || *summary.ByCategory[0].CategoryName != "To savings" {
		t.Fatalf("largest spending first: %+v", summary.ByCategory[0])
	}
	usdSummary := expect[generated.MonthlySummary](c, http.MethodGet, "/v1/summaries/monthly?month=2026-10&currency=USD", nil, http.StatusOK)
	if usdSummary.Spending.Amount != 2000 || usdSummary.TotalBalance.Amount != -2000 {
		t.Fatalf("usd summary = %+v", usdSummary)
	}
	cashOnly := expect[generated.MonthlySummary](c, http.MethodGet, "/v1/summaries/monthly?month=2026-10&accountId="+c.cash().Id.String(), nil, http.StatusOK)
	if cashOnly.Spending.Amount != 2200 || cashOnly.Income.Amount != 0 || cashOnly.TotalBalance.Amount != -2300-9999 || cashOnly.TotalPending.Amount != -9999 {
		t.Fatalf("cash summary = %+v", cashOnly)
	}
	c.expectError(http.MethodGet, "/v1/summaries/monthly?month=October", nil, http.StatusBadRequest, "invalid_request")
}
