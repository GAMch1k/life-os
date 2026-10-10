-- P13-05 subscriptions and alerts: recurring payments (detected or marked
-- by the owner) with the transactions they matched, the alert settings
-- (large debits, low balances, renewal reminders), the alert notifications
-- already claimed, and each account's low-balance episode. Existing rows are
-- unchanged.

-- A subscription is identified by its counterparty key and the cadence it
-- was found (or marked) with: an ignored one keeps that identity, so the
-- same counterparty and cadence are never suggested again. cadence is the
-- owner's (editable); match_cadence never changes. The *_edited flags keep
-- what the owner set from being changed by detection.
CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_key TEXT NOT NULL CHECK (btrim(merchant_key) <> ''),
    match_cadence TEXT NOT NULL CHECK (match_cadence IN ('weekly', 'monthly', 'quarterly', 'yearly')),
    state TEXT NOT NULL CHECK (state IN ('suggested', 'confirmed', 'ignored')),
    source TEXT NOT NULL CHECK (source IN ('detected', 'manual')),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    name_edited BOOLEAN NOT NULL DEFAULT false,
    cadence TEXT NOT NULL CHECK (cadence IN ('weekly', 'monthly', 'quarterly', 'yearly')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    amount_edited BOOLEAN NOT NULL DEFAULT false,
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    category_id UUID REFERENCES financial_categories(id) ON DELETE SET NULL,
    category_edited BOOLEAN NOT NULL DEFAULT false,
    account_id UUID REFERENCES accounts(id) ON DELETE SET NULL,
    confidence TEXT NOT NULL CHECK (confidence IN ('high', 'low')),
    last_date DATE,
    next_date DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (merchant_key, match_cadence)
);
CREATE INDEX subscriptions_state_idx ON subscriptions (state);

CREATE TABLE subscription_transactions (
    subscription_id UUID NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    transaction_id UUID NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    PRIMARY KEY (subscription_id, transaction_id)
);
CREATE INDEX subscription_transactions_transaction_idx ON subscription_transactions (transaction_id);

-- One row: the alert settings. Thresholds are minor units of the default
-- currency.
CREATE TABLE finance_settings (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    large_debit_enabled BOOLEAN NOT NULL DEFAULT true,
    large_debit_minor BIGINT NOT NULL DEFAULT 25000 CHECK (large_debit_minor > 0),
    low_balance_enabled BOOLEAN NOT NULL DEFAULT true,
    low_balance_minor BIGINT NOT NULL DEFAULT 10000 CHECK (low_balance_minor >= 0),
    renewals_enabled BOOLEAN NOT NULL DEFAULT true,
    renewal_lead_days INTEGER NOT NULL DEFAULT 3 CHECK (renewal_lead_days BETWEEN 0 AND 30),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO finance_settings DEFAULT VALUES;

-- An alert notification (renewal, large debit, low balance) is claimed
-- here before it is sent, so each is sent once across restarts; a send that
-- fails for now gives the claim back.
CREATE TABLE alert_notifications (
    dedupe_key TEXT PRIMARY KEY,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Whether an account's balance is below the low-balance threshold, and how
-- many times it fell below: each fall is a new episode with its own
-- notification.
CREATE TABLE low_balance_states (
    account_id UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    below BOOLEAN NOT NULL,
    episode INTEGER NOT NULL CHECK (episode >= 0),
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
