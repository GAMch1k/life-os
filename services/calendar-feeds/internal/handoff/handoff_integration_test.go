package handoff

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/testdb"
)

const (
	zawID     = "6b0f1d4e-8a1c-4c9e-9f3e-0d5b2a7c1e01"
	holidayID = "6b0f1d4e-8a1c-4c9e-9f3e-0d5b2a7c1e02"
	pausedID  = "6b0f1d4e-8a1c-4c9e-9f3e-0d5b2a7c1e03"
	// The personal URL the handoff must never print.
	zawURL = "https://service.mymuell.de/api.php?m=zaw&r=ical&city_id=104&area_id=50&street_name=Secretweg"
)

// legacyCalendar builds calendar_db as the release before P11-08 left it:
// Calendar's migrations up to 000014 in a throwaway schema, with three ICS
// feeds seeded before 000014 so its backfill gives them their integration
// identity, and an event of the ZAW feed.
func legacyCalendar(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testdb.Schema(t, "calendar_legacy")
	ctx := context.Background()
	// Calendar's own test packages may run against the same database:
	// install pgcrypto once outside the test schemas, as they do.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock(4210001)")
	if err == nil {
		_, err = conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public")
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock(4210001)")
	}
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	var integrationAPI string
	for _, path := range testdb.Migrations(t, testdb.CalendarMigrations(), "up") {
		base := filepath.Base(path)
		if base >= "000014" {
			if strings.HasPrefix(base, "000014_") {
				integrationAPI = path
			}
			continue
		}
		testdb.Apply(t, pool, path)
	}
	if integrationAPI == "" {
		t.Fatal("Calendar has no migration 000014")
	}
	preset, _ := json.Marshal(map[string]any{
		"name": "ZAW", "url": zawURL, "include": []string{"Bioabfall", "Papier"}, "exclude": []string{},
		"defaultReminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO calendar_sources (id, kind, name, url, enabled, filter_include, filter_exclude, default_reminders, preset, preset_config,
                              next_refresh_at, last_attempt_at, last_success_at, last_outcome, last_counts, consecutive_failures,
                              etag, last_modified, created_at, updated_at)
VALUES ($1, 'ics', 'Müllabfuhr', $2, true, '{Bioabfall,Papier}', '{}', '[{"kind":"relative","minutesBefore":90}]', 'zaw', $3,
        '2026-10-11T02:30:00Z', '2026-10-10T02:30:00Z', '2026-10-10T02:30:00Z', 'imported',
        '{"feedEvents":29,"imported":21,"inserted":0,"updated":0,"removed":0,"restored":0,"unchanged":21,"skipped":0}', 0,
        NULL, NULL, '2026-09-01T08:00:00Z', '2026-09-02T08:00:00Z'),
       ($4, 'ics', 'Holidays', 'https://feeds.example.test/holidays.ics', true, '{}', '{Observance}', NULL, NULL, NULL,
        '2026-10-10T12:15:00Z', '2026-10-10T11:00:00Z', '2026-10-09T02:30:00Z', 'failed', NULL, 2,
        '"v7"', 'Fri, 09 Oct 2026 02:30:00 GMT', '2026-09-03T08:00:00Z', '2026-09-03T08:00:00Z'),
       ($5, 'ics', 'Paused', 'https://feeds.example.test/paused.ics', false, '{}', '{}', NULL, NULL, NULL,
        NULL, NULL, NULL, NULL, NULL, 0, NULL, NULL, '2026-09-04T08:00:00Z', '2026-09-04T08:00:00Z')`,
		zawID, zawURL, preset, holidayID, pausedID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE calendar_sources SET last_error = 'the feed answered HTTP 503' WHERE id = $1`, holidayID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, time_zone)
VALUES ('Bioabfall', false, '2026-10-19T04:00:00Z', '2026-10-19T05:00:00Z', 'ics', $1, 'bio-20261019@mymuell', 'Europe/Berlin')`, zawID); err != nil {
		t.Fatal(err)
	}
	testdb.Apply(t, pool, integrationAPI)
	return pool
}

// handover applies Calendar's handover migration (whatever its number).
func handover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(testdb.CalendarMigrations(), "*_calendar_feeds_handover.up.sql"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("Calendar's handover migration: %v %v", paths, err)
	}
	testdb.Apply(t, pool, paths[0])
}

// readOnly is the calendar pool as the command opens it.
func readOnly(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	config := pool.Config().Copy()
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readonly, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonly.Close)
	return readonly
}

func id(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHandoffMovesCalendarsFeeds(t *testing.T) {
	ctx := context.Background()
	calendar := legacyCalendar(t)
	feedsDB := testdb.Open(t)
	queries := store.New(feedsDB)
	var before string
	if err := calendar.QueryRow(ctx, "SELECT string_agg(to_jsonb(s)::text, '|' ORDER BY id) FROM calendar_sources s").Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Before Calendar's handover migration the feeds are still in process.
	result, err := Run(ctx, readOnly(t, calendar), feedsDB)
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{Found: 3, HandedOver: 3, InProcess: 3, Presets: 1}) {
		t.Fatalf("result = %+v", result)
	}
	if printed := result.String(); strings.Contains(printed, "mymuell") || strings.Contains(printed, "Secretweg") || !strings.Contains(printed, "handed over now: 3") {
		t.Fatalf("printed %q", printed)
	}

	zaw, err := queries.GetFeed(ctx, id(t, zawID))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	_ = json.Unmarshal(zaw.PresetConfig, &config)
	if zaw.SourceID != zaw.ID || zaw.Name != "Müllabfuhr" || zaw.Url != zawURL || !zaw.Enabled || zaw.Preset == nil || *zaw.Preset != "zaw" ||
		!slices.Equal(zaw.FilterInclude, []string{"Bioabfall", "Papier"}) || len(zaw.FilterExclude) != 0 || config["url"] != zawURL || config["name"] != "ZAW" ||
		!zaw.RegisterPending || zaw.NextRefreshAt.Time.Format("2006-01-02T15:04Z07:00") != "2026-10-11T02:30Z" || deref(zaw.LastOutcome) != "imported" ||
		!strings.Contains(string(zaw.LastCounts), `"imported": 21`) || zaw.RefreshingUntil.Valid || zaw.CreatedAt.Time.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("ZAW = %+v (config %v)", zaw, config)
	}
	holidays, err := queries.GetFeed(ctx, id(t, holidayID))
	if err != nil {
		t.Fatal(err)
	}
	if holidays.Preset != nil || deref(holidays.Etag) != `"v7"` || deref(holidays.LastModified) != "Fri, 09 Oct 2026 02:30:00 GMT" || holidays.ConsecutiveFailures != 2 ||
		deref(holidays.LastError) != "the feed answered HTTP 503" || deref(holidays.LastOutcome) != "failed" || !slices.Equal(holidays.FilterExclude, []string{"Observance"}) {
		t.Fatalf("Holidays = %+v", holidays)
	}
	paused, err := queries.GetFeed(ctx, id(t, pausedID))
	if err != nil || paused.Enabled || paused.NextRefreshAt.Valid || paused.LastOutcome != nil {
		t.Fatalf("Paused = %+v, %v", paused, err)
	}

	// Calendar's handover migration, then the handoff again: nothing new,
	// nothing changed here, nothing still in process.
	handover(t, calendar)
	again, err := Run(ctx, readOnly(t, calendar), feedsDB)
	if err != nil {
		t.Fatal(err)
	}
	if again != (Result{Found: 3, AlreadyPresent: 3, Presets: 1}) {
		t.Fatalf("again = %+v", again)
	}
	if unchanged, _ := queries.GetFeed(ctx, id(t, holidayID)); unchanged.UpdatedAt != holidays.UpdatedAt || deref(unchanged.Etag) != `"v7"` {
		t.Fatalf("a second run changed a feed: %+v", unchanged)
	}

	// The handoff only read Calendar; its handover migration changed the
	// sources' kind and in_process, nothing else of the feed columns.
	var url, kind string
	var inProcess bool
	if err := calendar.QueryRow(ctx, "SELECT url, kind, in_process FROM calendar_sources WHERE id = $1", zawID).Scan(&url, &kind, &inProcess); err != nil ||
		url != zawURL || kind != "external" || inProcess {
		t.Fatalf("Calendar's ZAW source = %q %q %v, %v", url, kind, inProcess, err)
	}
	var eventKind string
	if err := calendar.QueryRow(ctx, "SELECT source_kind FROM events WHERE external_uid = 'bio-20261019@mymuell'").Scan(&eventKind); err != nil || eventKind != "external" {
		t.Fatalf("the ZAW event = %q, %v", eventKind, err)
	}
	_ = before
}

// A feed this service added before the handoff (calendar-feeds started too
// early) is replaced when it was never registered in Calendar, and refused
// (without the URL in the error) when it was.
func TestHandoffReplacesOnlyUnregisteredPresets(t *testing.T) {
	ctx := context.Background()
	calendar := legacyCalendar(t)
	feedsDB := testdb.Open(t)
	queries := store.New(feedsDB)
	early, err := queries.CreatePresetFeed(ctx, store.CreatePresetFeedParams{
		Name: "ZAW", Url: zawURL, FilterInclude: []string{}, FilterExclude: []string{}, Preset: "zaw", PresetConfig: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, calendar, feedsDB)
	if err != nil || result.Replaced != 1 || result.HandedOver != 3 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if _, err := queries.GetFeed(ctx, early.ID); err == nil {
		t.Fatal("the early preset feed is still there")
	}

	// A registered preset feed of another id: refused, nothing written.
	other := testdb.Open(t)
	otherQueries := store.New(other)
	registered, err := otherQueries.CreatePresetFeed(ctx, store.CreatePresetFeedParams{
		Name: "ZAW", Url: zawURL, FilterInclude: []string{}, FilterExclude: []string{}, Preset: "zaw", PresetConfig: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, "UPDATE feeds SET source_id = gen_random_uuid() WHERE id = $1", registered.ID); err != nil {
		t.Fatal(err)
	}
	_, err = Run(ctx, calendar, other)
	if err == nil || strings.Contains(err.Error(), "mymuell") || strings.Contains(err.Error(), "Secretweg") {
		t.Fatalf("conflict error = %v", err)
	}
	if rows, _ := otherQueries.ListFeeds(ctx); len(rows) != 1 {
		t.Fatalf("a refused handoff wrote %d feeds", len(rows))
	}
}
