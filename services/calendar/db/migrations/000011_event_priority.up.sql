-- Event priority (P12-05): how important an event is, low, medium, or high;
-- it becomes the importance of the event's reminder notifications.
--
-- events.priority is the event's own priority. NULL means the event has none
-- of its own and follows its default: its source's default_priority for an
-- imported event, medium for an event created in Life OS. Calendar stores an
-- own priority only when it differs from that default, so an imported event
-- follows a later change of its source's default unless the owner gave it a
-- different priority, which refreshes keep (like the owner's reminders).
-- Every existing event has none and so reads as medium.
ALTER TABLE events
    ADD COLUMN priority TEXT CONSTRAINT events_priority_check CHECK (priority IN ('low', 'medium', 'high'));

-- The default priority of a source's events; existing sources get medium.
ALTER TABLE calendar_sources
    ADD COLUMN default_priority TEXT NOT NULL DEFAULT 'medium'
        CONSTRAINT calendar_sources_default_priority_check CHECK (default_priority IN ('low', 'medium', 'high'));

-- One occurrence of a recurring event can have its own priority (This
-- event): event_exceptions.changes holds it as "priority", like its other
-- own fields; it needs no schema change.
