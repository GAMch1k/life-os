-- P13-08 AI categories: the AI fills the gaps the rules leave. A category
-- the AI chose is categorized_by 'ai': a rule or a merchant category code
-- replaces it, and a choice by hand always wins. ai_reviewed_at is when the
-- AI last looked at a transaction (and chose a category or left it
-- without), so it is not sent again until its counterparty or description
-- changes. Existing rows are unchanged.

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_categorized_by_check;
ALTER TABLE transactions
    ADD CONSTRAINT transactions_categorized_by_check CHECK (categorized_by IN ('manual', 'rule', 'mcc', 'ai'));

ALTER TABLE transactions ADD COLUMN ai_reviewed_at TIMESTAMPTZ;

-- The AI's candidates, latest first: booked, without a category nobody
-- chose, and not looked at yet.
CREATE INDEX transactions_ai_candidates_idx ON transactions (occurred_on DESC, created_at DESC, id DESC)
    WHERE status = 'booked' AND category_id IS NULL AND categorized_by IS NULL AND ai_reviewed_at IS NULL;
