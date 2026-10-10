-- Calendar integration API (P11-06). Integrations are separate services
-- (google-calendar, calendar-feeds) that give Calendar the events of their
-- sources, and follow the events created in Life OS, only through
-- Calendar's public API; Calendar names no provider. Expand only: every
-- legacy column and table stays (P11-09 drops them), and Calendar's
-- in-process Google and ICS adapters keep working as before.

-- Provider-neutral sources. A source belongs to one integration and is
-- known there by external_id (unique per integration): a Google calendar's
-- Google id, or for a feed the source's own id. provider is the label shown
-- for it (Google, ZAW, ICS); read_only says whether the source accepts
-- changes to its events.
--
-- in_process is true while Calendar's own adapter still syncs the source
-- (the ICS refresher or the Google syncer: every source from before the
-- integration services, of kind ics or google). A source an integration
-- registers through the API is of kind external, not in process, and only
-- those can be synced through it. The release that switches an adapter off
-- (P11-07, P11-08) hands its sources over: in_process false, kind external
-- (for the sources and their events), in one migration.
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

-- A source an integration syncs is of kind external and keeps no URL (a feed
-- URL is the integration's configuration); a handed-over Google source may
-- keep its legacy google_calendar_id until P11-09.
ALTER TABLE calendar_sources ALTER COLUMN url DROP NOT NULL;
ALTER TABLE calendar_sources
    DROP CONSTRAINT calendar_sources_kind_check,
    DROP CONSTRAINT calendar_sources_google_calendar_check,
    ADD CONSTRAINT calendar_sources_kind_check CHECK (kind IN ('ics', 'google', 'external')),
    ADD CONSTRAINT calendar_sources_external_check CHECK ((kind = 'external') = (NOT in_process)),
    ADD CONSTRAINT calendar_sources_google_calendar_check CHECK (google_calendar_id IS NULL OR kind <> 'ics'),
    ADD CONSTRAINT calendar_sources_in_process_google_check CHECK (NOT in_process OR (kind = 'google') = (google_calendar_id IS NOT NULL)),
    ADD CONSTRAINT calendar_sources_in_process_url_check CHECK (NOT in_process OR url IS NOT NULL);

-- Events brought in by an integration are of kind external. One the source
-- accepts changes to can still be read-only on its own (such as one
-- occurrence of a rule Calendar cannot hold): read_only_reason says why.
ALTER TABLE events
    DROP CONSTRAINT events_source_kind_check,
    ADD CONSTRAINT events_source_kind_check CHECK (source_kind IN ('manual', 'ics', 'google', 'external')),
    ADD COLUMN read_only_reason TEXT,
    ADD CONSTRAINT events_read_only_reason_check CHECK (
        read_only_reason IS NULL OR (source_kind = 'external' AND btrim(read_only_reason) <> '')
    );

-- The change feed of the events changed in Life OS that an integration may
-- write elsewhere: those created in Life OS (source_kind manual) and those
-- of a source an integration syncs that accepts changes (external, not
-- read-only). One entry per change, written by the triggers below in the
-- transaction of the change itself, so no path (the API, the in-process
-- Google reverse sync, an integration's sync, a future one) changes such an
-- event without an entry. actor is who made the change (the
-- X-Life-OS-Actor of the request: the writing transaction sets
-- calendar.actor, else system:calendar). source_id and external_uid are the
-- event's source identity, for its tombstone. The feed reads the events'
-- current state; an event that no longer exists is a tombstone. Deleting a
-- source deletes its events without entries: that never reaches the
-- provider.
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
    source_id UUID,
    external_uid TEXT,
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

-- Whether the feed follows an event of this kind and source: a Life OS
-- event, or one of a source an integration syncs that accepts changes (and
-- still exists).
CREATE FUNCTION calendar_event_followed(kind TEXT, source UUID) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT kind = 'manual' OR (kind = 'external' AND EXISTS (
        SELECT 1 FROM calendar_sources WHERE id = source AND NOT in_process AND NOT read_only
    ))
$$;

CREATE FUNCTION calendar_record_event_change(changed UUID, source UUID, uid TEXT) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('calendar_event_changes'));
    IF NOT EXISTS (
        SELECT 1 FROM calendar_event_changes
        WHERE event_id = changed AND transaction_id = pg_current_xact_id()::text::bigint
    ) THEN
        INSERT INTO calendar_event_changes (event_id, source_id, external_uid, actor)
        VALUES (changed, source, uid, COALESCE(NULLIF(current_setting('calendar.actor', true), ''), 'system:calendar'));
    END IF;
END
$$;

-- An event created, changed (anything but updated_at), or deleted.
CREATE FUNCTION calendar_events_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF calendar_event_followed(OLD.source_kind, OLD.source_id) THEN
            PERFORM calendar_record_event_change(OLD.id, OLD.source_id, OLD.external_uid);
        END IF;
    ELSIF (TG_OP = 'INSERT' OR (to_jsonb(NEW) - 'updated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at'))
        AND calendar_event_followed(NEW.source_kind, NEW.source_id) THEN
        PERFORM calendar_record_event_change(NEW.id, NEW.source_id, NEW.external_uid);
    END IF;
    RETURN NULL;
END
$$;

-- An occurrence's own changes or skip written, moved to another series (a
-- split), or removed: a change of its series. The series' row is read at
-- commit, so exceptions removed with their deleted series add nothing.
CREATE FUNCTION calendar_event_exceptions_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    series events%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' AND (to_jsonb(NEW) - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(OLD) - 'updated_at') THEN
        RETURN NULL;
    END IF;
    IF TG_OP <> 'INSERT' THEN
        SELECT * INTO series FROM events WHERE id = OLD.event_id;
        IF FOUND AND calendar_event_followed(series.source_kind, series.source_id) THEN
            PERFORM calendar_record_event_change(series.id, series.source_id, series.external_uid);
        END IF;
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND NEW.event_id <> OLD.event_id) THEN
        SELECT * INTO series FROM events WHERE id = NEW.event_id;
        IF FOUND AND calendar_event_followed(series.source_kind, series.source_id) THEN
            PERFORM calendar_record_event_change(series.id, series.source_id, series.external_uid);
        END IF;
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
