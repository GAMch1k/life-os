-- P22-16 (docs/install/email.md): the account emails, when the owner set up
-- email (./setup.sh email). The address itself is Better Auth's
-- "user".email and "emailVerified"; nothing here holds a message.

-- Sign-in codes by email, a second factor: the accounts that turned them
-- on in Settings → Security. Better Auth's "twoFactorEnabled" stays on while
-- an account has a verified authenticator app or this
-- (src/lib/auth/email-account.ts).
CREATE TABLE email_sign_in_codes (
  user_id UUID PRIMARY KEY REFERENCES "user" ("id") ON DELETE CASCADE,
  enabled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The emails a person receives unasked (Settings → Email), each kind on or
-- off: no row is on. Only the person changes their own.
CREATE TABLE email_preferences (
  user_id UUID NOT NULL REFERENCES "user" ("id") ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind ~ '^[a-z][a-z_]{0,39}$'),
  enabled BOOLEAN NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, kind)
);

-- Security alert emails on their way: written with the security log's entry
-- (and by the host's commands), sent by the Dashboard at once, retried
-- every minute, and dropped after a day. What the email says comes from the
-- entry's codes: its kind, detail, address, and device label, never content.
-- to_address is the address it goes to when not the account's own (the old
-- one, when the address changed); deleted with the row once sent.
CREATE TABLE email_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES "user" ("id") ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind ~ '^[a-z][a-z_]{0,39}$'),
  event TEXT NOT NULL CHECK (event ~ '^[a-z][a-z0-9_]{0,63}$'),
  detail TEXT CHECK (detail ~ '^[a-z0-9][a-z0-9_.:-]{0,63}$'),
  address TEXT CHECK (address ~ '^[0-9A-Fa-f:.]{1,64}$'),
  device TEXT CHECK (device ~ '^[A-Za-z][A-Za-z ]{0,47}$'),
  to_address TEXT CHECK (length(to_address) <= 254),
  at TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX email_outbox_due_idx ON email_outbox (next_attempt_at);
