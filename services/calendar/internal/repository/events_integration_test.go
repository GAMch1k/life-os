package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

func at(value time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: value, Valid: true} }
func on(value time.Time) pgtype.Date        { return pgtype.Date{Time: value, Valid: true} }
func text(value string) *string             { return &value }

func source(t *testing.T, queries *store.Queries, name string) pgtype.UUID {
	t.Helper()
	created, err := queries.CreateSource(context.Background(), store.CreateSourceParams{
		Kind: "ics", Name: name, Url: "https://calendar.example.org/" + name + ".ics", Enabled: true, FilterInclude: []string{}, FilterExclude: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func manualAllDay(title string, first, last time.Time) store.CreateEventParams {
	return store.CreateEventParams{Title: title, AllDay: true, StartDate: on(first), EndDate: on(last), Reminders: []byte(`[]`), UseDefaultReminders: true, Status: "active", SourceKind: "manual"}
}

func manualTimed(title string, start, end time.Time) store.CreateEventParams {
	return store.CreateEventParams{Title: title, StartAt: at(start), EndAt: at(end), Reminders: []byte(`[]`), UseDefaultReminders: true, Status: "active", SourceKind: "manual"}
}

func TestEventPersistenceSupportsTimedAndAllDayEvents(t *testing.T) {
	ctx := context.Background()
	queries := store.New(testdb.Open(t, ""))
	start := time.Date(2026, time.October, 6, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	frequency := "yearly"
	interval := int32(1)
	timed, err := queries.CreateEvent(ctx, manualTimed("Standup", start, end))
	if err != nil {
		t.Fatal(err)
	}
	birthday := manualAllDay("Birthday", start, start)
	birthday.RecurrenceFrequency, birthday.RecurrenceInterval = &frequency, &interval
	allDay, err := queries.CreateEvent(ctx, birthday)
	if err != nil {
		t.Fatal(err)
	}
	if allDay.Status != "active" || allDay.SourceKind != "manual" || string(allDay.Reminders) != "[]" || !allDay.UseDefaultReminders {
		t.Fatalf("defaults: %+v", allDay)
	}
	// Listing returns the non-recurring event; the recurring one comes from
	// its own query, for the API to expand into occurrences.
	items, err := queries.ListEvents(ctx, store.ListEventsParams{TimeZone: "UTC", RangeStart: at(start), RangeEnd: at(end), PageLimit: 10})
	if err != nil || len(items) != 1 || items[0].Event.ID != timed.ID {
		t.Fatalf("events=%#v err=%v", items, err)
	}
	recurring, err := queries.ListRecurringEvents(ctx, store.ListRecurringEventsParams{TimeZone: "UTC", RangeStart: at(start.AddDate(1, 0, 0)), RangeEnd: at(end.AddDate(1, 0, 0))})
	if err != nil || len(recurring) != 1 || recurring[0].Event.ID != allDay.ID {
		t.Fatalf("recurring=%#v err=%v", recurring, err)
	}

	// The update writes the merged row, guarded by the updated_at it read.
	update := store.UpdateEventParams{ID: timed.ID, ExpectedUpdatedAt: timed.UpdatedAt, Title: "Planning", StartAt: timed.StartAt, EndAt: timed.EndAt, Location: text("Room 2"), Reminders: []byte(`[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`), Status: "active"}
	updated, err := queries.UpdateEvent(ctx, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Planning" || *updated.Location != "Room 2" || !strings.Contains(string(updated.Reminders), `"localTime": "19:00"`) || !updated.UpdatedAt.Time.After(timed.UpdatedAt.Time) {
		t.Fatalf("updated = %+v (reminders %s)", updated, updated.Reminders)
	}
	// A writer that read the old row finds nothing to update.
	if _, err = queries.UpdateEvent(ctx, update); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale update err = %v, want no rows", err)
	}

	if affected, err := queries.DeleteEvent(ctx, allDay.ID); err != nil || affected != 1 {
		t.Fatalf("delete=%d err=%v", affected, err)
	}
	key, hash := "processor-event-1", "payload-a"
	once := manualAllDay("Only once", start, start)
	once.AutomationIdempotencyKey, once.AutomationPayloadHash = &key, &hash
	first, err := queries.CreateEvent(ctx, once)
	if err != nil {
		t.Fatal(err)
	}
	// A replay after an Inbox bookkeeping crash must recover the original row.
	replayed, err := queries.CreateEvent(ctx, once)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed event = %#v, err = %v; want %s", replayed, err, first.ID.String())
	}
}

// Events stored before the lifecycle migration become active manual events;
// nothing else about them changes, and the migration reverses cleanly.
func TestLifecycleMigrationKeepsExistingEvents(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000003")
	var id pgtype.UUID
	err := pool.QueryRow(ctx, `INSERT INTO events (title, description, all_day, start_date, end_date, external_provider, external_id)
		VALUES ('Old holiday', 'kept', true, '2026-12-24', '2026-12-26', 'legacy', 'abc') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000004_event_lifecycle.up.sql" {
			lifecycle = path
		}
	}
	if lifecycle == "" {
		t.Fatal("000004_event_lifecycle.up.sql not found")
	}
	testdb.Apply(t, pool, lifecycle)
	// The generated queries name columns of later migrations, so the event is
	// read with the columns this one knows.
	var old struct {
		status, sourceKind                          string
		sourceID                                    pgtype.UUID
		cancelledAt                                 pgtype.Timestamptz
		reminders                                   []byte
		location, description, provider, externalID *string
		startDate                                   pgtype.Date
	}
	read := func() error {
		return pool.QueryRow(ctx, `SELECT status, source_kind, source_id, cancelled_at, reminders, location, description, external_provider, external_id, start_date
			FROM events WHERE id = $1`, id).Scan(&old.status, &old.sourceKind, &old.sourceID, &old.cancelledAt, &old.reminders,
			&old.location, &old.description, &old.provider, &old.externalID, &old.startDate)
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if old.status != "active" || old.sourceKind != "manual" || old.sourceID.Valid || old.cancelledAt.Valid || old.reminders != nil || old.location != nil {
		t.Fatalf("migrated event = %+v", old)
	}
	if *old.description != "kept" || *old.provider != "legacy" || *old.externalID != "abc" || old.startDate.Time.Format(time.DateOnly) != "2026-12-24" {
		t.Fatalf("existing fields changed: %+v", old)
	}
	testdb.Apply(t, pool, strings.Replace(lifecycle, ".up.sql", ".down.sql", 1))
	testdb.Apply(t, pool, lifecycle)
	if err := read(); err != nil {
		t.Fatalf("after down and up again: %v", err)
	}
}

// The reminder-defaults migration keeps the reminders that apply to every
// event: one that followed the defaults keeps them with none of its own, an
// explicit list (even an empty one) applies instead of the defaults. Going
// back down restores the old form.
func TestReminderDefaultsMigrationKeepsEffectiveReminders(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000006")
	insert := func(title string, reminders any) pgtype.UUID {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_date, end_date, reminders)
			VALUES ($1, true, '2026-11-17', '2026-11-17', $2::jsonb) RETURNING id`, title, reminders).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	evening := `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`
	defaults, none, own := insert("Defaults", nil), insert("None", "[]"), insert("Own", evening)
	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000007_reminder_defaults.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000007_reminder_defaults.up.sql not found")
	}
	testdb.Apply(t, pool, migration)
	queries := store.New(pool)
	for id, want := range map[pgtype.UUID]struct {
		reminders   string
		useDefaults bool
	}{defaults: {"[]", true}, none: {"[]", false}, own: {evening, false}} {
		event, err := queries.GetEvent(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, event.Reminders, want.reminders) || event.UseDefaultReminders != want.useDefaults {
			t.Errorf("%s: reminders %s, defaults %v; want %s, %v", event.Title, event.Reminders, event.UseDefaultReminders, want.reminders, want.useDefaults)
		}
	}
	// New events start with defaults on and none of their own.
	created, err := queries.CreateEvent(ctx, store.CreateEventParams{Title: "New", AllDay: true, StartDate: on(time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC)), EndDate: on(time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC)),
		Reminders: []byte(`[]`), UseDefaultReminders: true, Status: "active", SourceKind: "manual"})
	if err != nil || !created.UseDefaultReminders {
		t.Fatalf("created = %+v, %v", created, err)
	}
	var settings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendar_settings`).Scan(&settings); err != nil || settings != 0 {
		t.Fatalf("settings rows = %d, %v; want none before the first change", settings, err)
	}

	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	for id, want := range map[pgtype.UUID]*string{defaults: nil, none: text("[]"), own: &evening} {
		var reminders []byte
		if err := pool.QueryRow(ctx, `SELECT reminders FROM events WHERE id = $1`, id).Scan(&reminders); err != nil {
			t.Fatal(err)
		}
		if (want == nil) != (reminders == nil) || (want != nil && !sameJSON(t, reminders, *want)) {
			t.Errorf("down: reminders %s, want %v", reminders, want)
		}
	}
	testdb.Apply(t, pool, migration)
}

func sameJSON(t *testing.T, raw []byte, want string) bool {
	t.Helper()
	var got, expected any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(got, expected)
}

// The schema keeps the lifecycle invariants even for writers that bypass the
// API, such as the future ICS importer.
func TestEventConstraints(t *testing.T) {
	ctx := context.Background()
	queries := store.New(testdb.Open(t, ""))
	pickup := time.Date(2026, time.November, 17, 5, 0, 0, 0, time.UTC)
	feed, secondFeed := source(t, queries, "zaw"), source(t, queries, "second")
	imported := func(uid string) store.CreateEventParams {
		params := manualTimed("Papier", pickup, pickup.Add(30*time.Minute))
		params.SourceKind, params.SourceID, params.ExternalUid = "ics", feed, text(uid)
		return params
	}
	if _, err := queries.CreateEvent(ctx, imported("zaw-1")); err != nil {
		t.Fatalf("imported event: %v", err)
	}
	other := imported("zaw-1")
	other.SourceID = secondFeed
	if _, err := queries.CreateEvent(ctx, other); err != nil {
		t.Fatalf("the same UID from another source: %v", err)
	}
	removed := imported("zaw-2")
	removed.Status, removed.CancelledAt, removed.RemovedFromSourceAt = "cancelled", at(pickup), at(pickup)
	if _, err := queries.CreateEvent(ctx, removed); err != nil {
		t.Fatalf("an event removed by its source: %v", err)
	}

	type violation struct {
		constraint string
		params     store.CreateEventParams
	}
	timed := func() store.CreateEventParams { return manualTimed("Manual", pickup, pickup.Add(time.Hour)) }
	with := func(base store.CreateEventParams, change func(*store.CreateEventParams)) store.CreateEventParams {
		change(&base)
		return base
	}
	violations := map[string]violation{
		"duplicate source UID": {"events_source_identity_key", imported("zaw-1")},
		"manual with a source": {"events_source_identity_check", with(timed(), func(p *store.CreateEventParams) { p.SourceID = feed })},
		"imported without UID": {"events_source_identity_check", with(imported("x"), func(p *store.CreateEventParams) { p.ExternalUid = nil })},
		"unknown source kind":  {"events_source_kind_check", with(imported("y"), func(p *store.CreateEventParams) { p.SourceKind = "outlook" })},
		"manual removed": {"events_removed_from_source_check", with(timed(), func(p *store.CreateEventParams) {
			p.Status, p.CancelledAt, p.RemovedFromSourceAt = "cancelled", at(pickup), at(pickup)
		})},
		"removed but active":          {"events_removed_from_source_check", with(imported("z"), func(p *store.CreateEventParams) { p.RemovedFromSourceAt = at(pickup) })},
		"cancelled without a moment":  {"events_cancelled_at_check", with(timed(), func(p *store.CreateEventParams) { p.Status = "cancelled" })},
		"active with a cancel moment": {"events_cancelled_at_check", with(timed(), func(p *store.CreateEventParams) { p.CancelledAt = at(pickup) })},
		"unknown status":              {"events_status_check", with(timed(), func(p *store.CreateEventParams) { p.Status = "deleted" })},
		"all-day with a zone": {"events_time_zone_check", with(manualAllDay("All day", pickup, pickup), func(p *store.CreateEventParams) {
			p.TimeZone = text("Europe/Berlin")
		})},
		"reminders not a list": {"events_reminders_check", with(timed(), func(p *store.CreateEventParams) { p.Reminders = []byte(`{"kind":"relative"}`) })},
	}
	for name, tc := range violations {
		_, err := queries.CreateEvent(ctx, tc.params)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.ConstraintName != tc.constraint {
			t.Errorf("%s: err = %v, want a %s violation", name, err, tc.constraint)
		}
	}
}
