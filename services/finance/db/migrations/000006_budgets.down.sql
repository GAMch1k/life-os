-- Rolling back keeps every category, rule, and transaction: the default
-- categories and rules stay as ordinary ones, and a category chosen from a
-- merchant category code counts as chosen by a rule (so applying the rules
-- with scope all re-checks it). Budget plans and notification claims go.

DROP INDEX IF EXISTS transactions_month_currency_idx;
DROP TABLE IF EXISTS finance_seeds;
DROP TABLE IF EXISTS budget_notifications;
DROP TABLE IF EXISTS budget_plan_lines;
DROP TABLE IF EXISTS budget_plans;

UPDATE transactions SET categorized_by = 'rule' WHERE categorized_by = 'mcc';
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_categorized_by_check;
ALTER TABLE transactions
    ADD CONSTRAINT transactions_categorized_by_check CHECK (categorized_by IN ('manual', 'rule'));

ALTER TABLE financial_categories
    DROP CONSTRAINT IF EXISTS financial_categories_system_key_unique,
    DROP COLUMN IF EXISTS system_key;
