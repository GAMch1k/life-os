-- Bank connections (P13-03): the owner's read-only (account information)
-- consents at a licensed PSD2 provider, Enable Banking, and the state of
-- syncing their accounts into Finance.
--
-- The provider's session id is the one credential: it is stored only
-- sealed by Banking with LIFE_OS_BANKING_TOKEN_KEY (AES-256-GCM; the nonce
-- beside it) and wiped on disconnect. Transactions are never stored here
-- beyond one pending import waiting for Finance; Finance owns them.
CREATE TABLE bank_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider TEXT NOT NULL DEFAULT 'enablebanking' CHECK (provider IN ('enablebanking')),
    institution TEXT NOT NULL CHECK (btrim(institution) <> ''),
    country TEXT NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    -- pending: consented, the first sync has not succeeded yet.
    -- connected: syncing. expired: the consent ran out or was revoked at the
    -- bank (reconnect). error: the provider refuses the connection for
    -- another reason (last_error says which; reconnect). disconnected: the
    -- owner removed it; kept as history, never synced again.
    status TEXT NOT NULL CHECK (status IN ('pending', 'connected', 'expired', 'error', 'disconnected')),
    consent_valid_until TIMESTAMPTZ,
    session_ciphertext BYTEA,
    session_nonce BYTEA,
    -- The consented accounts: [{uid, key, name, product, currency,
    -- cashAccountType, ibanMasked}]. uid is the provider's per-session
    -- account id; key is the stable account id sent to Finance (a hash of
    -- the IBAN). Never the IBAN itself.
    accounts JSONB NOT NULL DEFAULT '[]',
    last_sync_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    next_sync_at TIMESTAMPTZ,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_error TEXT,
    last_counts JSONB,
    -- The latest booking date of a booked transaction synced so far: the
    -- next sync starts BANKING_OVERLAP_DAYS before it.
    sync_cursor DATE,
    -- Reads without the owner present (the scheduled syncs), counted per
    -- owner-local day: banks allow about four a day.
    unattended_reads_date DATE,
    unattended_reads_count INTEGER NOT NULL DEFAULT 0 CHECK (unattended_reads_count >= 0),
    last_manual_sync_at TIMESTAMPTZ,
    -- An import read from the bank that Finance has not accepted yet
    -- (Finance unavailable): retried without reading the bank again.
    pending_import JSONB,
    -- Finance has not archived the accounts of this disconnected
    -- connection yet: retried.
    archive_pending BOOLEAN NOT NULL DEFAULT false,
    -- The latest consent notice sent ('7d', '1d', 'expired') and the
    -- consent_valid_until it was about: a new consent starts over.
    consent_notice TEXT CHECK (consent_notice IN ('7d', '1d', 'expired')),
    consent_notice_for TIMESTAMPTZ,
    connected_at TIMESTAMPTZ,
    disconnected_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((session_ciphertext IS NULL) = (session_nonce IS NULL))
);

-- One live connection per bank: a reconnect (after expiry) reuses it, so
-- Finance keeps the same accounts.
CREATE UNIQUE INDEX bank_connections_live_institution
    ON bank_connections (provider, institution, country)
    WHERE status <> 'disconnected';

CREATE INDEX bank_connections_due ON bank_connections (next_sync_at)
    WHERE status IN ('pending', 'connected');

-- Pending authorizations: the state sent to the provider (stored as its
-- SHA-256, so the table cannot complete one), single use and short-lived.
-- connection_id is set for a reconnect.
CREATE TABLE bank_auth_states (
    state_hash BYTEA PRIMARY KEY,
    institution TEXT NOT NULL,
    country TEXT NOT NULL,
    connection_id UUID REFERENCES bank_connections (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
