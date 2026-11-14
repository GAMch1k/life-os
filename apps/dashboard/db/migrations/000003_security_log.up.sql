-- The security log (docs/architecture/multi-user.md#logs): sign-ins, failed
-- sign-ins, account and admin actions, legacy access, and the host's
-- commands, with who did it, whom it concerned, the address, a short device
-- label, and the outcome. No content: every column is an id, a time, a
-- count, or a name from a fixed form, and the checks keep it that way. Kept
-- for one year (src/lib/auth/security-log.ts deletes older rows).
--
-- No foreign keys to "user": an entry outlives a deleted account for its
-- year.
CREATE TABLE security_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  -- When it happened; for an aggregate (legacy access, counted per day),
  -- the first time, and last_at the last.
  at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind TEXT NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{0,63}$'),
  outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'failed', 'refused')),
  -- Who did it: NULL for the host's commands and for someone not known.
  actor_user_id UUID,
  -- Whom it concerned.
  subject_user_id UUID,
  via TEXT CHECK (via IN ('session', 'legacy', 'host', 'device', 'token', 'mcp', 'delegation')),
  -- The client's address, as the gateway saw it.
  address TEXT CHECK (address ~ '^[0-9A-Fa-f:.]{1,64}$'),
  -- A short label such as "Chrome on Android", never the User-Agent.
  device TEXT CHECK (device ~ '^[A-Za-z][A-Za-z ]{0,47}$'),
  -- A code: the way in ("password", "passkey"), the new role, a reason.
  detail TEXT CHECK (detail ~ '^[a-z0-9][a-z0-9_.:-]{0,63}$'),
  count INTEGER NOT NULL DEFAULT 1 CHECK (count >= 1),
  CHECK (last_at >= at)
);

CREATE INDEX security_events_at_idx ON security_events (at DESC, id DESC);
CREATE INDEX security_events_last_at_idx ON security_events (last_at);
CREATE INDEX security_events_actor_idx ON security_events (actor_user_id, at DESC) WHERE actor_user_id IS NOT NULL;
CREATE INDEX security_events_subject_idx ON security_events (subject_user_id, at DESC) WHERE subject_user_id IS NOT NULL;
CREATE INDEX security_events_kind_idx ON security_events (kind, at DESC);
