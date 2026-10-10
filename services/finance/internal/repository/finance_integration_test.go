package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/testdb"
)

func date(year int, month time.Month, day int) pgtype.Date {
	return pgtype.Date{Time: time.Date(year, month, day, 0, 0, 0, 0, time.UTC), Valid: true}
}

func TestFinanceQueriesPersistMinorUnitsBalancesAndImportIdentity(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := context.Background()
	queries := generated.New(pool)
	account, err := queries.CreateAccount(ctx, generated.CreateAccountParams{Name: "Main account", Currency: "USD", OpeningBalanceMinor: 1200, Kind: "bank"})
	if err != nil {
		t.Fatal(err)
	}
	category, err := queries.CreateFinancialCategory(ctx, generated.CreateFinancialCategoryParams{Name: "Groceries", Kind: "expense"})
	if err != nil {
		t.Fatal(err)
	}
	imported := generated.CreateImportedTransactionParams{
		AccountID: account.ID, CategoryID: category.ID, AmountMinor: -500, Currency: "USD", OccurredOn: date(2026, time.October, 5),
		Status: "booked", CategorizedBy: ptr("manual"), ImportSource: "csv", ImportExternalID: "row-42", SourceMetadata: []byte(`{"source":"test"}`),
	}
	transaction, err := queries.CreateImportedTransaction(ctx, imported)
	if err != nil || transaction.AmountMinor != -500 {
		t.Fatalf("imported = %+v, %v", transaction, err)
	}
	// The same identity in the same account is a duplicate: no row returns.
	if _, err := queries.CreateImportedTransaction(ctx, imported); err != pgx.ErrNoRows {
		t.Fatalf("duplicate import identity: %v", err)
	}
	// Another account may use the same external id.
	other, err := queries.CreateAccount(ctx, generated.CreateAccountParams{Name: "Other", Currency: "USD", Kind: "other"})
	if err != nil {
		t.Fatal(err)
	}
	imported.AccountID = other.ID
	if _, err := queries.CreateImportedTransaction(ctx, imported); err != nil {
		t.Fatalf("same external id in another account: %v", err)
	}
	// A pending transaction is not in the balance.
	if _, err := queries.CreateTransaction(ctx, generated.CreateTransactionParams{AccountID: account.ID, AmountMinor: -300, Currency: "USD",
		OccurredOn: date(2026, time.October, 6), Status: "pending", SourceMetadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	accounts, err := queries.ListAccountsWithBalance(ctx, generated.ListAccountsWithBalanceParams{AccountID: account.ID})
	if err != nil || len(accounts) != 1 || accounts[0].BalanceMinor != 700 || accounts[0].PendingMinor != -300 || accounts[0].TransactionCount != 2 {
		t.Fatalf("account balances = %+v, %v; want 700 booked, -300 pending", accounts, err)
	}
	cashflow, err := queries.GetMonthlyCashflow(ctx, generated.GetMonthlyCashflowParams{Currency: "USD", MonthStart: date(2026, time.October, 1)})
	if err != nil || cashflow.IncomeMinor != 0 || cashflow.SpendingMinor != 1000 || cashflow.NetMinor != -1000 {
		t.Fatalf("cashflow = %+v, %v; want spending=1000 over both accounts, pending left out", cashflow, err)
	}

	key, hash := "processor-transaction-1", "payload-a"
	params := generated.CreateTransactionParams{AccountID: account.ID, AmountMinor: -125, Currency: "USD", OccurredOn: date(2026, time.October, 6),
		Status: "booked", SourceMetadata: []byte(`{}`), AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash}
	first, err := queries.CreateTransaction(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	// Replay the committed owner mutation as a processor would after crashing
	// before it persisted the successful Inbox attempt.
	replayed, err := queries.CreateTransaction(ctx, params)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed transaction = %#v, err = %v; want %s", replayed, err, first.ID.String())
	}
}

func TestWorkspaceMigrationPreservesLedgerDataAndRollsBack(t *testing.T) {
	pool := testdb.Open(t, "000003")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var accountID, categoryID string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, currency, opening_balance_minor) VALUES ('Giro', 'EUR', 10000) RETURNING id`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO financial_categories (name, kind) VALUES ('Groceries', 'expense') RETURNING id`).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO transactions (account_id, category_id, amount_minor, currency, occurred_on, note, import_source, import_external_id)
	      VALUES ($1, $2, -2345, 'EUR', '2026-10-01', 'REWE', 'csv', 'row-1')`, accountID, categoryID)
	exec(`INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, note, import_source, import_external_id)
	      VALUES ($1, -100, 'EUR', '2026-10-02', 'legacy source', 'bank-export', 'x-1')`, accountID)
	exec(`INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, note) VALUES ($1, 5000, 'EUR', '2026-10-03', 'gift')`, accountID)

	testdb.Apply(t, pool, testdb.Migration(t, "000004", "up"))
	rows, err := pool.Query(ctx, `SELECT note, COALESCE(import_source, 'manual'), status, categorized_by FROM transactions ORDER BY occurred_on`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		note, source, status string
		categorizedBy        *string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.note, &r.source, &r.status, &r.categorizedBy); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 3 || got[0].note != "REWE" || got[0].source != "csv" || got[0].status != "booked" || got[0].categorizedBy == nil || *got[0].categorizedBy != "manual" ||
		got[1].source != "csv" || got[1].categorizedBy != nil || got[2].source != "manual" || got[2].status != "booked" {
		t.Fatalf("migrated transactions = %+v", got)
	}
	var kind string
	var archived bool
	if err := pool.QueryRow(ctx, `SELECT kind, archived_at IS NOT NULL FROM accounts WHERE id = $1`, accountID).Scan(&kind, &archived); err != nil || kind != "other" || archived {
		t.Fatalf("migrated account kind=%q archived=%v err=%v", kind, archived, err)
	}
	// The workspace's own data, then a rollback.
	exec(`UPDATE transactions SET counterparty = 'Bäckerei', description = 'Brötchen', note = NULL WHERE note = 'gift'`)
	exec(`INSERT INTO financial_categories (name, kind) VALUES ('Savings move', 'transfer'), ('Groceries', 'transfer')`)
	exec(`INSERT INTO category_rules (category_id, match_field, match_operator, match_text, priority) VALUES ($1, 'counterparty', 'contains', 'rewe', 10)`, categoryID)
	testdb.Apply(t, pool, testdb.Migration(t, "000004", "down"))
	var note string
	if err := pool.QueryRow(ctx, `SELECT note FROM transactions WHERE amount_minor = 5000`).Scan(&note); err != nil || note != "Bäckerei — Brötchen" {
		t.Fatalf("rolled-back note = %q, %v", note, err)
	}
	var names []string
	rows, err = pool.Query(ctx, `SELECT name || ':' || kind FROM financial_categories ORDER BY name COLLATE "C"`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		names = append(names, name)
	}
	rows.Close()
	if len(names) != 3 || names[0] != "Groceries:expense" || names[1] != "Groceries (transfer):expense" || names[2] != "Savings move:expense" {
		t.Fatalf("rolled-back categories = %v", names)
	}
	var rulesTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('category_rules')::text`).Scan(&rulesTable); err != nil || rulesTable != nil {
		t.Fatalf("category_rules after rollback = %v, %v", rulesTable, err)
	}
	// And forward again: the migration is repeatable.
	testdb.Apply(t, pool, testdb.Migration(t, "000004", "up"))
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE status = 'booked'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("after re-applying: %d booked, %v", count, err)
	}
}

