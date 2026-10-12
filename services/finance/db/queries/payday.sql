-- name: GetPaydaySettings :one
SELECT * FROM payday_settings WHERE id;

-- name: SavePaydaySettings :one
INSERT INTO payday_settings (id, enabled, payer_match, min_amount_minor)
VALUES (true, sqlc.arg(enabled), sqlc.narg(payer_match), sqlc.narg(min_amount_minor))
ON CONFLICT (id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    payer_match = EXCLUDED.payer_match,
    min_amount_minor = EXCLUDED.min_amount_minor,
    updated_at = now()
RETURNING *;

-- name: ListIncomePayments :many
-- Booked money in with a counterparty in a currency since a day, not in a
-- transfer category (money moved between the owner's own accounts): who may
-- pay the salary; latest first.
SELECT t.id, t.account_id, t.amount_minor, t.currency, t.occurred_on, t.counterparty, t.description, t.category_id
FROM transactions AS t
LEFT JOIN financial_categories AS c ON c.id = t.category_id
WHERE t.status = 'booked' AND t.amount_minor > 0 AND t.currency = sqlc.arg(currency)
  AND t.counterparty IS NOT NULL
  AND t.occurred_on >= sqlc.arg(since)::date
  AND (c.kind IS NULL OR c.kind <> 'transfer')
ORDER BY t.occurred_on DESC, t.id DESC;
