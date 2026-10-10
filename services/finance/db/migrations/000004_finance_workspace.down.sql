-- Rolls the Finance workspace back to the 000003 ledger. Text that only the
-- workspace has (counterparty, description) is folded into the note so no
-- words are lost; pending transactions become ordinary rows, rules are
-- removed, transfer categories become expense categories, and the built-in
-- Cash account stays as an ordinary account named Cash.

DROP INDEX IF EXISTS transactions_booking_order_idx;

UPDATE transactions
SET note = NULLIF(concat_ws(' — ', counterparty, description, NULLIF(note, '')), '')
WHERE counterparty IS NOT NULL OR description IS NOT NULL;

-- Fails when two accounts imported the same external id, which the 000003
-- ledger cannot hold; resolve those rows before rolling back.
DROP INDEX IF EXISTS transactions_import_identity_key;
CREATE UNIQUE INDEX transactions_import_identity_key
    ON transactions (import_source, import_external_id)
    WHERE import_source IS NOT NULL;

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_rule_categorized,
    DROP CONSTRAINT IF EXISTS transactions_connection_source,
    DROP CONSTRAINT IF EXISTS transactions_import_source_kind,
    DROP COLUMN IF EXISTS category_rule_id,
    DROP COLUMN IF EXISTS categorized_by,
    DROP COLUMN IF EXISTS connection_id,
    DROP COLUMN IF EXISTS value_date,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS counterparty;

DROP TABLE IF EXISTS category_rules;

UPDATE financial_categories AS c
SET kind = 'expense',
    name = CASE WHEN EXISTS (SELECT 1 FROM financial_categories AS e WHERE e.kind = 'expense' AND e.name = c.name)
                THEN c.name || ' (transfer)' ELSE c.name END
WHERE c.kind = 'transfer';
ALTER TABLE financial_categories DROP CONSTRAINT IF EXISTS financial_categories_kind_check;
ALTER TABLE financial_categories
    ADD CONSTRAINT financial_categories_kind_check CHECK (kind IN ('income', 'expense')),
    DROP COLUMN IF EXISTS archived_at,
    DROP COLUMN IF EXISTS color;

DROP INDEX IF EXISTS accounts_bank_identity_key;
ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_bank_identity,
    DROP CONSTRAINT IF EXISTS accounts_built_in_active,
    DROP CONSTRAINT IF EXISTS accounts_system_key_unique,
    DROP COLUMN IF EXISTS external_id,
    DROP COLUMN IF EXISTS connection_id,
    DROP COLUMN IF EXISTS source_kind,
    DROP COLUMN IF EXISTS archived_at,
    DROP COLUMN IF EXISTS system_key,
    DROP COLUMN IF EXISTS kind;
