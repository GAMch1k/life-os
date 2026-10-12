-- name: CreateAccount :one
INSERT INTO accounts (name, currency, opening_balance_minor, kind)
VALUES (sqlc.arg(name), sqlc.arg(currency), sqlc.arg(opening_balance_minor), sqlc.arg(kind))
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: GetBuiltInAccount :one
SELECT * FROM accounts WHERE system_key = sqlc.arg(system_key)::text;

-- name: LockBuiltInAccounts :exec
SELECT pg_advisory_xact_lock(7313001);

-- name: AdoptAccountAsBuiltIn :one
-- An account the owner already called Cash becomes the built-in one.
UPDATE accounts
SET system_key = sqlc.arg(system_key)::text, kind = 'cash', archived_at = NULL, updated_at = now()
WHERE id = (
    SELECT a.id FROM accounts AS a
    WHERE a.system_key IS NULL AND a.source_kind = 'manual' AND lower(a.name) = lower(sqlc.arg(name)::text)
    ORDER BY a.created_at, a.id
    LIMIT 1
)
RETURNING *;

-- name: CreateBuiltInAccount :one
INSERT INTO accounts (name, currency, kind, system_key)
VALUES (sqlc.arg(name), sqlc.arg(currency), 'cash', sqlc.arg(system_key)::text)
RETURNING *;

-- name: ListAccountsWithBalance :many
-- booked_minor is the current booked balance. For a bank account that
-- reported one it is the bank's booked balance plus every booked
-- transaction with a later booking date: a balance dated D includes the
-- bookings through D. Every other account has its opening balance plus its
-- booked transactions. pending_minor is the sum of the pending ones.
-- ListLowBalanceAccounts computes the same.
SELECT a.id, a.name, a.currency, a.opening_balance_minor, a.kind, a.system_key, a.archived_at,
       a.source_kind, a.connection_id, a.external_id, a.provider, a.institution, a.iban_masked,
       a.booked_balance_minor, a.booked_balance_date, a.available_balance_minor, a.synced_at, a.created_at, a.updated_at,
       (CASE WHEN a.source_kind = 'bank' AND a.booked_balance_minor IS NOT NULL
             THEN a.booked_balance_minor + COALESCE(SUM(t.amount_minor) FILTER (WHERE t.status = 'booked' AND t.occurred_on > a.booked_balance_date), 0)
             ELSE a.opening_balance_minor + COALESCE(SUM(t.amount_minor) FILTER (WHERE t.status = 'booked'), 0) END)::bigint AS booked_minor,
       COALESCE(SUM(t.amount_minor) FILTER (WHERE t.status = 'pending'), 0)::bigint AS pending_minor,
       COUNT(t.id)::bigint AS transaction_count
FROM accounts AS a
LEFT JOIN transactions AS t ON t.account_id = a.id
WHERE (sqlc.arg(include_archived)::boolean OR a.archived_at IS NULL)
  AND (sqlc.narg(account_id)::uuid IS NULL OR a.id = sqlc.narg(account_id)::uuid)
GROUP BY a.id
ORDER BY (a.system_key IS NULL), lower(a.name), a.id;

-- name: UpdateAccount :one
UPDATE accounts
SET name = COALESCE(sqlc.narg(name), name),
    kind = COALESCE(sqlc.narg(kind), kind),
    currency = COALESCE(sqlc.narg(currency), currency),
    opening_balance_minor = COALESCE(sqlc.narg(opening_balance_minor), opening_balance_minor),
    archived_at = CASE
        WHEN sqlc.narg(archived)::boolean IS NULL THEN archived_at
        WHEN sqlc.narg(archived)::boolean THEN COALESCE(archived_at, now())
        ELSE NULL
    END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CountAccountTransactions :one
SELECT COUNT(*)::bigint FROM transactions WHERE account_id = $1;

-- name: CreateFinancialCategory :one
INSERT INTO financial_categories (name, kind, color)
VALUES (sqlc.arg(name), sqlc.arg(kind), sqlc.narg(color))
RETURNING *;

