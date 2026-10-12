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
	// A pending transaction is not in the booked balance.
	if _, err := queries.CreateTransaction(ctx, generated.CreateTransactionParams{AccountID: account.ID, AmountMinor: -300, Currency: "USD",
		OccurredOn: date(2026, time.October, 6), Status: "pending", SourceMetadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	accounts, err := queries.ListAccountsWithBalance(ctx, generated.ListAccountsWithBalanceParams{AccountID: account.ID})
	if err != nil || len(accounts) != 1 || accounts[0].BookedMinor != 700 || accounts[0].PendingMinor != -300 || accounts[0].TransactionCount != 2 {
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

func TestEveryMigrationRollsBackAndForwardWithData(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (name, currency) VALUES ('Giro', 'EUR');
		INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, counterparty, status)
		SELECT id, -100, 'EUR', '2026-10-01', 'Kiosk', 'pending' FROM accounts WHERE name = 'Giro'`); err != nil {
		t.Fatal(err)
	}
	downs := testdb.Migrations(t, "down")
	// Down to 000002: the 000001 rollback drops the ledger itself.
	for index := len(downs) - 1; index >= 2; index-- {
		testdb.Apply(t, pool, downs[index])
	}
	var note string
	if err := pool.QueryRow(ctx, `SELECT note FROM transactions`).Scan(&note); err != nil || note != "Kiosk" {
		t.Fatalf("after rolling back to 000002: note %q, %v", note, err)
	}
	for _, up := range testdb.Migrations(t, "up")[2:] {
		testdb.Apply(t, pool, up)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM transactions`).Scan(&status); err != nil || status != "booked" {
		t.Fatalf("after migrating forward again: status %q, %v", status, err)
	}
}

func TestBudgetMigrationRollsBackAndForwardKeepingCategories(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO accounts (name, currency) VALUES ('Giro', 'EUR')`)
	exec(`INSERT INTO financial_categories (name, kind, system_key) VALUES ('Groceries', 'expense', 'groceries')`)
	exec(`INSERT INTO transactions (account_id, category_id, amount_minor, currency, occurred_on, categorized_by, merchant_category_code)
	      SELECT a.id, c.id, -500, 'EUR', '2026-10-02', 'mcc', '5411' FROM accounts a, financial_categories c`)
	exec(`INSERT INTO budget_plans (month, scope, currency, expected_income_minor) VALUES ('2026-10-01', 'regular', 'EUR', 300000)`)
	exec(`INSERT INTO budget_plan_lines (plan_id, category_id, amount_minor) SELECT p.id, c.id, 30000 FROM budget_plans p, financial_categories c`)
	exec(`INSERT INTO budget_notifications (dedupe_key, month) VALUES ('finance.budget:2026-10:summary', '2026-10-01')`)
	// A plan month is a month's first day; a system key is unique.
	if _, err := pool.Exec(ctx, `INSERT INTO budget_plans (month, scope, currency) VALUES ('2026-10-02', 'month', 'EUR')`); err == nil {
		t.Fatal("a plan for the 2nd of a month was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO financial_categories (name, kind, system_key) VALUES ('Food', 'expense', 'groceries')`); err == nil {
		t.Fatal("a second groceries category was accepted")
	}

	testdb.Apply(t, pool, testdb.Migration(t, "000006", "down"))
	var categorizedBy, category string
	if err := pool.QueryRow(ctx, `SELECT t.categorized_by, c.name FROM transactions t JOIN financial_categories c ON c.id = t.category_id`).Scan(&categorizedBy, &category); err != nil ||
		categorizedBy != "rule" || category != "Groceries" {
		t.Fatalf("after rollback: %q %q %v", categorizedBy, category, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000006", "up"))
	exec(`UPDATE transactions SET categorized_by = 'mcc'`)
}

func TestSubscriptionsMigrationRollsBackAndForwardKeepingTransactions(t *testing.T) {
	pool := testdb.Open(t, "000006")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO accounts (name, currency, kind) VALUES ('Giro', 'EUR', 'bank')`)
	exec(`INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, counterparty)
	      SELECT id, -1399, 'EUR', '2026-10-01', 'NETFLIX.COM' FROM accounts`)

	testdb.Apply(t, pool, testdb.Migration(t, "000007", "up"))
	queries := generated.New(pool)
	settings, err := queries.GetFinanceSettings(ctx)
	if err != nil || !settings.LargeDebitEnabled || settings.LargeDebitMinor != 25000 || !settings.LowBalanceEnabled || settings.LowBalanceMinor != 10000 ||
		!settings.RenewalsEnabled || settings.RenewalLeadDays != 3 {
		t.Fatalf("default settings = %+v, %v", settings, err)
	}
	exec(`INSERT INTO subscriptions (merchant_key, match_cadence, state, source, name, cadence, amount_minor, currency, confidence, last_date, next_date)
	      VALUES ('service:netflix', 'monthly', 'confirmed', 'manual', 'Netflix', 'monthly', 1399, 'EUR', 'high', '2026-10-01', '2026-11-01')`)
	exec(`INSERT INTO subscription_transactions (subscription_id, transaction_id) SELECT s.id, t.id FROM subscriptions s, transactions t`)
	exec(`INSERT INTO alert_notifications (dedupe_key) VALUES ('finance.large_debit:x')`)
	exec(`INSERT INTO low_balance_states (account_id, below, episode) SELECT id, true, 1 FROM accounts`)
	// One subscription per counterparty and cadence; one settings row.
	if _, err := pool.Exec(ctx, `INSERT INTO subscriptions (merchant_key, match_cadence, state, source, name, cadence, amount_minor, currency, confidence)
	      VALUES ('service:netflix', 'monthly', 'suggested', 'detected', 'Netflix', 'yearly', 1399, 'EUR', 'high')`); err == nil {
		t.Fatal("a second Netflix monthly subscription was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO finance_settings (id) VALUES (false)`); err == nil {
		t.Fatal("a second settings row was accepted")
	}
	// Deleting a transaction drops its link, not the subscription.
	exec(`DELETE FROM transactions`)
	var links, subs int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM subscription_transactions), (SELECT count(*) FROM subscriptions)`).Scan(&links, &subs); err != nil ||
		links != 0 || subs != 1 {
		t.Fatalf("after deleting the transaction: %d links, %d subscriptions, %v", links, subs, err)
	}
	exec(`INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, counterparty)
	      SELECT id, -1399, 'EUR', '2026-11-01', 'NETFLIX.COM' FROM accounts`)

	testdb.Apply(t, pool, testdb.Migration(t, "000007", "down"))
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("transactions after rollback: %d %v", count, err)
	}
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()
	      AND table_name IN ('subscriptions', 'subscription_transactions', 'finance_settings', 'alert_notifications', 'low_balance_states')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("tables left after rollback: %d %v", tables, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000007", "up"))
}

func TestAICategoriesMigrationRollsBackAndForwardKeepingCategories(t *testing.T) {
	pool := testdb.Open(t, "000007")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO accounts (name, currency, kind) VALUES ('Giro', 'EUR', 'bank')`)
	exec(`INSERT INTO financial_categories (name, kind) VALUES ('Eating out', 'expense')`)
	exec(`INSERT INTO transactions (account_id, amount_minor, currency, occurred_on, counterparty)
	      SELECT id, -2350, 'EUR', '2026-10-05', 'Kneipe41' FROM accounts`)
	// Before 000008 the AI is no categorizer.
	if _, err := pool.Exec(ctx, `UPDATE transactions SET categorized_by = 'ai'`); err == nil {
		t.Fatal("categorized_by ai was accepted before 000008")
	}

	testdb.Apply(t, pool, testdb.Migration(t, "000008", "up"))
	exec(`UPDATE transactions SET category_id = (SELECT id FROM financial_categories), categorized_by = 'ai', ai_reviewed_at = now()`)
	if _, err := pool.Exec(ctx, `UPDATE transactions SET categorized_by = 'robot'`); err == nil {
		t.Fatal("an unknown categorizer was accepted")
	}

	testdb.Apply(t, pool, testdb.Migration(t, "000008", "down"))
	var categorizedBy, category string
	if err := pool.QueryRow(ctx, `SELECT t.categorized_by, c.name FROM transactions t JOIN financial_categories c ON c.id = t.category_id`).Scan(&categorizedBy, &category); err != nil ||
		categorizedBy != "rule" || category != "Eating out" {
		t.Fatalf("after rollback: %q %q %v", categorizedBy, category, err)
	}
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'transactions' AND column_name = 'ai_reviewed_at'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("ai_reviewed_at after rollback: %d %v", columns, err)
	}

	testdb.Apply(t, pool, testdb.Migration(t, "000008", "up"))
	exec(`UPDATE transactions SET categorized_by = 'ai', ai_reviewed_at = now()`)
}

