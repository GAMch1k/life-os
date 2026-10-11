-- Expiry reminders (P15-04). Once a day Food tells the owner, through the
-- Notifications service, which in-stock pantry items expire today or
-- tomorrow.
--
-- food_notification_dispatch is the dispatcher's single state row:
-- last_run_at is the cursor (every reminder due up to it has been handled),
-- lease_owner/lease_until let one Food process run at a time, and the rest is
-- its health for GET /v1/notifications/status. last_error stays until the
-- next error; compare last_error_at with last_success_at to tell if it is
-- current.
CREATE TABLE food_notification_dispatch (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    last_run_at TIMESTAMPTZ,
    lease_owner TEXT CHECK (lease_owner IS NULL OR lease_owner <> ''),
    lease_until TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_error TEXT,
    last_error_at TIMESTAMPTZ,
    last_sent_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);

-- One row per reminder handled, by its notification dedupe key: sent, missed
-- (due while Food was down, past the grace window; recorded in Activity
-- instead), failed (Notifications refused it), or empty (nothing expired
-- soon, not sent). A key with a row is never handled again, so a restart or
-- two overlapping runs neither repeat nor reclassify it. Rows are pruned once
-- well past any run's window.
CREATE TABLE food_notification_log (
    dedupe_key TEXT PRIMARY KEY CHECK (dedupe_key <> ''),
    due_at TIMESTAMPTZ NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('sent', 'missed', 'failed', 'empty')),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX food_notification_log_due_idx ON food_notification_log (due_at);
