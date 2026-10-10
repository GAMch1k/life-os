-- name: LockBudgetPlans :exec
-- One plan change at a time.
SELECT pg_advisory_xact_lock(7313003);

-- name: GetEffectiveBudgetPlan :one
-- The plan of a month: its own plan, else the latest regular plan that
-- started in or before it.
SELECT * FROM budget_plans
WHERE (scope = 'month' AND month = sqlc.arg(month)::date)
   OR (scope = 'regular' AND month <= sqlc.arg(month)::date)
ORDER BY (scope = 'month') DESC, month DESC
LIMIT 1;

-- name: GetBudgetPlan :one
SELECT * FROM budget_plans WHERE month = sqlc.arg(month)::date AND scope = sqlc.arg(scope)::text;

-- name: ListBudgetPlanLines :many
SELECT * FROM budget_plan_lines WHERE plan_id = $1 ORDER BY category_id;

-- name: UpsertBudgetPlan :one
INSERT INTO budget_plans (month, scope, currency, expected_income_minor, savings_goal_minor)
VALUES (sqlc.arg(month)::date, sqlc.arg(scope)::text, sqlc.arg(currency), sqlc.arg(expected_income_minor), sqlc.arg(savings_goal_minor))
ON CONFLICT (month, scope) DO UPDATE
SET currency = EXCLUDED.currency,
    expected_income_minor = EXCLUDED.expected_income_minor,
    savings_goal_minor = EXCLUDED.savings_goal_minor,
    updated_at = now()
RETURNING *;

-- name: DeleteBudgetPlanLines :exec
DELETE FROM budget_plan_lines WHERE plan_id = $1;

-- name: InsertBudgetPlanLines :exec
INSERT INTO budget_plan_lines (plan_id, category_id, amount_minor)
SELECT sqlc.arg(plan_id), unnest(sqlc.arg(category_ids)::uuid[]), unnest(sqlc.arg(amounts)::bigint[]);

-- name: DeleteLaterBudgetPlans :many
-- A regular plan starting in a month replaces the months' own plans from
-- then on and the regular plans that would start later.
DELETE FROM budget_plans
WHERE (scope = 'month' AND month >= sqlc.arg(month)::date)
   OR (scope = 'regular' AND month > sqlc.arg(month)::date)
RETURNING month, scope;

-- name: DeleteMonthBudgetPlan :one
DELETE FROM budget_plans WHERE month = sqlc.arg(month)::date AND scope = 'month'
RETURNING *;

-- name: GetBudgetMonthTotals :many
-- A month's money in one currency per category (null: uncategorized):
-- money out and in, booked and pending, and the net amount on accounts
-- that are not savings accounts (for the Savings category).
SELECT t.category_id,
       COALESCE(-SUM(t.amount_minor) FILTER (WHERE t.amount_minor < 0 AND t.status = 'booked'), 0)::bigint AS out_booked_minor,
       COALESCE(-SUM(t.amount_minor) FILTER (WHERE t.amount_minor < 0 AND t.status = 'pending'), 0)::bigint AS out_pending_minor,
       COALESCE(SUM(t.amount_minor) FILTER (WHERE t.amount_minor > 0 AND t.status = 'booked'), 0)::bigint AS in_booked_minor,
       COALESCE(SUM(t.amount_minor) FILTER (WHERE t.amount_minor > 0 AND t.status = 'pending'), 0)::bigint AS in_pending_minor,
       COALESCE(SUM(t.amount_minor) FILTER (WHERE a.kind <> 'savings'), 0)::bigint AS net_outside_savings_minor,
       COUNT(*)::bigint AS transaction_count
FROM transactions AS t
JOIN accounts AS a ON a.id = t.account_id
WHERE t.currency = sqlc.arg(currency)
  AND t.occurred_on >= sqlc.arg(month_start)::date
  AND t.occurred_on < (sqlc.arg(month_start)::date + INTERVAL '1 month')
GROUP BY t.category_id;

-- name: ListExcludedCurrencies :many
-- The month's transactions in other currencies than the budget's.
SELECT currency::text AS currency, COUNT(*)::bigint AS transaction_count
FROM transactions
WHERE currency <> sqlc.arg(currency)
  AND occurred_on >= sqlc.arg(month_start)::date
  AND occurred_on < (sqlc.arg(month_start)::date + INTERVAL '1 month')
GROUP BY currency
ORDER BY currency;

-- name: GetBudgetHistory :many
-- Booked money per month and category in one currency, for suggestions.
SELECT date_trunc('month', t.occurred_on)::date AS month, t.category_id,
       COALESCE(-SUM(t.amount_minor) FILTER (WHERE t.amount_minor < 0), 0)::bigint AS out_minor,
       COALESCE(SUM(t.amount_minor) FILTER (WHERE t.amount_minor > 0), 0)::bigint AS in_minor
FROM transactions AS t
WHERE t.status = 'booked'
  AND t.currency = sqlc.arg(currency)
  AND t.occurred_on >= sqlc.arg(from_month)::date
  AND t.occurred_on < sqlc.arg(to_month)::date
GROUP BY 1, 2;

-- name: ClaimBudgetNotification :execrows
INSERT INTO budget_notifications (dedupe_key, month)
VALUES (sqlc.arg(dedupe_key), sqlc.arg(month)::date)
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: ReleaseBudgetNotification :exec
DELETE FROM budget_notifications WHERE dedupe_key = $1;

-- name: ListBudgetNotifications :many
SELECT dedupe_key FROM budget_notifications WHERE month = sqlc.arg(month)::date ORDER BY dedupe_key;

-- name: LockSeeds :exec
SELECT pg_advisory_xact_lock(7313004);

-- name: SeedApplied :one
SELECT EXISTS (SELECT 1 FROM finance_seeds WHERE name = $1);

-- name: MarkSeedApplied :exec
INSERT INTO finance_seeds (name) VALUES ($1) ON CONFLICT (name) DO NOTHING;

-- name: GetCategoryBySystemKey :one
SELECT * FROM financial_categories WHERE system_key = sqlc.arg(system_key)::text;

-- name: AdoptCategory :one
-- An existing category of the default's name (any case) and kind becomes
-- the default one; only its system key is set.
UPDATE financial_categories
SET system_key = sqlc.arg(system_key)::text
WHERE id = (
    SELECT c.id FROM financial_categories AS c
    WHERE c.system_key IS NULL AND lower(c.name) = lower(sqlc.arg(name)::text) AND c.kind = sqlc.arg(kind)::text
    ORDER BY c.archived_at NULLS FIRST, c.created_at, c.id
    LIMIT 1
)
RETURNING *;

-- name: CategoryNameTaken :one
SELECT EXISTS (SELECT 1 FROM financial_categories WHERE lower(name) = lower(sqlc.arg(name)::text));

-- name: CreateDefaultCategory :one
INSERT INTO financial_categories (name, kind, color, system_key)
VALUES (sqlc.arg(name), sqlc.arg(kind), sqlc.narg(color), sqlc.arg(system_key)::text)
RETURNING *;

-- name: ListDefaultCategories :many
-- The active default categories, for the merchant category code fallback.
SELECT * FROM financial_categories WHERE system_key IS NOT NULL AND archived_at IS NULL ORDER BY system_key;
