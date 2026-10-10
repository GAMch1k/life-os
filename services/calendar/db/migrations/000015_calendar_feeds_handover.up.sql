-- Calendar feeds handover (P11-08). The calendar-feeds integration service
-- takes over the ICS feeds Calendar refreshed itself (the in-process
-- refresher is gone): their sources stop being in process and become kind
-- external, and so do their events, so calendar-feeds syncs them through the
-- integration API under the identity migration 000014 gave them
-- (integration calendar-feeds, externalId the source's own id). The legacy
-- feed columns (url, filters, preset, refresh state) stay until P11-09: the
-- one-time `calendar-feeds handoff` reads them, after this migration, to
-- configure the feeds in calendar_feeds_db.
--
-- The journal triggers of migration 000014 are deferred; they fire now, per
-- statement, so a later migration in the same run can alter events.
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;

UPDATE calendar_sources
SET kind = 'external', in_process = false
WHERE integration = 'calendar-feeds' AND in_process;

UPDATE events
SET source_kind = 'external'
WHERE source_kind = 'ics'
  AND source_id IN (SELECT id FROM calendar_sources WHERE integration = 'calendar-feeds');
