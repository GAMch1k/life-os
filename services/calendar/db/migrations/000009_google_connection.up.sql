-- Google Calendar connection (P11-01): the owner's one Google account, the
-- calendars it can see, and which of them will sync.
--
-- Only the refresh token is persisted, encrypted by Calendar with
-- LIFE_OS_GOOGLE_TOKEN_KEY (AES-256-GCM; the nonce is stored beside it).
-- Access tokens live in memory only; their expiry is kept for display.
-- At most one connection exists (the singleton column).
CREATE TABLE google_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton BOOLEAN NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    account_email TEXT NOT NULL CHECK (btrim(account_email) <> ''),
    -- The scopes Google granted (the owner may grant fewer than asked).
    scopes TEXT[] NOT NULL DEFAULT '{}',
    refresh_token_ciphertext BYTEA NOT NULL,
    refresh_token_nonce BYTEA NOT NULL,
    access_token_expires_at TIMESTAMPTZ,
    -- connected: usable. expired: Google refused the refresh token
    -- (invalid_grant: expired or revoked), reconnect. error: another
    -- refusal, such as a changed client secret; last_error says which.
    status TEXT NOT NULL DEFAULT 'connected' CHECK (status IN ('connected', 'expired', 'error')),
    last_error TEXT,
    connected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The connected account's calendar list, as last read from Google, with the
-- owner's choice of the calendars to sync. Disconnecting deletes it.
CREATE TABLE google_calendars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL REFERENCES google_connections (id) ON DELETE CASCADE,
    google_calendar_id TEXT NOT NULL CHECK (google_calendar_id <> ''),
    summary TEXT NOT NULL,
    color TEXT,
    access_role TEXT NOT NULL CHECK (access_role IN ('owner', 'writer', 'reader', 'freeBusyReader')),
    is_primary BOOLEAN NOT NULL DEFAULT false,
    sync_enabled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connection_id, google_calendar_id)
);

-- Pending authorizations: the OAuth state (stored as its SHA-256, so the
-- table cannot be replayed) and the PKCE verifier, single use and short-lived.
CREATE TABLE google_oauth_states (
    state_hash BYTEA PRIMARY KEY,
    code_verifier TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
