-- P13-01 Finance workspace: account kinds, archive, and the built-in Cash
-- account; transaction counterparty/description, status, value date, and the
-- source identity a bank connection will use; category colors, archive, the
-- transfer kind, and category rules. Existing rows keep their data: accounts
-- become kind "other", transactions stay booked, a CSV row stays a csv
-- import, and an existing category counts as chosen by hand.

ALTER TABLE accounts
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'other' CHECK (kind IN ('bank', 'cash', 'card', 'savings', 'other')),
    ADD COLUMN system_key TEXT CHECK (system_key IN ('cash')),
    ADD COLUMN archived_at TIMESTAMPTZ,
    ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'manual' CHECK (source_kind IN ('manual', 'bank')),
    ADD COLUMN connection_id UUID,
    ADD COLUMN external_id TEXT;
ALTER TABLE accounts
    ADD CONSTRAINT accounts_system_key_unique UNIQUE (system_key),
    ADD CONSTRAINT accounts_built_in_active CHECK (system_key IS NULL OR archived_at IS NULL),
    ADD CONSTRAINT accounts_bank_identity CHECK ((source_kind = 'bank') = (connection_id IS NOT NULL AND external_id IS NOT NULL));
CREATE UNIQUE INDEX accounts_bank_identity_key ON accounts (connection_id, external_id) WHERE connection_id IS NOT NULL;

ALTER TABLE financial_categories DROP CONSTRAINT IF EXISTS financial_categories_kind_check;
ALTER TABLE financial_categories
    ADD CONSTRAINT financial_categories_kind_check CHECK (kind IN ('income', 'expense', 'transfer')),
    ADD COLUMN color TEXT CHECK (color ~ '^#[0-9a-fA-F]{6}$'),
    ADD COLUMN archived_at TIMESTAMPTZ;

CREATE TABLE category_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    category_id UUID NOT NULL REFERENCES financial_categories(id) ON DELETE CASCADE,
    match_field TEXT NOT NULL CHECK (match_field IN ('counterparty', 'description', 'any')),
    match_operator TEXT NOT NULL CHECK (match_operator IN ('contains', 'equals')),
    match_text TEXT NOT NULL CHECK (btrim(match_text) <> ''),
    priority INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX category_rules_order_idx ON category_rules (priority, created_at, id);

-- Only the CSV importer ever wrote import_source; anything else is an import
-- too, so it becomes csv before the column is limited to the source kinds.
UPDATE transactions SET import_source = 'csv' WHERE import_source IS NOT NULL AND import_source NOT IN ('csv', 'bank');

ALTER TABLE transactions
    ADD COLUMN counterparty TEXT,
    ADD COLUMN description TEXT,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'booked' CHECK (status IN ('booked', 'pending')),
    ADD COLUMN value_date DATE,
    ADD COLUMN connection_id UUID,
    ADD COLUMN categorized_by TEXT CHECK (categorized_by IN ('manual', 'rule')),
    ADD COLUMN category_rule_id UUID REFERENCES category_rules(id) ON DELETE SET NULL;
ALTER TABLE transactions
    ADD CONSTRAINT transactions_import_source_kind CHECK (import_source IS NULL OR import_source IN ('csv', 'bank')),
    ADD CONSTRAINT transactions_connection_source CHECK (connection_id IS NULL OR import_source = 'bank'),
    ADD CONSTRAINT transactions_rule_categorized CHECK (category_rule_id IS NULL OR categorized_by = 'rule');

UPDATE transactions SET categorized_by = 'manual' WHERE category_id IS NOT NULL;

-- An imported row is unique within its account: two accounts' files may use
-- the same external ids.
DROP INDEX IF EXISTS transactions_import_identity_key;
CREATE UNIQUE INDEX transactions_import_identity_key
    ON transactions (account_id, import_source, import_external_id)
    WHERE import_source IS NOT NULL;

CREATE INDEX transactions_booking_order_idx ON transactions (occurred_on DESC, created_at DESC, id DESC);
