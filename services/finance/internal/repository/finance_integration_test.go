package repository_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

func TestFinanceQueriesPersistMinorUnitsAndImportIdentity(t *testing.T) {
	databaseURL := os.Getenv("FINANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FINANCE_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	schema := fmt.Sprintf("finance_test_%d", time.Now().UnixNano())
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	})

	for _, name := range []string{"000001_template_baseline.up.sql", "000002_finance_ledger_compat.up.sql", "000003_automation_idempotency.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}

	queries := generated.New(pool)
	account, err := queries.CreateAccount(ctx, generated.CreateAccountParams{
		Name:                "Main account",
		Currency:            "USD",
		OpeningBalanceMinor: 1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	category, err := queries.CreateFinancialCategory(ctx, generated.CreateFinancialCategoryParams{Name: "Groceries", Kind: "expense"})
	if err != nil {
		t.Fatal(err)
	}
	source, externalID := "csv", "row-42"
	transaction, err := queries.CreateTransaction(ctx, generated.CreateTransactionParams{
		AccountID:        account.ID,
		CategoryID:       category.ID,
		AmountMinor:      -500,
		Currency:         "USD",
		OccurredOn:       pgtype.Date{Time: time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC), Valid: true},
		ImportSource:     &source,
		ImportExternalID: &externalID,
		SourceMetadata:   []byte(`{"source":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.AmountMinor != -500 {
		t.Fatalf("amount_minor = %d, want -500", transaction.AmountMinor)
	}

	accounts, err := queries.ListAccountsWithBalance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].BalanceMinor != 700 {
		t.Fatalf("account balances = %#v, want 700 minor units", accounts)
	}
	cashflow, err := queries.GetMonthlyCashflow(ctx, pgtype.Date{Time: time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC), Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if cashflow.IncomeMinor != 0 || cashflow.SpendingMinor != 500 || cashflow.NetMinor != -500 {
		t.Fatalf("cashflow = %#v, want income=0 spending=500 net=-500", cashflow)
	}
	spending, err := queries.GetMonthlySpendingByCategory(ctx, pgtype.Date{Time: time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC), Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(spending) != 1 || spending[0].CategoryID != category.ID || spending[0].AmountMinor != 500 {
		t.Fatalf("spending by category = %#v, want groceries=500", spending)
	}
	if _, err := queries.CreateTransaction(ctx, generated.CreateTransactionParams{
		AccountID: account.ID, AmountMinor: -500, Currency: "USD",
		OccurredOn:   pgtype.Date{Time: time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC), Valid: true},
		ImportSource: &source, ImportExternalID: &externalID, SourceMetadata: []byte(`{}`),
	}); err == nil {
		t.Fatal("duplicate import identity was accepted")
	}
	key, hash := "processor-transaction-1", "payload-a"
	first, err := queries.CreateTransaction(ctx, generated.CreateTransactionParams{AccountID: account.ID, AmountMinor: -125, Currency: "USD", OccurredOn: pgtype.Date{Time: time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC), Valid: true}, SourceMetadata: []byte(`{}`), AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash})
	if err != nil {
		t.Fatal(err)
	}
	// Replay the committed owner mutation as a processor would after crashing
	// before it persisted the successful Inbox attempt.
	replayed, err := queries.CreateTransaction(ctx, generated.CreateTransactionParams{AccountID: account.ID, AmountMinor: -125, Currency: "USD", OccurredOn: pgtype.Date{Time: time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC), Valid: true}, SourceMetadata: []byte(`{}`), AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash})
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed transaction = %#v, err = %v; want %s", replayed, err, first.ID.String())
	}
}
