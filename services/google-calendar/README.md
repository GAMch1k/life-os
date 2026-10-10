# Google Calendar integration

The `google-calendar` service connects the owner's Google account and syncs
the chosen Google calendars both ways with Calendar, through Calendar's
integration API only. See [docs/services/google-calendar.md](../../docs/services/google-calendar.md)
for what it does and [docs/operations/google-calendar.md](../../docs/operations/google-calendar.md)
for setup, the one-time handoff from Calendar, and troubleshooting.

## Layout

```text
api/openapi.yaml                 # the integration's own API (connection, calendars, exports)
cmd/server/                      # the service, and the `handoff` command
db/migrations/                   # google_calendar_db
db/queries/                      # sqlc input
internal/api/                    # HTTP handlers
internal/calendar/               # Calendar's integration and events API (client)
internal/google/                 # OAuth, sealing, Google's API, mapping, sync, exports, edits
internal/google/fakegoogle/      # a fake Google for tests
internal/handoff/                # the one-time copy from calendar_db
internal/ics, internal/recurrence, internal/sources  # copies used by the mapping
internal/testsupport/            # fake Google + the real Calendar for integration tests
```

## Commands

From this directory, with `DATABASE_URL` set to this service's database:

```sh
make generate
make test
make migrate-up DATABASE_URL='postgres://...'
make run DATABASE_URL='postgres://...'
```

Integration tests need a throwaway Postgres in
`GOOGLE_CALENDAR_TEST_DATABASE_URL`; they run Calendar's own server (built
from `services/calendar`) on a private schema of it and skip when it is unset.

The handoff (once, with Calendar stopped and migrated):

```sh
docker compose --profile handoff run --rm google-calendar-handoff
```
