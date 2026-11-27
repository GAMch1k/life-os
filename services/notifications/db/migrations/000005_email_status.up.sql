-- P22-16 (docs/install/email.md): whether email works, for Status and
-- ./setup.sh status. One row, written after every send: the latest success,
-- the latest failure and its code, and since when sends fail. Never an
-- address, a message, or the SMTP settings (those stay in .env).
CREATE TABLE email_status (
  id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  last_success_at TIMESTAMPTZ,
  last_failure_at TIMESTAMPTZ,
  failing_since TIMESTAMPTZ,
  last_error_code TEXT CHECK (last_error_code ~ '^[a-z][a-z0-9_]{0,47}$'),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
