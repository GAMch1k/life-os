# Calendar service

Calendar owns events in `calendar_db`: title, description, timed or all-day start/end values, recurrence, and optional external provider reference. It presents a stable event API and does not synchronize third-party calendars yet. Gateway base: `/api/calendar`.

All-day events use dates; timed events use timestamps. List requests require a time range. Invalid time/data combinations return 400 and unknown IDs return 404. Event creation supports `Idempotency-Key`: an identical repeat returns the original event, while conflicting reuse returns 409. `/health` and `/ready` are available. Later ICS/iCal providers and waste-collection feeds retain provider identity for idempotent synchronization; reminder intent stays separate from delivery.
