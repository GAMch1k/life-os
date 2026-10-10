-- Gives the feeds back to Calendar's in-process refresher: the sources of
-- calendar-feeds that still have their feed URL (those from before the
-- handover) and their events become kind ics again. Sources calendar-feeds
-- registered itself have no URL in Calendar and stay external.
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;

UPDATE events
SET source_kind = 'ics'
WHERE source_kind = 'external'
  AND source_id IN (
      SELECT id FROM calendar_sources
      WHERE integration = 'calendar-feeds' AND NOT in_process AND url IS NOT NULL AND google_calendar_id IS NULL
  );

UPDATE calendar_sources
SET kind = 'ics', in_process = true
WHERE integration = 'calendar-feeds' AND NOT in_process AND url IS NOT NULL AND google_calendar_id IS NULL;
