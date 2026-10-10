-- Calendar reminder delivery (P12-04). Calendar sends every effective
-- reminder to the Notifications service when it is due.
--
-- calendar_reminder_dispatch is the dispatcher's single state row:
-- last_run_at is the cursor (every reminder due up to it has been handled),
-- lease_owner/lease_until let one Calendar process run at a time, and the
-- rest is its health for the Status page. last_error stays until the next
-- error; compare last_error_at with last_success_at to tell if it is
-- current.
CREATE TABLE calendar_reminder_dispatch (
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

-- One row per reminder handled, by its notification dedupe key: sent,
-- missed (due while Calendar was down, past the grace window; recorded in
-- Activity instead), or failed (Notifications refused it). A reminder with a
-- row is never handled again, so a restart or two overlapping runs neither
-- repeat nor reclassify it. Rows are pruned once well past any reminder
-- window.
CREATE TABLE calendar_reminder_log (
    dedupe_key TEXT PRIMARY KEY CHECK (dedupe_key <> ''),
    due_at TIMESTAMPTZ NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('sent', 'missed', 'failed')),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX calendar_reminder_log_due_idx ON calendar_reminder_log (due_at);
