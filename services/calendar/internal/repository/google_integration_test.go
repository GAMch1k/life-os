package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The Google connection tables keep at most one connection, cascade its
// calendar list, and go away cleanly.
func TestGoogleConnectionMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "")
	insert := `INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce) VALUES ($1, '\x01', '\x02') RETURNING id`
	var connection string
	if err := pool.QueryRow(ctx, insert, "owner@example.com").Scan(&connection); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, "other@example.com"); err == nil || !strings.Contains(err.Error(), "singleton") {
		t.Fatalf("a second connection: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role) VALUES ($1, 'owner@example.com', 'Owner', 'owner')`, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role) VALUES ($1, 'x', 'X', 'admin')`, connection); err == nil {
		t.Fatal("an unknown access role was accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE google_connections SET status = 'revoked'`); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM google_connections`); err != nil {
		t.Fatal(err)
	}
	var calendars int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM google_calendars`).Scan(&calendars); err != nil || calendars != 0 {
		t.Fatalf("calendars after disconnect = %d, %v", calendars, err)
	}

	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000009_google_connection.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000009_google_connection.up.sql not found")
	}
	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'google_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("google tables after down = %d, %v", tables, err)
	}
	testdb.Apply(t, pool, migration)
}
