# calendar-feeds

The ICS/iCal feed integration (P11-08): it subscribes to calendar feeds, the
owner's ZAW waste calendar among them, refreshes them on its own schedule,
and syncs their events into Calendar only through Calendar's integration API
(`/v1/integrations/calendar-feeds/...`). It owns its database,
`calendar_feeds_db`. See [docs/services/calendar-feeds.md](../../docs/services/calendar-feeds.md).

## Layout

```text
api/openapi.yaml          # the feeds API (/v1/feeds)
cmd/server/               # the service
db/migrations/            # calendar_feeds_db
internal/ics/             # safe download, iCalendar parsing, type filter
internal/feeds/           # refresher, scheduler, ZAW preset, Calendar registration
internal/calendar/        # client of Calendar's integration API (+ fakecalendar for tests)
```

## Commands

```sh
make generate                     # sqlc and the OpenAPI server code
make test                         # unit tests; integration tests need CALENDAR_FEEDS_TEST_DATABASE_URL
make migrate-up DATABASE_URL='postgres://...'
```

The integration tests run against a throwaway Postgres named by
`CALENDAR_FEEDS_TEST_DATABASE_URL`, each in its own schema.
