-- Calendar integration API (P11-06). Integrations are separate services
-- (google-calendar, calendar-feeds) that give Calendar the events of their
-- sources, and follow the events created in Life OS, only through
-- Calendar's public API. Expand only: every legacy column and table stays
-- (P11-09 drops them), and Calendar's in-process Google and ICS adapters
-- keep working as before.

-- Provider-neutral source identity. A source belongs to one integration
-- and is known there by external_id (unique per integration): a Google
-- calendar's Google id, or for a feed the source's own id. provider is the
-- label shown for it (Google, ZAW, ICS); read_only says whether the source
-- accepts changes to its events.
--
-- in_process is true while Calendar's own adapter still syncs the source
-- (the ICS refresher or the Google syncer: every source from before the
-- integration services); a source an integration registers through the API
-- is false, and only those can be synced through it. The release that
-- switches an adapter off (P11-07, P11-08) sets it to false for the sources
-- the integration takes over.
--
-- integration_config is what the integration last set of the owner's
-- settings ({"name", "defaultReminders", "defaultPriority"}): registering
-- again changes only what the integration changed since, so the owner's own
-- edits stay (as a preset's preset_config does).
ALTER TABLE calendar_sources
    ADD COLUMN integration TEXT CONSTRAINT calendar_sources_integration_check CHECK (integration ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    ADD COLUMN external_id TEXT CONSTRAINT calendar_sources_external_id_check CHECK (external_id <> '' AND length(external_id) <= 1024),
    ADD COLUMN provider TEXT CONSTRAINT calendar_sources_provider_check CHECK (btrim(provider) <> ''),
    ADD COLUMN read_only BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN in_process BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN integration_config JSONB CONSTRAINT calendar_sources_integration_config_check CHECK (
        integration_config IS NULL OR jsonb_typeof(integration_config) = 'object'
    ),
    ADD CONSTRAINT calendar_sources_identity_check CHECK ((integration IS NULL) = (external_id IS NULL)),
    -- A source an integration syncs always has its identity.
    ADD CONSTRAINT calendar_sources_integration_identity_check CHECK (in_process OR integration IS NOT NULL);

-- Stable identity for the existing sources. Google: the Google calendar's
-- Google-side id; read-only unless the account may edit the calendar.
UPDATE calendar_sources AS source
SET integration = 'google-calendar', external_id = calendar.google_calendar_id, provider = 'Google',
    read_only = calendar.access_role NOT IN ('owner', 'writer')
FROM google_calendars AS calendar
WHERE source.kind = 'google' AND calendar.id = source.google_calendar_id;

-- ICS feeds: the source's own id; the ZAW preset shows as ZAW.
UPDATE calendar_sources
SET integration = 'calendar-feeds', external_id = id::text,
    provider = CASE WHEN preset = 'zaw' THEN 'ZAW' ELSE 'ICS' END, read_only = true
WHERE kind = 'ics';

-- What the integration last set: today's values, except that a preset's
-- name and default reminders are those its environment last applied, so an
-- owner's edit of them survives the integration registering it again.
UPDATE calendar_sources
SET integration_config = jsonb_build_object(
    'name', COALESCE(preset_config ->> 'name', name),
    'defaultReminders', CASE WHEN preset_config ? 'defaultReminders' THEN preset_config -> 'defaultReminders' ELSE default_reminders END,
    'defaultPriority', default_priority
);

CREATE UNIQUE INDEX calendar_sources_integration_key ON calendar_sources (integration, external_id);

-- A source an integration syncs has no Google calendar row (google_calendar_id
-- stays the in-process syncer's) and may have no URL (it is only a
-- reference link for it).
ALTER TABLE calendar_sources
    DROP CONSTRAINT calendar_sources_google_calendar_check,
    DROP CONSTRAINT calendar_sources_url_check,
    ADD CONSTRAINT calendar_sources_google_calendar_check CHECK (google_calendar_id IS NULL OR kind = 'google'),
    ADD CONSTRAINT calendar_sources_in_process_google_check CHECK (NOT in_process OR kind <> 'google' OR google_calendar_id IS NOT NULL),
    ADD CONSTRAINT calendar_sources_url_check CHECK (url <> '' OR NOT in_process);

-- The change feed of events created in Life OS (source_kind manual): one
-- entry per change, written by the triggers below in the transaction of the
-- change itself, so no path (the API, the in-process Google reverse sync, a
-- future one) can change such an event without an entry. actor is who made
-- the change (the X-Life-OS-Actor of the request; the writing transaction
-- sets calendar.actor, else system:calendar). The feed reads the events'
-- current state; an event that no longer exists is a tombstone.
--
-- Entries are numbered in commit order: the triggers are deferred to the
-- commit and append holding one advisory lock until the transaction ends,
-- so an entry still to come always gets a higher seq than every visible
-- one and a reader moving past a seq never skips one. A transaction writes
-- at most one entry per event (transaction_id).
--
-- Because the triggers are deferred, a later migration that changes rows of
-- events or event_exceptions and then alters either table in the same
-- transaction must first run SET CONSTRAINTS events_record_change,
-- event_exceptions_record_change IMMEDIATE (PostgreSQL refuses ALTER TABLE
-- while trigger events are pending).
CREATE TABLE calendar_event_changes (
    seq BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id UUID NOT NULL,
    actor TEXT NOT NULL CHECK (actor <> ''),
    transaction_id BIGINT NOT NULL DEFAULT pg_current_xact_id()::text::bigint,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX calendar_event_changes_changed_at_idx ON calendar_event_changes (changed_at);
CREATE INDEX calendar_event_changes_transaction_idx ON calendar_event_changes (event_id, transaction_id);

-- The feed's single state row: entries up to pruned_through were pruned
-- (older than the retention), so a cursor before it has expired.
CREATE TABLE calendar_change_feed (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    pruned_through BIGINT NOT NULL DEFAULT 0 CHECK (pruned_through >= 0),
    pruned_at TIMESTAMPTZ
);

INSERT INTO calendar_change_feed DEFAULT VALUES;

CREATE FUNCTION calendar_record_event_change(changed UUID) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('calendar_event_changes'));
    IF NOT EXISTS (
        SELECT 1 FROM calendar_event_changes
        WHERE event_id = changed AND transaction_id = pg_current_xact_id()::text::bigint
    ) THEN
        INSERT INTO calendar_event_changes (event_id, actor)
        VALUES (changed, COALESCE(NULLIF(current_setting('calendar.actor', true), ''), 'system:calendar'));
    END IF;
END
$$;

-- An event created, changed (anything but updated_at), or deleted.
CREATE FUNCTION calendar_events_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.source_kind = 'manual' THEN
            PERFORM calendar_record_event_change(OLD.id);
        END IF;
    ELSIF NEW.source_kind = 'manual'
        AND (TG_OP = 'INSERT' OR (to_jsonb(NEW) - 'updated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at')) THEN
        PERFORM calendar_record_event_change(NEW.id);
    END IF;
    RETURN NULL;
END
$$;

-- An occurrence's own changes or skip written, moved to another series (a
-- split), or removed: a change of its series. The series' row is read at
-- commit, so exceptions removed with their deleted series add nothing.
CREATE FUNCTION calendar_event_exceptions_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (to_jsonb(NEW) - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(OLD) - 'updated_at') THEN
        RETURN NULL;
    END IF;
    IF TG_OP <> 'INSERT' AND EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id AND source_kind = 'manual') THEN
        PERFORM calendar_record_event_change(OLD.event_id);
    END IF;
    IF TG_OP <> 'DELETE' AND (TG_OP = 'INSERT' OR NEW.event_id <> OLD.event_id)
        AND EXISTS (SELECT 1 FROM events WHERE id = NEW.event_id AND source_kind = 'manual') THEN
        PERFORM calendar_record_event_change(NEW.event_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER events_record_change
    AFTER INSERT OR UPDATE OR DELETE ON events
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION calendar_events_changed();

CREATE CONSTRAINT TRIGGER event_exceptions_record_change
    AFTER INSERT OR UPDATE OR DELETE ON event_exceptions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION calendar_event_exceptions_changed();
