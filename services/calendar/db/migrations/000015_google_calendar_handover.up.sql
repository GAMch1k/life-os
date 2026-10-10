-- Google Calendar handover (P11-07). Calendar's in-process Google sync is
-- switched off: the google-calendar integration service syncs the owner's
-- Google calendars through the integration API from now on. Its sources
-- (backfilled by 000014 as integration google-calendar, external id the
-- Google calendar's id) are handed over: no longer in process, kind
-- external, and so are their events. Nothing is deleted: the events keep
-- their ids, uids, and the owner's reminders and priority, and the legacy
-- google_* tables (copied into google_calendar_db by `google-calendar
-- handoff`) and google_calendar_id stay until P11-09.
--
-- The change journal's triggers (000014) are deferred; this migration only
-- changes rows, but makes them fire per statement anyway, attributed to the
-- integration (its own changes, which it skips), so no trigger event is
-- left pending at commit.
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;
-- Transaction-local: the journal entries of this migration's changes.
SELECT set_config('calendar.actor', 'system:google-calendar', true);

UPDATE calendar_sources
SET in_process = false, kind = 'external', updated_at = now()
WHERE integration = 'google-calendar' AND in_process;

UPDATE events
SET source_kind = 'external'
WHERE source_kind = 'google'
  AND source_id IN (SELECT id FROM calendar_sources WHERE integration = 'google-calendar' AND NOT in_process);
