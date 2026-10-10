-- P13-01 bank imports: what the banking integration service reports about a
-- bank account (provider, institution, masked IBAN, booked and available
-- balances, last sync) and about a bank transaction (merchant category code,
-- bank reference). A bank transaction is unique within its connection.

ALTER TABLE accounts
    ADD COLUMN provider TEXT,
    ADD COLUMN institution TEXT,
    ADD COLUMN iban_masked TEXT,
    ADD COLUMN booked_balance_minor BIGINT,
    ADD COLUMN booked_balance_date DATE,
    ADD COLUMN available_balance_minor BIGINT,
    ADD COLUMN available_balance_date DATE,
    ADD COLUMN synced_at TIMESTAMPTZ;
ALTER TABLE accounts
    ADD CONSTRAINT accounts_booked_balance_dated CHECK ((booked_balance_minor IS NULL) = (booked_balance_date IS NULL)),
    ADD CONSTRAINT accounts_available_balance_dated CHECK ((available_balance_minor IS NULL) = (available_balance_date IS NULL));

ALTER TABLE transactions
    ADD COLUMN merchant_category_code TEXT,
    ADD COLUMN bank_reference TEXT;

CREATE UNIQUE INDEX transactions_bank_identity_key
    ON transactions (connection_id, import_external_id)
    WHERE connection_id IS NOT NULL;
