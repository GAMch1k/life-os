-- name: LockSubscriptions :exec
-- One detection run or subscription change at a time.
SELECT pg_advisory_xact_lock(7313005);

-- name: ListRecurringCandidates :many
-- Booked money out since a day, for detecting recurring payments; money
-- moved between own accounts (transfer categories) is not a payment.
SELECT t.id, t.account_id, t.amount_minor, t.currency, t.occurred_on, t.counterparty, t.description, t.category_id
FROM transactions AS t
LEFT JOIN financial_categories AS c ON c.id = t.category_id
WHERE t.status = 'booked'
  AND t.amount_minor < 0
  AND t.occurred_on >= sqlc.arg(since)::date
  AND c.kind IS DISTINCT FROM 'transfer'
ORDER BY t.occurred_on, t.created_at, t.id;

-- name: ListSubscriptions :many
-- Suggested first, then confirmed, then ignored; by name within a state.
SELECT * FROM subscriptions
WHERE sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text
ORDER BY CASE state WHEN 'suggested' THEN 0 WHEN 'confirmed' THEN 1 ELSE 2 END, lower(name), id;

-- name: ListSubscriptionLinks :many
-- The transactions each subscription matched, latest first.
SELECT st.subscription_id, st.transaction_id
FROM subscription_transactions AS st
JOIN transactions AS t ON t.id = st.transaction_id
WHERE sqlc.narg(subscription_id)::uuid IS NULL OR st.subscription_id = sqlc.narg(subscription_id)::uuid
ORDER BY st.subscription_id, t.occurred_on DESC, t.created_at DESC, t.id DESC;

-- name: GetSubscription :one
SELECT * FROM subscriptions WHERE id = $1;

-- name: GetSubscriptionForUpdate :one
SELECT * FROM subscriptions WHERE id = $1 FOR UPDATE;

-- name: GetSubscriptionByIdentity :one
SELECT * FROM subscriptions
WHERE merchant_key = sqlc.arg(merchant_key) AND match_cadence = sqlc.arg(match_cadence)
FOR UPDATE;

-- name: InsertSubscription :one
INSERT INTO subscriptions (
    merchant_key, match_cadence, state, source, name, cadence, amount_minor, currency,
    category_id, account_id, confidence, last_date, next_date
)
VALUES (
    sqlc.arg(merchant_key), sqlc.arg(match_cadence), sqlc.arg(state), sqlc.arg(source), sqlc.arg(name), sqlc.arg(cadence),
    sqlc.arg(amount_minor), sqlc.arg(currency), sqlc.narg(category_id), sqlc.narg(account_id), sqlc.arg(confidence),
    sqlc.narg(last_date), sqlc.narg(next_date)
)
RETURNING *;

-- name: UpdateSubscription :one
UPDATE subscriptions
SET state = sqlc.arg(state),
    name = sqlc.arg(name),
    name_edited = sqlc.arg(name_edited),
    cadence = sqlc.arg(cadence),
    amount_minor = sqlc.arg(amount_minor),
    amount_edited = sqlc.arg(amount_edited),
    currency = sqlc.arg(currency),
    category_id = sqlc.narg(category_id),
    category_edited = sqlc.arg(category_edited),
    account_id = sqlc.narg(account_id),
    confidence = sqlc.arg(confidence),
    last_date = sqlc.narg(last_date),
    next_date = sqlc.narg(next_date),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteSubscription :one
DELETE FROM subscriptions WHERE id = $1
RETURNING *;

-- name: ReplaceSubscriptionLinks :exec
-- Makes ids the subscription's matched transactions.
WITH removed AS (
    DELETE FROM subscription_transactions AS st
    WHERE st.subscription_id = sqlc.arg(subscription_id)
      AND NOT (st.transaction_id = ANY(sqlc.arg(transaction_ids)::uuid[]))
)
INSERT INTO subscription_transactions (subscription_id, transaction_id)
SELECT sqlc.arg(subscription_id), unnest(sqlc.arg(transaction_ids)::uuid[])
ON CONFLICT DO NOTHING;

-- name: GetFinanceSettings :one
SELECT * FROM finance_settings WHERE id;

