package importer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// fakeStore keeps accounts and the import identities already stored.
type fakeStore struct {
	accounts map[uuid.UUID]store.Account
	stored   map[string]bool
	calls    []store.CreateImportedTransactionParams
}

func newFakeStore(accounts ...store.Account) *fakeStore {
	f := &fakeStore{accounts: map[uuid.UUID]store.Account{}, stored: map[string]bool{}}
	for _, account := range accounts {
		f.accounts[account.ID.Bytes] = account
	}
	return f
}

func (f *fakeStore) GetAccount(_ context.Context, id pgtype.UUID) (store.Account, error) {
	account, ok := f.accounts[id.Bytes]
	if !ok {
		return store.Account{}, pgx.ErrNoRows
	}
	return account, nil
}

func (f *fakeStore) CreateImportedTransaction(_ context.Context, params store.CreateImportedTransactionParams) (store.Transaction, error) {
	key := uuid.UUID(params.AccountID.Bytes).String() + "|" + params.ImportSource + "|" + params.ImportExternalID
	if f.stored[key] {
		return store.Transaction{}, pgx.ErrNoRows
	}
	f.stored[key] = true
	f.calls = append(f.calls, params)
	return store.Transaction{}, nil
}

func account(currency string) store.Account {
	return store.Account{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Name: "Girokonto", Currency: currency}
}

