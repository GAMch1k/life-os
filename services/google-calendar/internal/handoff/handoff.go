// Package handoff moves the Google Calendar connection from Calendar's
// database to this service's, once, when Calendar's in-process Google sync
// is switched off (P11-07): the connection with its sealed refresh token
// (so the owner does not reconnect), the calendar list with the owner's
// choices and each calendar's sync token and window, the pending
// authorizations, the raw Google events, and the export links of Life OS
// events with their etags (so nothing is duplicated in Google). Calendar's
// database is only read, in one read-only transaction; the legacy tables
// stay there until P11-09. Running it again copies nothing twice: every row
// keeps its id and is skipped when already here. It never prints tokens.
package handoff

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Counts says what the handoff found in Calendar's database (Read) and
// copied (Copied: rows not already here).
type Counts struct {
	Connections, Calendars, States, Events, Exports, Sources, SentEvents Pair
}

// Pair is one table's rows read and copied.
type Pair struct{ Read, Copied int }

// ErrNotHandedOver means Calendar still syncs Google itself: its handover
// migration (000015) has not run.
var ErrNotHandedOver = errors.New("Calendar still syncs Google itself (sources in process): run Calendar's migrations (000015) before the handoff")

// ErrOtherConnection means this service already has another connection.
var ErrOtherConnection = errors.New("google_calendar_db already holds another Google connection; the handoff copies nothing over it")

// OpenLegacy opens Calendar's database read-only: every transaction of the
// pool is read only.
func OpenLegacy(ctx context.Context, url string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("HANDOFF_CALENDAR_DATABASE_URL is not a valid Postgres URL")
	}
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	config.ConnConfig.RuntimeParams["application_name"] = "google-calendar-handoff"
	config.MaxConns = 2
	return pgxpool.NewWithConfig(ctx, config)
}

// table is one legacy table copied as it is: the rows read from
// Calendar's database (read, columns named as here) are written here
// through JSON, so every type keeps its value.
type table struct {
	name  string
	read  string
	count *Pair
}

