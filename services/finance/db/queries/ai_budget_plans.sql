-- name: LockAIBudgetPlans :exec
-- One request or claim at a time.
SELECT pg_advisory_xact_lock(7313005);

-- name: CreateAIBudgetPlan :one
INSERT INTO ai_budget_plans (month, currency, savings_goal_minor, trigger, requested_by_kind, requested_by_name, salary_transaction_id, salary_booked_on)
VALUES (sqlc.arg(month)::date, sqlc.arg(currency), sqlc.narg(savings_goal_minor), sqlc.arg(trigger), sqlc.arg(requested_by_kind), sqlc.narg(requested_by_name),
        sqlc.narg(salary_transaction_id), sqlc.narg(salary_booked_on)::date)
RETURNING *;

-- name: GetScheduledAIBudgetPlan :one
SELECT * FROM ai_budget_plans WHERE month = sqlc.arg(month)::date AND trigger = 'schedule';

-- name: GetSalaryAIBudgetPlan :one
-- A month's newest plan the salary asked for that did not fail (dismissed
-- ones included: the owner saw that month's plan).
SELECT * FROM ai_budget_plans
WHERE month = sqlc.arg(month)::date AND trigger = 'salary' AND state <> 'failed'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: AIBudgetPlanForSalaryExists :one
-- Whether a payment already asked for a plan.
SELECT EXISTS (SELECT 1 FROM ai_budget_plans WHERE salary_transaction_id = sqlc.arg(transaction_id));

-- name: AIBudgetPlanForMonthExists :one
-- Whether a month has a plan that is waiting, running, ready, or applied:
-- the salary asks for none then. A failed or dismissed one does not count.
SELECT EXISTS (
    SELECT 1 FROM ai_budget_plans
    WHERE month = sqlc.arg(month)::date AND state IN ('requested', 'running', 'ready', 'applied', 'partly_applied')
);

-- name: ListAIBudgetPlansToNotify :many
-- Ready plans the AI made without the owner asking (the salary's and the
-- schedule's) that finished since an instant, oldest first: each gets a
-- notification.
SELECT * FROM ai_budget_plans
WHERE state = 'ready' AND trigger IN ('salary', 'schedule') AND finished_at >= sqlc.arg(since)::timestamptz
ORDER BY finished_at, id;

-- name: GetActiveAIBudgetPlan :one
-- A month's newest plan that is still waiting or running.
SELECT * FROM ai_budget_plans
WHERE month = sqlc.arg(month)::date AND state IN ('requested', 'running')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: CountAIBudgetPlanRequestsSince :one
-- The owner's requests since an instant, for the daily limit.
SELECT COUNT(*)::bigint FROM ai_budget_plans
WHERE trigger = 'request' AND requested_by_kind = 'user' AND created_at >= sqlc.arg(since);

-- name: GetAIBudgetPlan :one
SELECT * FROM ai_budget_plans WHERE id = $1;

-- name: GetAIBudgetPlanForUpdate :one
SELECT * FROM ai_budget_plans WHERE id = $1 FOR UPDATE;

-- name: ListAIBudgetPlans :many
-- Newest first.
SELECT * FROM ai_budget_plans
WHERE (sqlc.narg(month)::date IS NULL OR month = sqlc.narg(month)::date)
  AND (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: CountWaitingAIBudgetPlans :one
-- Plans the job may claim now: requested and due, or running on a claim
-- that lapsed (the processor stopped during the run).
SELECT COUNT(*)::bigint FROM ai_budget_plans
WHERE (state = 'requested' AND (retry_at IS NULL OR retry_at <= sqlc.arg(now)::timestamptz))
   OR (state = 'running' AND claimed_at <= sqlc.arg(lapsed_before)::timestamptz);

-- name: AIBudgetPlanRunning :one
-- Whether a plan is running on a claim that has not lapsed: the job runs
-- one plan at a time.
SELECT EXISTS (
    SELECT 1 FROM ai_budget_plans WHERE state = 'running' AND claimed_at > sqlc.arg(lapsed_before)::timestamptz
);

-- name: ClaimAIBudgetPlan :one
-- The oldest plan the job may claim, now running.
UPDATE ai_budget_plans
SET state = 'running', attempts = attempts + 1, claimed_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = (
    SELECT p.id FROM ai_budget_plans AS p
    WHERE (p.state = 'requested' AND (p.retry_at IS NULL OR p.retry_at <= sqlc.arg(now)::timestamptz))
       OR (p.state = 'running' AND p.claimed_at <= sqlc.arg(lapsed_before)::timestamptz)
    ORDER BY p.created_at, p.id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: SetAIBudgetPlanResult :one
UPDATE ai_budget_plans
SET state = 'ready', result = sqlc.arg(result), error = NULL, retry_at = NULL, finished_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: FailAIBudgetPlan :one
-- A failed run: waiting again until retry_at, or failed for good without
-- one.
UPDATE ai_budget_plans
SET state = CASE WHEN sqlc.narg(retry_at)::timestamptz IS NULL THEN 'failed' ELSE 'requested' END,
    error = sqlc.arg(error)::text,
    retry_at = sqlc.narg(retry_at)::timestamptz,
    finished_at = CASE WHEN sqlc.narg(retry_at)::timestamptz IS NULL THEN sqlc.arg(now)::timestamptz END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ApplyAIBudgetPlan :one
UPDATE ai_budget_plans
SET state = sqlc.arg(state)::text, applied = sqlc.arg(applied), applied_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DismissAIBudgetPlan :one
UPDATE ai_budget_plans
SET state = 'dismissed', dismissed_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetHistoryStart :one
-- Where the history of a currency starts: its first booked transaction's
-- date (null without one).
SELECT MIN(occurred_on)::date AS first_day FROM transactions WHERE status = 'booked' AND currency = sqlc.arg(currency);

-- name: ListPlanningSpending :many
-- Booked money out in expense categories in a currency between two dates
-- (the end excluded): who was paid, in which category, for a budget plan's
-- merchants; latest first.
SELECT t.category_id, t.amount_minor, t.counterparty, t.description
FROM transactions AS t
JOIN financial_categories AS c ON c.id = t.category_id
WHERE t.status = 'booked' AND t.amount_minor < 0 AND t.currency = sqlc.arg(currency)
  AND c.kind = 'expense'
  AND t.occurred_on >= sqlc.arg(from_day)::date AND t.occurred_on < sqlc.arg(to_day)::date
ORDER BY t.occurred_on DESC, t.created_at DESC, t.id DESC;