func parse(t *testing.T, csv string) Parsed {
	t.Helper()
	parsed, err := Parse(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestImportIntoTheChosenAccountWithDecimalAmountsAndRules(t *testing.T) {
	giro := account("EUR")
	queries := newFakeStore(giro)
	groceries := store.FinancialCategory{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Name: "Groceries", Kind: "expense"}
	rent := store.FinancialCategory{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Name: "Rent", Kind: "expense"}
	rule := rules.Rule{ID: uuid.New(), CategoryID: groceries.ID.Bytes, Field: rules.FieldCounterparty, Operator: rules.OperatorContains, Text: "rewe"}
	csv := "\xef\xbb\xbfDate;Amount;Counterparty;Description;Category;Notes\n" +
		"05.10.2026;-23,45;REWE Markt;Einkauf;;weekly shop\n" +
		"2026-10-01;-950,00;Hausverwaltung;Miete Oktober;rent;\n" +
		"2026-10-03;1.234,56;ACME GmbH;Gehalt;;\n" +
		"2026-10-04;-3,20;Bäckerei;;;\n" +
		"2026-10-04;-3,20;Bäckerei;;;\n" +
		"bad-date;-1;X;;;\n"
	id := uuid.UUID(giro.ID.Bytes)
	input := Input{AccountID: &id, Categories: []store.FinancialCategory{groceries, rent}, Rules: []rules.Rule{rule}}
	result, err := Import(context.Background(), queries, parse(t, csv), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 5 || result.Duplicates != 0 || result.Failed != 1 || result.Categorized != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].Row != 7 || !strings.Contains(result.Errors[0].Message, "date") {
		t.Fatalf("errors = %+v", result.Errors)
	}
	shop := queries.calls[0]
	if shop.AmountMinor != -2345 || shop.Currency != "EUR" || shop.OccurredOn.Time != time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) ||
		*shop.Counterparty != "REWE Markt" || *shop.Note != "weekly shop" || *shop.CategorizedBy != "rule" ||
		shop.CategoryID.Bytes != groceries.ID.Bytes || shop.CategoryRuleID.Bytes != rule.ID || shop.ImportSource != Source {
		t.Fatalf("shop = %+v", shop)
	}
	if rentRow := queries.calls[1]; rentRow.AmountMinor != -95000 || rentRow.CategoryID.Bytes != rent.ID.Bytes || *rentRow.CategorizedBy != "manual" {
		t.Fatalf("rent = %+v", rentRow)
	}
	if salary := queries.calls[2]; salary.AmountMinor != 123456 || salary.CategoryID.Valid || salary.CategorizedBy != nil {
		t.Fatalf("salary = %+v", salary)
	}
	// Two identical rows are two transactions with distinct content ids.
	if queries.calls[3].ImportExternalID == queries.calls[4].ImportExternalID || !strings.HasPrefix(queries.calls[3].ImportExternalID, "content:") {
		t.Fatalf("content ids = %q %q", queries.calls[3].ImportExternalID, queries.calls[4].ImportExternalID)
	}

	// Re-importing the file finds every row again, the twins included.
	again, err := Import(context.Background(), queries, parse(t, csv), input)
	if err != nil || again.Imported != 0 || again.Duplicates != 5 || again.Failed != 1 {
		t.Fatalf("re-import = %+v %v", again, err)
	}
}

func TestImportRefusesRequestsWithoutAUsableAccount(t *testing.T) {
	giro := account("EUR")
	archived := account("EUR")
	archived.ArchivedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	queries := newFakeStore(giro, archived)
	modern := parse(t, "date,amount\n2026-10-01,-1.00\n")
	if _, err := Import(context.Background(), queries, modern, Input{}); err != ErrAccountRequired {
		t.Fatalf("no account: %v", err)
	}
	unknown := uuid.New()
	if _, err := Import(context.Background(), queries, modern, Input{AccountID: &unknown}); err != ErrUnknownAccount {
		t.Fatalf("unknown account: %v", err)
	}
	archivedID := uuid.UUID(archived.ID.Bytes)
	if _, err := Import(context.Background(), queries, modern, Input{AccountID: &archivedID}); err != ErrAccountArchived {
		t.Fatalf("archived account: %v", err)
	}
}

func TestImportRejectsRowsInAnotherCurrencyOrWithTooManyDecimals(t *testing.T) {
	giro := account("EUR")
	queries := newFakeStore(giro)
	id := uuid.UUID(giro.ID.Bytes)
	result, err := Import(context.Background(), queries, parse(t, "date,amount,currency\n2026-10-01,-1.00,USD\n2026-10-01,\"-1.000,555\",\n2026-10-02,-2.00,eur\n"), Input{AccountID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Failed != 2 || !strings.Contains(result.Errors[0].Message, "USD") || !strings.Contains(result.Errors[1].Message, "decimals") {
		t.Fatalf("result = %+v", result)
	}
}

func TestOlderFormatFilesStillImportIntoEachRowsAccount(t *testing.T) {
	giro := account("USD")
	queries := newFakeStore(giro)
	csv := "account_id,occurred_on,amount_minor,currency,external_id,category_id,note\n" +
		uuid.UUID(giro.ID.Bytes).String() + ",2026-10-05,-500,usd,row-1,,coffee\n" +
		uuid.NewString() + ",2026-10-05,-500,USD,row-2,,unknown account\n" +
		"not-a-uuid,2026-10-05,-500,USD,row-3,,\n"
	result, err := Import(context.Background(), queries, parse(t, csv), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Failed != 2 || result.Account != nil {
		t.Fatalf("result = %+v", result)
	}
	if call := queries.calls[0]; call.ImportExternalID != "row-1" || call.AmountMinor != -500 || *call.Note != "coffee" || call.Currency != "USD" {
		t.Fatalf("call = %+v", call)
	}
	again, _ := Import(context.Background(), queries, parse(t, csv), Input{})
	if again.Duplicates != 1 || again.Imported != 0 {
		t.Fatalf("again = %+v", again)
	}
	// The chosen account wins over a row naming another one.
	chosen := uuid.UUID(giro.ID.Bytes)
	mixed, _ := Import(context.Background(), queries, parse(t, csv), Input{AccountID: &chosen})
	if mixed.Duplicates != 1 || mixed.Failed != 2 || !strings.Contains(mixed.Errors[0].Message, "not the account chosen") {
		t.Fatalf("mixed = %+v", mixed)
	}
}

func TestParseRejectsFilesWithoutTheRequiredColumns(t *testing.T) {
	for csv, want := range map[string]string{
		"":                       "empty",
		"amount\n-1\n":           "date",
		"date,counterparty\n1,x": "amount",
	} {
		if _, err := Parse(strings.NewReader(csv)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", csv, err, want)
		}
	}
	parsed := parse(t, "date,amount_minor,amount\n2026-10-01,0,\n2026-10-01,,abc\n,,\n2026-10-01,-125,-9\n")
	if len(parsed.Rows) != 1 || len(parsed.Errors) != 2 || *parsed.Rows[0].AmountMinor != -125 {
		t.Fatalf("parsed = %+v", parsed)
	}
}
