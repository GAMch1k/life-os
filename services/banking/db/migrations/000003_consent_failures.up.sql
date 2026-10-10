-- Consents the bank or provider did not complete (P13-03): the callback
-- came back with an error instead of a code. Kept durably so the reason
-- survives restarts; the newest uncleared one is shown on the Finance page
-- until a connection succeeds (which clears them). Never the code or the
-- state itself.
CREATE TABLE bank_consent_failures (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at TIMESTAMPTZ NOT NULL,
    -- The provider's error code (OAuth style, such as server_error) and its
    -- description, cleaned and cut to 300 characters.
    error TEXT NOT NULL CHECK (btrim(error) <> ''),
    description TEXT,
    -- From the authorization the state belonged to; NULL when unknown.
    institution TEXT,
    country TEXT,
    connection_id UUID REFERENCES bank_connections (id) ON DELETE SET NULL,
    -- matched: a pending authorization; expired: one older than its
    -- lifetime; unknown: no such authorization (used, forged, or missing).
    state_match TEXT NOT NULL CHECK (state_match IN ('matched', 'expired', 'unknown')),
    -- Set when a connection succeeded after it.
    cleared_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bank_consent_failures_recent ON bank_consent_failures (occurred_at DESC);
