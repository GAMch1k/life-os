-- name: ListAICategoryCandidates :many
-- What the AI may categorize, latest first: booked transactions of active
-- accounts without a category that nobody chose (a category cleared by hand
-- stays cleared), which the AI has not looked at since their text last
-- changed. Pending ones are left out: their text is often incomplete.
SELECT t.id, t.amount_minor, t.currency, t.occurred_on, t.counterparty, t.description, a.kind AS account_kind
FROM transactions AS t
JOIN accounts AS a ON a.id = t.account_id
WHERE t.status = 'booked' AND t.category_id IS NULL AND t.categorized_by IS NULL AND t.ai_reviewed_at IS NULL
  AND a.archived_at IS NULL
ORDER BY t.occurred_on DESC, t.created_at DESC, t.id DESC
LIMIT sqlc.arg(page_limit);

-- name: CountAICategoryCandidates :one
SELECT COUNT(*)::bigint
FROM transactions AS t
JOIN accounts AS a ON a.id = t.account_id
WHERE t.status = 'booked' AND t.category_id IS NULL AND t.categorized_by IS NULL AND t.ai_reviewed_at IS NULL
  AND a.archived_at IS NULL;

-- name: ListManualCategoryExamples :many
-- The owner's latest choice by hand per counterparty (compared without case
-- or repeated spaces), in a category that is not archived, latest first:
-- examples of their taste for the AI.
SELECT latest.counterparty, latest.category_id
FROM (
    SELECT DISTINCT ON (lower(regexp_replace(btrim(t.counterparty), '\s+', ' ', 'g')))
           t.counterparty::text AS counterparty, t.category_id, t.occurred_on, t.created_at, t.id
    FROM transactions AS t
    JOIN financial_categories AS c ON c.id = t.category_id
    WHERE t.categorized_by = 'manual' AND t.counterparty IS NOT NULL AND c.archived_at IS NULL
    ORDER BY lower(regexp_replace(btrim(t.counterparty), '\s+', ' ', 'g')), t.occurred_on DESC, t.created_at DESC, t.id DESC
) AS latest
ORDER BY latest.occurred_on DESC, latest.created_at DESC, latest.id DESC
LIMIT sqlc.arg(example_limit);

-- name: LockTransactionsForAI :many
-- The transactions the AI chose for, locked while its choices apply.
SELECT id, amount_minor, category_id, categorized_by
FROM transactions
WHERE id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id
FOR UPDATE;

-- name: SetAICategories :execrows
-- Gives each transaction the AI's category, only while no one else chose
-- one (or the AI chose it), and notes that the AI looked at it.
UPDATE transactions AS t
SET category_id = u.category_id,
    category_rule_id = NULL,
    categorized_by = 'ai',
    ai_reviewed_at = now(),
    updated_at = now()
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id,
             unnest(sqlc.arg(category_ids)::uuid[]) AS category_id) AS u
WHERE t.id = u.id
  AND (t.categorized_by IS NULL OR t.categorized_by = 'ai');

-- name: MarkAIReviewed :execrows
-- Notes that the AI looked at transactions it left without a category; one
-- that got a category meanwhile is left alone.
UPDATE transactions
SET ai_reviewed_at = now()
WHERE id = ANY(sqlc.arg(ids)::uuid[])
  AND category_id IS NULL
  AND categorized_by IS NULL;
