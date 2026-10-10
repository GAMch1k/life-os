-- Gives the handed-over Google sources back to Calendar's in-process sync
-- (the release before P11-07): those that still have their legacy Google
-- calendar. A source the integration registered after the handover has no
-- legacy calendar and stays external (valid under 000014).
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;
-- Transaction-local: the journal entries of this migration's changes.
SELECT set_config('calendar.actor', 'system:google-calendar', true);

UPDATE events
SET source_kind = 'google', read_only_reason = NULL
WHERE source_kind = 'external'
  AND source_id IN (
      SELECT id FROM calendar_sources
      WHERE integration = 'google-calendar' AND NOT in_process AND google_calendar_id IS NOT NULL AND url IS NOT NULL
  );

UPDATE calendar_sources
SET in_process = true, kind = 'google', updated_at = now()
WHERE integration = 'google-calendar' AND NOT in_process AND google_calendar_id IS NOT NULL AND url IS NOT NULL;
