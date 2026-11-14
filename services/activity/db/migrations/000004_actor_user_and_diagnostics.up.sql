-- The person an event is for (docs/architecture/multi-user.md#logs): the
-- user who signed in, or the first account under legacy access, also when a
-- development tool or the AI makes the change for them; NULL for a
-- service's own work and for every event recorded before accounts. P21-07
-- adds space_id next to it.
ALTER TABLE activity_events ADD COLUMN actor_user_id UUID;
CREATE INDEX activity_events_actor_user_idx ON activity_events (actor_user_id, occurred_at DESC)
    WHERE actor_user_id IS NOT NULL;

-- Server diagnostics: what went wrong where, for the server's admins, never
-- content. Every column is a name from Life OS's own code, a code, an id, a
-- number, or a time; there is no free-text column, and the checks keep it
-- that way. One row counts a burst of one code at one route of one service
-- (pkg/diagnostics sends them once a minute). Kept 90 days.
CREATE TABLE diagnostic_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    service TEXT NOT NULL CHECK (service ~ '^[a-z][a-z0-9-]{0,63}$'),
    code TEXT NOT NULL CHECK (code ~ '^[a-z][a-z_]{0,63}$'),
    route TEXT CHECK (char_length(route) <= 128 AND route ~ '^(/[A-Za-z0-9_.:{}*+-]*)+$'),
    status INTEGER CHECK (status BETWEEN 100 AND 599),
    subject_id UUID,
    request_id UUID,
    count INTEGER NOT NULL CHECK (count >= 1),
    duration_ms BIGINT CHECK (duration_ms >= 0),
    first_at TIMESTAMPTZ NOT NULL,
    last_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (last_at >= first_at)
);

CREATE INDEX diagnostic_events_last_idx ON diagnostic_events (last_at DESC, id DESC);
CREATE INDEX diagnostic_events_service_idx ON diagnostic_events (service, code, last_at DESC);