func TestAIBudgetPlansMigrationRollsBackAndForward(t *testing.T) {
	pool := testdb.Open(t, "000008")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO financial_categories (name, kind) VALUES ('Eating out', 'expense')`)
	exec(`INSERT INTO budget_plans (month, scope, currency) VALUES ('2026-11-01', 'month', 'EUR')`)
	exec(`INSERT INTO budget_plan_lines (plan_id, category_id, amount_minor) SELECT p.id, c.id, 9000 FROM budget_plans p, financial_categories c`)

	testdb.Apply(t, pool, testdb.Migration(t, "000009", "up"))
	exec(`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind) VALUES ('2026-11-01', 'EUR', 'request', 'user')`)
	exec(`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind, requested_by_name, state, result, applied)
	      VALUES ('2026-11-01', 'EUR', 'schedule', 'agent', 'codex-budget-plan', 'applied', '{}', '{}')`)
	for _, bad := range []string{
		// A month is its first day; a ready plan has its proposal and an
		// applied one what was applied; one scheduled plan a month.
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind) VALUES ('2026-11-02', 'EUR', 'request', 'user')`,
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind, state) VALUES ('2026-11-01', 'EUR', 'request', 'user', 'ready')`,
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind, state, result) VALUES ('2026-11-01', 'EUR', 'request', 'user', 'applied', '{}')`,
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind) VALUES ('2026-11-01', 'EUR', 'schedule', 'system')`,
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind) VALUES ('2026-11-01', 'EUR', 'whim', 'user')`,
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind, state) VALUES ('2026-11-01', 'EUR', 'request', 'user', 'thinking')`,
		`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind, savings_goal_minor) VALUES ('2026-11-01', 'EUR', 'request', 'user', -1)`,
	} {
		if _, err := pool.Exec(ctx, bad); err == nil {
			t.Fatalf("accepted: %s", bad)
		}
	}

	testdb.Apply(t, pool, testdb.Migration(t, "000009", "down"))
	var tables, lines int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'ai_budget_plans'),
	      (SELECT count(*) FROM budget_plan_lines)`).Scan(&tables, &lines); err != nil || tables != 0 || lines != 1 {
		t.Fatalf("after rollback: %d tables, %d plan lines, %v", tables, lines, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000009", "up"))
	exec(`INSERT INTO ai_budget_plans (month, currency, trigger, requested_by_kind) VALUES ('2026-12-01', 'EUR', 'schedule', 'agent')`)
}