-- name: GetFinancialCategory :one
SELECT * FROM financial_categories WHERE id = $1;

-- name: ListFinancialCategories :many
SELECT * FROM financial_categories
WHERE (sqlc.arg(include_archived)::boolean OR archived_at IS NULL)
ORDER BY kind, lower(name), id;

-- name: UpdateFinancialCategory :one
UPDATE financial_categories
SET name = COALESCE(sqlc.narg(name), name),
    kind = COALESCE(sqlc.narg(kind), kind),
    color = CASE WHEN sqlc.arg(set_color)::boolean THEN sqlc.narg(color) ELSE color END,
    archived_at = CASE
        WHEN sqlc.narg(archived)::boolean IS NULL THEN archived_at
        WHEN sqlc.narg(archived)::boolean THEN COALESCE(archived_at, now())
        ELSE NULL
    END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListCategoryRules :many
SELECT * FROM category_rules
ORDER BY priority, created_at, id;

-- name: ListActiveCategoryRules :many
-- The rules that may categorize: those of categories that are not archived,
-- in the order they are tried.
SELECT r.* FROM category_rules AS r
JOIN financial_categories AS c ON c.id = r.category_id
WHERE c.archived_at IS NULL
ORDER BY r.priority, r.created_at, r.id;

-- name: GetCategoryRule :one
SELECT * FROM category_rules WHERE id = $1;

-- name: CreateCategoryRule :one
INSERT INTO category_rules (category_id, match_field, match_operator, match_text, priority)
VALUES (
    sqlc.arg(category_id), sqlc.arg(match_field), sqlc.arg(match_operator), sqlc.arg(match_text),
    COALESCE(sqlc.narg(priority)::integer, (SELECT COALESCE(MAX(priority), 0) + 10 FROM category_rules WHERE priority < 100000))
)
RETURNING *;

-- name: UpdateCategoryRule :one
UPDATE category_rules
SET category_id = COALESCE(sqlc.narg(category_id), category_id),
    match_field = COALESCE(sqlc.narg(match_field), match_field),
    match_operator = COALESCE(sqlc.narg(match_operator), match_operator),
    match_text = COALESCE(sqlc.narg(match_text), match_text),
    priority = COALESCE(sqlc.narg(priority), priority),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteCategoryRule :one
DELETE FROM category_rules WHERE id = $1
RETURNING *;

