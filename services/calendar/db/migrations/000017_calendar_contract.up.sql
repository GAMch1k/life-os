-- Calendar contract (P11-09). Calendar names no provider: Google Calendar
-- is the google-calendar integration (P11-07, handover 000016) and ICS
-- feeds are calendar-feeds (P11-08, handover 000015), each with its own
-- database. What only Calendar's former in-process adapters used goes:
-- the legacy google_* tables, the feed and Google columns of
-- calendar_sources (URL, filter, preset, refresh state, conditional-request
-- validators, google_calendar_id), in_process, and the kinds ics and google.
-- Every source is an integration's (kind external, with its identity and
-- provider label), and every event is a Life OS one (manual) or an
-- integration's (external).
--
-- The integration API keeps what it uses: integration, external_id,
-- provider, read_only, and integration_config (what the integration last
-- set of the owner's settings, so a registration changes only what the
-- integration changed since). What reminders took from the preset becomes
-- the source's own setting: private_location.
--
-- It refuses to run, changing nothing, while any source is still synced in
-- process or of kind ics or google, a source lacks its integration
-- identity, or an event is of kind ics or google: the handover migrations
-- (000015, 000016) and the integrations' handoffs come first, so this one
-- never destroys data an integration has not taken over.

DO $$
DECLARE
    legacy_sources BIGINT;
    legacy_events BIGINT;
BEGIN
    SELECT count(*) INTO legacy_sources FROM calendar_sources
    WHERE in_process OR kind <> 'external' OR integration IS NULL OR external_id IS NULL OR provider IS NULL;
    SELECT count(*) INTO legacy_events FROM events WHERE source_kind NOT IN ('manual', 'external');
    IF legacy_sources > 0 OR legacy_events > 0 THEN
        RAISE EXCEPTION 'calendar migration 000017 refused, nothing changed: % source(s) still synced in process, of kind ics or google, or without an integration identity, and % event(s) of kind ics or google', legacy_sources, legacy_events
            USING HINT = 'Hand Calendar''s sources over to their integrations first (migrations 000015 and 000016, then `calendar-feeds handoff` and `google-calendar handoff` of the release before P11-09), then migrate again.';
    END IF;
END
$$;

-- The journal triggers of migration 000014 are deferred; they fire now, per
-- statement, so the ALTER TABLE of events below never meets pending trigger
-- events (this migration changes no event row itself).
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;

-- The change feed follows a Life OS event, or one of a source that accepts
-- changes: every source is an integration's now.
CREATE OR REPLACE FUNCTION calendar_event_followed(kind TEXT, source UUID) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT kind = 'manual' OR (kind = 'external' AND EXISTS (
        SELECT 1 FROM calendar_sources WHERE id = source AND NOT read_only
    ))
$$;

ALTER TABLE events
    DROP CONSTRAINT events_source_kind_check,
    ADD CONSTRAINT events_source_kind_check CHECK (source_kind IN ('manual', 'external'));

-- Whether the locations of the source's events are private, such as the
-- owner's own address (the ZAW waste pickups at home): reminder
-- notifications leave them out. The integration says so when it registers
-- the source; until now Calendar knew it from the preset (ZAW), so the
-- preset's source keeps it.
ALTER TABLE calendar_sources ADD COLUMN private_location BOOLEAN NOT NULL DEFAULT false;
UPDATE calendar_sources SET private_location = true WHERE preset IS NOT NULL;

-- The sources: the constraints of the in-process era first, then their
-- columns (with their own checks and indexes: calendar_sources_due_idx,
-- calendar_sources_preset_key, and google_calendar_id's key and reference).
ALTER TABLE calendar_sources
    DROP CONSTRAINT calendar_sources_in_process_url_check,
    DROP CONSTRAINT calendar_sources_in_process_google_check,
    DROP CONSTRAINT calendar_sources_google_calendar_check,
    DROP CONSTRAINT calendar_sources_external_check,
    DROP CONSTRAINT calendar_sources_integration_identity_check,
    DROP CONSTRAINT calendar_sources_identity_check,
    DROP CONSTRAINT calendar_sources_kind_check;

ALTER TABLE calendar_sources
    DROP COLUMN url,
    DROP COLUMN filter_include,
    DROP COLUMN filter_exclude,
    DROP COLUMN next_refresh_at,
    DROP COLUMN refreshing_until,
    DROP COLUMN last_attempt_at,
    DROP COLUMN last_success_at,
    DROP COLUMN last_outcome,
    DROP COLUMN last_error,
    DROP COLUMN last_counts,
    DROP COLUMN consecutive_failures,
    DROP COLUMN etag,
    DROP COLUMN last_modified,
    DROP COLUMN preset,
    DROP COLUMN preset_config,
    DROP COLUMN google_calendar_id,
    DROP COLUMN in_process;

-- Every source belongs to an integration, which knows it by external_id
-- (unique per integration, calendar_sources_integration_key) and labels it.
ALTER TABLE calendar_sources
    ALTER COLUMN integration SET NOT NULL,
    ALTER COLUMN external_id SET NOT NULL,
    ALTER COLUMN provider SET NOT NULL,
    ADD CONSTRAINT calendar_sources_kind_check CHECK (kind = 'external');

-- The legacy tables of Calendar's former Google sync (google-calendar keeps
-- its own copies in google_calendar_db).
DROP TABLE google_exports;
DROP TABLE google_events;
DROP TABLE google_calendars;
DROP TABLE google_oauth_states;
DROP TABLE google_connections;