// Run copies the legacy Google tables from legacy (Calendar's database)
// into target (this service's) in one transaction.
func Run(ctx context.Context, legacy, target *pgxpool.Pool) (Counts, error) {
	var counts Counts
	source, err := legacy.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return counts, fmt.Errorf("read Calendar's database: %w", err)
	}
	defer func() { _ = source.Rollback(context.WithoutCancel(ctx)) }()
	var inProcess int
	if err := source.QueryRow(ctx, `SELECT count(*) FROM calendar_sources WHERE integration = 'google-calendar' AND in_process`).Scan(&inProcess); err != nil {
		return counts, fmt.Errorf("read Calendar's sources (is Calendar migrated to 000015?): %w", err)
	}
	if inProcess > 0 {
		return counts, ErrNotHandedOver
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		return counts, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var legacyConnection, existing *string
	if err := source.QueryRow(ctx, `SELECT id::text FROM google_connections WHERE singleton`).Scan(&legacyConnection); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return counts, err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM google_connections WHERE singleton`).Scan(&existing); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return counts, err
	}
	if legacyConnection != nil && existing != nil && *legacyConnection != *existing {
		return counts, ErrOtherConnection
	}

	tables := []table{
		{
			name: "google_connections",
			read: `SELECT id, singleton, account_email, scopes, refresh_token_ciphertext, refresh_token_nonce, access_token_expires_at, status,
			              last_error, connected_at, created_at, updated_at, export_enabled, export_backfilled_at, lifeos_calendar_id,
			              lifeos_calendar_created_at, lifeos_calendar_note
			       FROM google_connections`,
			count: &counts.Connections,
		},
		{
			// Each calendar with its source in Calendar (handed over by
			// 000015), due for a sync now; its lease does not carry over.
			name: "google_calendars",
			read: `SELECT calendar.id, calendar.connection_id, calendar.google_calendar_id, calendar.summary, calendar.color, calendar.access_role,
			              calendar.is_primary, calendar.sync_enabled, calendar.created_at, calendar.updated_at, calendar.time_zone, calendar.sync_token,
			              calendar.sync_window_start, CASE WHEN calendar.sync_enabled THEN now() END AS next_sync_at, calendar.last_sync_attempt_at, calendar.last_synced_at,
			              calendar.last_sync_error, calendar.sync_failures, calendar.last_sync_counts, calendar.derived_at, source.id AS source_id
			       FROM google_calendars AS calendar
			       LEFT JOIN calendar_sources AS source ON source.google_calendar_id = calendar.id AND source.integration = 'google-calendar'`,
			count: &counts.Calendars,
		},
		{
			name:  "google_oauth_states",
			read:  `SELECT state_hash, code_verifier, expires_at, created_at FROM google_oauth_states WHERE expires_at > now()`,
			count: &counts.States,
		},
		{
			name:  "google_events",
			read:  `SELECT calendar_id, event_id, recurring_event_id, data, updated_at FROM google_events`,
			count: &counts.Events,
		},
		{
			// The export links with Google's etags; a lease does not carry
			// over. The event itself comes from Calendar's change feed.
			name: "google_exports",
			read: `SELECT event_id, connection_id, google_calendar_id, google_event_id, etag, google_updated, exported_exceptions, state, deleted,
			              requested_at, next_attempt_at, attempts, last_error, exported_at, created_at, updated_at
			       FROM google_exports`,
			count: &counts.Exports,
		},
		{
			// The events Calendar holds of each handed-over source: the
			// first sync sends them all again (what Calendar holds of them
			// is not known here) and removes those Google no longer has.
			name: "google_sent_events",
			read: `SELECT calendar.id AS calendar_id, event.external_uid
			       FROM events AS event
			       JOIN calendar_sources AS source ON source.id = event.source_id AND source.integration = 'google-calendar'
			       JOIN google_calendars AS calendar ON calendar.id = source.google_calendar_id
			       WHERE event.external_uid IS NOT NULL`,
			count: &counts.SentEvents,
		},
	}
	for _, step := range tables {
		if err := copyTable(ctx, source, tx, step); err != nil {
			return counts, fmt.Errorf("copy %s: %w", step.name, err)
		}
	}
	if err := source.QueryRow(ctx, `SELECT count(*) FROM calendar_sources WHERE integration = 'google-calendar' AND google_calendar_id IS NOT NULL`).Scan(&counts.Sources.Read); err != nil {
		return counts, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM google_calendars WHERE source_id IS NOT NULL`).Scan(&counts.Sources.Copied); err != nil {
		return counts, err
	}
	if err := tx.Commit(ctx); err != nil {
		return counts, err
	}
	return counts, nil
}

// copyTable copies one table's rows, skipping those already here.
func copyTable(ctx context.Context, source, target pgx.Tx, step table) error {
	rows, err := source.Query(ctx, "SELECT to_jsonb(legacy) FROM ("+step.read+") AS legacy")
	if err != nil {
		return err
	}
	var values [][]byte
	for rows.Next() {
		var row []byte
		if err := rows.Scan(&row); err != nil {
			rows.Close()
			return err
		}
		values = append(values, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	step.count.Read = len(values)
	// The table name is one of the constants above, never input.
	insert := "INSERT INTO " + step.name + " SELECT * FROM jsonb_populate_record(NULL::" + step.name + ", $1::jsonb) ON CONFLICT DO NOTHING"
	for _, row := range values {
		tag, err := target.Exec(ctx, insert, string(row))
		if err != nil {
			return err
		}
		step.count.Copied += int(tag.RowsAffected())
	}
	return nil
}

// Lines are the counts as the command prints them: numbers only.
func (c Counts) Lines() []string {
	line := func(name string, pair Pair) string {
		return fmt.Sprintf("%-20s %6d found, %6d copied (%d already here)", name, pair.Read, pair.Copied, pair.Read-pair.Copied)
	}
	return []string{
		line("connections", c.Connections),
		line("calendars", c.Calendars),
		line("oauth states", c.States),
		line("google events", c.Events),
		line("exports", c.Exports),
		line("event uids", c.SentEvents),
		fmt.Sprintf("%-20s %6d in Calendar, %6d linked here", "sources", c.Sources.Read, c.Sources.Copied),
	}
}
