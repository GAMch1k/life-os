DROP INDEX IF EXISTS transactions_bank_identity_key;

ALTER TABLE transactions
    DROP COLUMN IF EXISTS bank_reference,
    DROP COLUMN IF EXISTS merchant_category_code;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_available_balance_dated,
    DROP CONSTRAINT IF EXISTS accounts_booked_balance_dated,
    DROP COLUMN IF EXISTS synced_at,
    DROP COLUMN IF EXISTS available_balance_date,
    DROP COLUMN IF EXISTS available_balance_minor,
    DROP COLUMN IF EXISTS booked_balance_date,
    DROP COLUMN IF EXISTS booked_balance_minor,
    DROP COLUMN IF EXISTS iban_masked,
    DROP COLUMN IF EXISTS institution,
    DROP COLUMN IF EXISTS provider;