-- name: SaveFinanceSettings :one
INSERT INTO finance_settings (id, large_debit_enabled, large_debit_minor, low_balance_enabled, low_balance_minor, renewals_enabled, renewal_lead_days)
VALUES (true, sqlc.arg(large_debit_enabled), sqlc.arg(large_debit_minor), sqlc.arg(low_balance_enabled), sqlc.arg(low_balance_minor),
        sqlc.arg(renewals_enabled), sqlc.arg(renewal_lead_days))
ON CONFLICT (id) DO UPDATE
SET large_debit_enabled = EXCLUDED.large_debit_enabled,
    large_debit_minor = EXCLUDED.large_debit_minor,
    low_balance_enabled = EXCLUDED.low_balance_enabled,
    low_balance_minor = EXCLUDED.low_balance_minor,
    renewals_enabled = EXCLUDED.renewals_enabled,
    renewal_lead_days = EXCLUDED.renewal_lead_days,
    updated_at = now()
RETURNING *;

-- name: ClaimAlertNotification :execrows
INSERT INTO alert_notifications (dedupe_key) VALUES (sqlc.arg(dedupe_key)) ON CONFLICT (dedupe_key) DO NOTHING;

-- name: ReleaseAlertNotification :exec
DELETE FROM alert_notifications WHERE dedupe_key = $1;

-- name: ListAlertNotifications :many
SELECT dedupe_key FROM alert_notifications WHERE starts_with(dedupe_key, sqlc.arg(prefix)::text) ORDER BY dedupe_key;

-- name: CarryOverAlertClaim :exec
-- A booked transaction that replaced a pending one keeps the pending one's
-- claim, so the same debit is not notified twice.
INSERT INTO alert_notifications (dedupe_key)
SELECT sqlc.arg(to_key)::text
WHERE EXISTS (SELECT 1 FROM alert_notifications WHERE dedupe_key = sqlc.arg(from_key)::text)
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: ListLargeDebits :many
-- Money out of at least a threshold in one currency, booked or pending,
-- booked on or after a day, that has not been notified.
SELECT t.id, t.account_id, t.amount_minor, t.currency, t.occurred_on, t.status, t.counterparty, t.description,
       a.name AS account_name
FROM transactions AS t
JOIN accounts AS a ON a.id = t.account_id
WHERE t.currency = sqlc.arg(currency)
  AND t.amount_minor <= -sqlc.arg(threshold_minor)::bigint
  AND t.occurred_on >= sqlc.arg(since)::date
  AND NOT EXISTS (SELECT 1 FROM alert_notifications AS n WHERE n.dedupe_key = 'finance.large_debit:' || t.id::text)
ORDER BY t.occurred_on, t.created_at, t.id;

-- name: ListLowBalanceAccounts :many
-- Active bank and cash accounts in one currency with the balance they
-- show (the bank's booked balance when it reported one) and their
-- low-balance state.
SELECT a.id, a.name, a.source_kind, a.booked_balance_date,
       (CASE WHEN a.source_kind = 'bank' AND a.booked_balance_minor IS NOT NULL THEN a.booked_balance_minor
             ELSE a.opening_balance_minor + COALESCE((SELECT SUM(t.amount_minor) FROM transactions AS t
                                                      WHERE t.account_id = a.id AND t.status = 'booked'), 0) END)::bigint AS balance_minor,
       s.below, s.episode
FROM accounts AS a
LEFT JOIN low_balance_states AS s ON s.account_id = a.id
WHERE a.archived_at IS NULL
  AND a.kind IN ('bank', 'cash')
  AND a.currency = sqlc.arg(currency)
ORDER BY lower(a.name), a.id;

-- name: SetLowBalanceState :exec
INSERT INTO low_balance_states (account_id, below, episode)
VALUES (sqlc.arg(account_id), sqlc.arg(below), sqlc.arg(episode))
ON CONFLICT (account_id) DO UPDATE
SET below = EXCLUDED.below, episode = EXCLUDED.episode, changed_at = now();

-- name: ListDueRenewals :many
-- Confirmed subscriptions whose next payment is expected between two days.
SELECT s.*, a.name AS account_name
FROM subscriptions AS s
LEFT JOIN accounts AS a ON a.id = s.account_id
WHERE s.state = 'confirmed'
  AND s.next_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
ORDER BY s.next_date, lower(s.name), s.id;
