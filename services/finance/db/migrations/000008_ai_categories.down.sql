-- Rolling back keeps every transaction and its category: a category the AI
-- chose counts as chosen by a rule (so applying the rules with scope all
-- re-checks it, as 000006's rollback does with a merchant code's choice).
-- When the AI looked at a transaction is forgotten.

DROP INDEX IF EXISTS transactions_ai_candidates_idx;
ALTER TABLE transactions DROP COLUMN IF EXISTS ai_reviewed_at;

UPDATE transactions SET categorized_by = 'rule' WHERE categorized_by = 'ai';
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_categorized_by_check;
ALTER TABLE transactions
    ADD CONSTRAINT transactions_categorized_by_check CHECK (categorized_by IN ('manual', 'rule', 'mcc'));