-- name: CreateTransaction :one
INSERT INTO transactions (
    account_id, category_id, amount_minor, currency, occurred_on, value_date, status,
    counterparty, description, note, categorized_by, category_rule_id,
    source_metadata, automation_idempotency_key, automation_payload_hash
)
VALUES (
    sqlc.arg(account_id), sqlc.narg(category_id), sqlc.arg(amount_minor), sqlc.arg(currency), sqlc.arg(occurred_on),
    sqlc.narg(value_date), sqlc.arg(status), sqlc.narg(counterparty), sqlc.narg(description), sqlc.narg(note),
    sqlc.narg(categorized_by), sqlc.narg(category_rule_id), sqlc.arg(source_metadata),
    sqlc.narg(automation_idempotency_key), sqlc.narg(automation_payload_hash)
)
ON CONFLICT (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL
DO UPDATE SET automation_idempotency_key = EXCLUDED.automation_idempotency_key
RETURNING *;

-- name: CreateImportedTransaction :one
-- No row comes back when the account already has this import identity.
INSERT INTO transactions (
    account_id, category_id, amount_minor, currency, occurred_on, value_date, status,
    counterparty, description, note, categorized_by, category_rule_id,
    import_source, import_external_id, connection_id, merchant_category_code, bank_reference, source_metadata
)
VALUES (
    sqlc.arg(account_id), sqlc.narg(category_id), sqlc.arg(amount_minor), sqlc.arg(currency), sqlc.arg(occurred_on),
    sqlc.narg(value_date), sqlc.arg(status), sqlc.narg(counterparty), sqlc.narg(description), sqlc.narg(note),
    sqlc.narg(categorized_by), sqlc.narg(category_rule_id), sqlc.arg(import_source)::text, sqlc.arg(import_external_id)::text,
    sqlc.narg(connection_id), sqlc.narg(merchant_category_code), sqlc.narg(bank_reference), sqlc.arg(source_metadata)
)
ON CONFLICT (account_id, import_source, import_external_id) WHERE import_source IS NOT NULL DO NOTHING
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1;

-- name: GetTransactionForUpdate :one
SELECT * FROM transactions WHERE id = $1 FOR UPDATE;

-- name: ListTransactions :many
-- Latest booking date first, then latest created; the after_* position is
-- the last row of the previous page. Every search word must occur in the
-- counterparty, description, or notes (the words arrive LIKE-escaped).
SELECT * FROM transactions
WHERE (sqlc.narg(account_id)::uuid IS NULL OR account_id = sqlc.narg(account_id)::uuid)
  AND (sqlc.narg(category_id)::uuid IS NULL OR category_id = sqlc.narg(category_id)::uuid)
  AND (NOT sqlc.arg(uncategorized)::boolean OR category_id IS NULL)
  AND (sqlc.narg(from_date)::date IS NULL OR occurred_on >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR occurred_on <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(direction)::text IS NULL
       OR (sqlc.narg(direction)::text = 'in' AND amount_minor > 0)
       OR (sqlc.narg(direction)::text = 'out' AND amount_minor < 0))
  AND (sqlc.narg(amount_min)::bigint IS NULL OR abs(amount_minor) >= sqlc.narg(amount_min)::bigint)
  AND (sqlc.narg(amount_max)::bigint IS NULL OR abs(amount_minor) <= sqlc.narg(amount_max)::bigint)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(source_kind)::text IS NULL OR COALESCE(import_source, 'manual') = sqlc.narg(source_kind)::text)
  AND (sqlc.narg(categorized_by)::text IS NULL OR categorized_by = sqlc.narg(categorized_by)::text)
  AND NOT EXISTS (
      SELECT 1 FROM unnest(sqlc.arg(words)::text[]) AS word
      WHERE concat_ws(' ', counterparty, description, note) NOT ILIKE '%' || word || '%'
  )
  AND (sqlc.narg(after_date)::date IS NULL
       OR (occurred_on, created_at, id) < (sqlc.narg(after_date)::date, sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY occurred_on DESC, created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: UpdateTransaction :one
-- A new counterparty or description lets the AI look at it again.
UPDATE transactions
SET account_id = sqlc.arg(account_id),
    amount_minor = sqlc.arg(amount_minor),
    currency = sqlc.arg(currency),
    occurred_on = sqlc.arg(occurred_on),
    value_date = sqlc.narg(value_date),
    status = sqlc.arg(status),
    counterparty = sqlc.narg(counterparty),
    description = sqlc.narg(description),
    note = sqlc.narg(note),
    category_id = sqlc.narg(category_id),
    categorized_by = sqlc.narg(categorized_by),
    category_rule_id = sqlc.narg(category_rule_id),
    ai_reviewed_at = CASE
        WHEN counterparty IS DISTINCT FROM sqlc.narg(counterparty) OR description IS DISTINCT FROM sqlc.narg(description) THEN NULL
        ELSE ai_reviewed_at
    END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteTransaction :exec
DELETE FROM transactions WHERE id = $1;

-- name: ListRuleCandidates :many
-- Transactions whose category was not chosen by hand: those without a
-- category or with the AI's, plus those a rule or a merchant category code
-- categorized when all_categorized is set.
SELECT id, counterparty, description, merchant_category_code, category_id, categorized_by, category_rule_id
FROM transactions
WHERE categorized_by IS DISTINCT FROM 'manual'
  AND (sqlc.arg(all_categorized)::boolean OR category_id IS NULL OR categorized_by = 'ai')
ORDER BY id
FOR UPDATE;

-- name: SetRuleCategories :exec
-- Gives each transaction the category its rule or merchant category code
-- chose (null: none); categorized_by is 'rule', 'mcc', or '' for none.
UPDATE transactions AS t
SET category_id = u.category_id,
    category_rule_id = u.rule_id,
    categorized_by = CASE WHEN u.category_id IS NULL THEN NULL ELSE NULLIF(u.categorized_by, '') END,
    updated_at = now()
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id,
             unnest(sqlc.arg(category_ids)::uuid[]) AS category_id,
             unnest(sqlc.arg(rule_ids)::uuid[]) AS rule_id,
             unnest(sqlc.arg(categorized_by)::text[]) AS categorized_by) AS u
WHERE t.id = u.id;

-- name: GetMonthlyCashflow :one
-- Booked transactions of the month in one currency, leaving out transfers.
SELECT COALESCE(SUM(t.amount_minor) FILTER (WHERE t.amount_minor > 0), 0)::bigint AS income_minor,
       COALESCE(-SUM(t.amount_minor) FILTER (WHERE t.amount_minor < 0), 0)::bigint AS spending_minor,
       COALESCE(SUM(t.amount_minor), 0)::bigint AS net_minor
FROM transactions AS t
LEFT JOIN financial_categories AS c ON c.id = t.category_id
WHERE t.status = 'booked'
  AND t.currency = sqlc.arg(currency)
  AND (sqlc.narg(account_id)::uuid IS NULL OR t.account_id = sqlc.narg(account_id)::uuid)
  AND c.kind IS DISTINCT FROM 'transfer'
  AND t.occurred_on >= sqlc.arg(month_start)::date
  AND t.occurred_on < (sqlc.arg(month_start)::date + INTERVAL '1 month');

-- name: GetMonthlyCategoryTotals :many
SELECT t.category_id, c.name AS category_name, c.kind AS category_kind,
       COALESCE(SUM(t.amount_minor) FILTER (WHERE t.amount_minor > 0), 0)::bigint AS income_minor,
       COALESCE(-SUM(t.amount_minor) FILTER (WHERE t.amount_minor < 0), 0)::bigint AS spending_minor,
       COUNT(*)::bigint AS transaction_count
FROM transactions AS t
LEFT JOIN financial_categories AS c ON c.id = t.category_id
WHERE t.status = 'booked'
  AND t.currency = sqlc.arg(currency)
  AND (sqlc.narg(account_id)::uuid IS NULL OR t.account_id = sqlc.narg(account_id)::uuid)
  AND t.occurred_on >= sqlc.arg(month_start)::date
  AND t.occurred_on < (sqlc.arg(month_start)::date + INTERVAL '1 month')
GROUP BY t.category_id, c.name, c.kind
ORDER BY spending_minor DESC, income_minor DESC, c.name NULLS LAST;

-- name: LockBankConnection :exec
-- One sync of a connection at a time.
SELECT pg_advisory_xact_lock(7313002, hashtext(sqlc.arg(connection_id)::uuid::text));

-- name: ListConnectionAccounts :many
SELECT * FROM accounts WHERE connection_id = $1 ORDER BY created_at, id;

-- name: AccountNameExists :one
SELECT EXISTS (SELECT 1 FROM accounts WHERE name = $1);

-- name: CreateBankAccount :one
INSERT INTO accounts (
    name, kind, currency, source_kind, connection_id, external_id, provider, institution, iban_masked,
    booked_balance_minor, booked_balance_date, available_balance_minor, available_balance_date, synced_at
)
VALUES (
    sqlc.arg(name), sqlc.arg(kind), sqlc.arg(currency), 'bank', sqlc.arg(connection_id), sqlc.arg(external_id)::text,
    sqlc.arg(provider), sqlc.arg(institution), sqlc.narg(iban_masked),
    sqlc.narg(booked_balance_minor), sqlc.narg(booked_balance_date), sqlc.narg(available_balance_minor), sqlc.narg(available_balance_date), now()
)
RETURNING *;

-- name: UpdateBankAccount :one
-- A re-sync refreshes what the bank reports; the owner's name, kind, and
-- archive state stay. A balance the sync does not report stays as it was.
UPDATE accounts
SET provider = sqlc.arg(provider),
    institution = sqlc.arg(institution),
    iban_masked = COALESCE(sqlc.narg(iban_masked), iban_masked),
    booked_balance_minor = CASE WHEN sqlc.narg(booked_balance_date)::date IS NULL THEN booked_balance_minor ELSE sqlc.narg(booked_balance_minor)::bigint END,
    booked_balance_date = COALESCE(sqlc.narg(booked_balance_date)::date, booked_balance_date),
    available_balance_minor = CASE WHEN sqlc.narg(available_balance_date)::date IS NULL THEN available_balance_minor ELSE sqlc.narg(available_balance_minor)::bigint END,
    available_balance_date = COALESCE(sqlc.narg(available_balance_date)::date, available_balance_date),
    synced_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ArchiveConnectionAccounts :many
UPDATE accounts
SET archived_at = now(), updated_at = now()
WHERE connection_id = $1 AND archived_at IS NULL
RETURNING *;

-- name: ListBankTransactions :many
SELECT * FROM transactions
WHERE connection_id = sqlc.arg(connection_id) AND import_external_id = ANY(sqlc.arg(external_ids)::text[])
FOR UPDATE;

-- name: FindReplacedPending :one
-- The pending bank transaction a new booked one replaces: same account and
-- amount, the same counterparty ignoring case and spacing, booked at most
-- three days apart, and absent from the sync (keep).
SELECT * FROM transactions
WHERE account_id = sqlc.arg(account_id)
  AND import_source = 'bank'
  AND status = 'pending'
  AND amount_minor = sqlc.arg(amount_minor)
  AND lower(regexp_replace(btrim(COALESCE(counterparty, '')), '\s+', ' ', 'g'))
      = lower(regexp_replace(btrim(COALESCE(sqlc.narg(counterparty)::text, '')), '\s+', ' ', 'g'))
  AND abs(occurred_on - sqlc.arg(booking_date)::date) <= 3
  AND NOT (import_external_id = ANY(sqlc.arg(keep)::text[]))
ORDER BY abs(occurred_on - sqlc.arg(booking_date)::date), created_at, id
LIMIT 1
FOR UPDATE;

-- name: UpdateBankTransaction :one
-- A new counterparty or description lets the AI look at it again.
UPDATE transactions
SET account_id = sqlc.arg(account_id),
    amount_minor = sqlc.arg(amount_minor),
    currency = sqlc.arg(currency),
    occurred_on = sqlc.arg(occurred_on),
    value_date = sqlc.narg(value_date),
    status = sqlc.arg(status),
    counterparty = sqlc.narg(counterparty),
    description = sqlc.narg(description),
    merchant_category_code = sqlc.narg(merchant_category_code),
    bank_reference = sqlc.narg(bank_reference),
    category_id = sqlc.narg(category_id),
    categorized_by = sqlc.narg(categorized_by),
    category_rule_id = sqlc.narg(category_rule_id),
    ai_reviewed_at = CASE
        WHEN counterparty IS DISTINCT FROM sqlc.narg(counterparty) OR description IS DISTINCT FROM sqlc.narg(description) THEN NULL
        ELSE ai_reviewed_at
    END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteStalePending :execrows
-- Pending bank transactions of the connection booked inside the sync window
-- that the sync no longer has.
DELETE FROM transactions
WHERE connection_id = sqlc.arg(connection_id)
  AND import_source = 'bank'
  AND status = 'pending'
  AND occurred_on BETWEEN sqlc.arg(window_from)::date AND sqlc.arg(window_to)::date
  AND NOT (import_external_id = ANY(sqlc.arg(keep)::text[]));
