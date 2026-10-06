-- name: CreateAccount :one
INSERT INTO accounts (name, currency, opening_balance_minor)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListAccountsWithBalance :many
SELECT a.id, a.name, a.currency, a.opening_balance_minor,
       (a.opening_balance_minor + COALESCE(SUM(t.amount_minor), 0))::bigint AS balance_minor,
       a.created_at, a.updated_at
FROM accounts AS a
LEFT JOIN transactions AS t ON t.account_id = a.id
GROUP BY a.id
ORDER BY a.name;

-- name: CreateFinancialCategory :one
INSERT INTO financial_categories (name, kind)
VALUES ($1, $2)
RETURNING *;

-- name: ListFinancialCategories :many
SELECT * FROM financial_categories
ORDER BY kind, name;

-- name: UpdateFinancialCategory :one
UPDATE financial_categories
SET name = COALESCE(sqlc.narg(name), name),
    kind = COALESCE(sqlc.narg(kind), kind),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CreateTransaction :one
INSERT INTO transactions (
    account_id, category_id, amount_minor, currency, occurred_on, note,
    import_source, import_external_id, source_metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetTransactionByImportIdentity :one
SELECT * FROM transactions
WHERE import_source = $1 AND import_external_id = $2;

-- name: ListTransactions :many
SELECT * FROM transactions
WHERE (sqlc.narg(account_id)::uuid IS NULL OR account_id = sqlc.narg(account_id)::uuid)
  AND (sqlc.narg(category_id)::uuid IS NULL OR category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(from_date)::date IS NULL OR occurred_on >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR occurred_on <= sqlc.narg(to_date)::date)
ORDER BY occurred_on DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: GetMonthlyCashflow :one
SELECT COALESCE(SUM(amount_minor) FILTER (WHERE amount_minor > 0), 0)::bigint AS income_minor,
       COALESCE(-SUM(amount_minor) FILTER (WHERE amount_minor < 0), 0)::bigint AS spending_minor,
       COALESCE(SUM(amount_minor), 0)::bigint AS net_minor
FROM transactions
WHERE occurred_on >= sqlc.arg(month_start)::date
  AND occurred_on < (sqlc.arg(month_start)::date + INTERVAL '1 month');
