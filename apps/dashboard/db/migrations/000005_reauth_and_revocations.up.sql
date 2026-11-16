-- P21-06 (review): re-entering the password in a session, and credential
-- revocations that follow an account action Better Auth runs on its own.

-- When a session last re-entered its password (Settings, the consent
-- page, the phone's pairing page): with a sign-in from the last 12 hours,
-- what making an API token, a pairing code, or an OAuth consent needs, and
-- (within 5 minutes) what adding a first second factor needs.
CREATE TABLE session_reauth (
  session_id UUID PRIMARY KEY REFERENCES "session" ("id") ON DELETE CASCADE,
  password_at TIMESTAMPTZ NOT NULL
);

-- Accounts whose device keys, API tokens, and OAuth grants are to be
-- revoked after Better Auth signed them out, disabled or deleted them, or
-- reset or changed their password (it deletes the sessions itself, in its
-- own transaction): written right after, worked off at once, and again
-- every minute until done, each in one transaction with the revocation.
-- No foreign key: a deleted account's keys still go.
CREATE TABLE credential_revocations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL,
  reason TEXT NOT NULL CHECK (reason ~ '^[a-z][a-z_]{0,39}$'),
  -- A password change keeps the session that changed it.
  keep_sessions BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_attempt_at TIMESTAMPTZ
);

CREATE INDEX credential_revocations_created_idx ON credential_revocations (created_at);
