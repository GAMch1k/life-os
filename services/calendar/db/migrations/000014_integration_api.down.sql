DROP TRIGGER event_exceptions_record_change ON event_exceptions;
DROP TRIGGER events_record_change ON events;
DROP FUNCTION calendar_event_exceptions_changed();
DROP FUNCTION calendar_events_changed();
DROP FUNCTION calendar_record_event_change(UUID, UUID, TEXT);
DROP FUNCTION calendar_event_followed(TEXT, UUID);
DROP TABLE calendar_change_feed;
DROP TABLE calendar_event_changes;

-- A source an integration syncs (kind external) cannot exist without this
-- migration: rolling back deletes it, and its events with it.
DELETE FROM calendar_sources WHERE NOT in_process;

ALTER TABLE events
    DROP CONSTRAINT events_read_only_reason_check,
    DROP COLUMN read_only_reason,
    DROP CONSTRAINT events_source_kind_check,
    ADD CONSTRAINT events_source_kind_check CHECK (source_kind IN ('manual', 'ics', 'google'));

ALTER TABLE calendar_sources
    DROP CONSTRAINT calendar_sources_in_process_url_check,
    DROP CONSTRAINT calendar_sources_in_process_google_check,
    DROP CONSTRAINT calendar_sources_google_calendar_check,
    DROP CONSTRAINT calendar_sources_external_check,
    DROP CONSTRAINT calendar_sources_kind_check,
    ADD CONSTRAINT calendar_sources_kind_check CHECK (kind IN ('ics', 'google')),
    ADD CONSTRAINT calendar_sources_google_calendar_check CHECK ((kind = 'google') = (google_calendar_id IS NOT NULL));
ALTER TABLE calendar_sources ALTER COLUMN url SET NOT NULL;

DROP INDEX calendar_sources_integration_key;

ALTER TABLE calendar_sources
    DROP CONSTRAINT calendar_sources_integration_identity_check,
    DROP CONSTRAINT calendar_sources_identity_check,
    DROP COLUMN integration_config,
    DROP COLUMN in_process,
    DROP COLUMN read_only,
    DROP COLUMN provider,
    DROP COLUMN external_id,
    DROP COLUMN integration;
