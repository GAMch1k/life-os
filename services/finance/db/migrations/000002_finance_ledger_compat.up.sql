-- Existing local installations may have recorded the original no-op baseline.
-- Keep this migration idempotent so those databases receive the finance ledger.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(), name TEXT NOT NULL CHECK (btrim(name) <> ''),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'), opening_balance_minor BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE (name)
);
CREATE TABLE IF NOT EXISTS financial_categories (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(), name TEXT NOT NULL CHECK (btrim(name) <> ''),
    kind TEXT NOT NULL CHECK (kind IN ('income', 'expense')), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE (name, kind)
);
CREATE TABLE IF NOT EXISTS transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(), account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    category_id UUID REFERENCES financial_categories(id) ON DELETE SET NULL, amount_minor BIGINT NOT NULL CHECK (amount_minor <> 0),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'), occurred_on DATE NOT NULL, note TEXT,
    import_source TEXT, import_external_id TEXT, source_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((import_source IS NULL) = (import_external_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS transactions_import_identity_key ON transactions (import_source, import_external_id) WHERE import_source IS NOT NULL;
CREATE INDEX IF NOT EXISTS transactions_account_occurred_on_idx ON transactions (account_id, occurred_on DESC, id DESC);
CREATE INDEX IF NOT EXISTS transactions_category_occurred_on_idx ON transactions (category_id, occurred_on DESC, id DESC);