func ptr[T any](value T) *T { return &value }

func TestBankImportMigrationRollsBackAndForward(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var accountID string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, currency, kind, source_kind, connection_id, external_id, booked_balance_minor, booked_balance_date)
	      VALUES ('DB Giro', 'EUR', 'bank', 'bank', gen_random_uuid(), 'acc-1', 5000, '2026-10-10') RETURNING id`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, import_source, import_external_id, connection_id, merchant_category_code)
	      SELECT id, -500, 'EUR', '2026-10-02', 'bank', 't1', connection_id, '5411' FROM accounts WHERE id = $1`, accountID)
	// The same bank id twice in one connection is refused.
	if _, err := pool.Exec(ctx, `INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, import_source, import_external_id, connection_id)
	      SELECT id, -1, 'EUR', '2026-10-02', 'bank', 't1', connection_id FROM accounts WHERE id = $1`, accountID); err == nil {
		t.Fatal("a duplicate bank transaction id was accepted")
	}
	// A booked balance needs its date.
	if _, err := pool.Exec(ctx, `UPDATE accounts SET booked_balance_date = NULL WHERE id = $1`, accountID); err == nil {
		t.Fatal("a booked balance without a date was accepted")
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000005", "down"))
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE connection_id IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bank transactions after rollback: %d %v", count, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000005", "up"))
}
