package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The integration test runs real backups against the throwaway PostgreSQL
// named by BACKUP_TEST_DATABASE_URL (a superuser URL, never a live server).
// It skips when that is unset. pg_dump and pg_restore are those on PATH, or
// BACKUP_TEST_PG_DUMP and BACKUP_TEST_PG_RESTORE (testdata/pg_dump and
// testdata/pg_restore run them from the PostgreSQL image).
type integration struct {
	t         *testing.T
	ctx       context.Context
	adminURL  *url.URL
	suffix    string
	pgDump    string
	pgRestore string
}

func newIntegration(t *testing.T) *integration {
	raw := os.Getenv("BACKUP_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("BACKUP_TEST_DATABASE_URL is not set")
	}
	adminURL, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &integration{
		t:         t,
		ctx:       context.Background(),
		adminURL:  adminURL,
		suffix:    fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000),
		pgDump:    envOr("BACKUP_TEST_PG_DUMP", "pg_dump"),
		pgRestore: envOr("BACKUP_TEST_PG_RESTORE", "pg_restore"),
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (it *integration) name(base string) string { return base + "_" + it.suffix }

// connect opens a connection to database as user (the admin when user is
// empty).
func (it *integration) connect(database, user, password string) *pgx.Conn {
	it.t.Helper()
	u := *it.adminURL
	u.Path = "/" + database
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	conn, err := pgx.Connect(it.ctx, u.String())
	if err != nil {
		it.t.Fatalf("connect to %s as %s: %v", database, user, err)
	}
	it.t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func (it *integration) exec(conn *pgx.Conn, sql string) {
	it.t.Helper()
	if _, err := conn.Exec(it.ctx, sql); err != nil {
		it.t.Fatalf("%s: %v", sql, err)
	}
}

// connection is how the service would reach the server, as role.
func (it *integration) connection(role, password string) Connection {
	it.t.Helper()
	u := *it.adminURL
	u.Path = "/"
	u.User = url.User(role)
	connection, err := ParseConnection(u.String(), password)
	if err != nil {
		it.t.Fatal(err)
	}
	return connection
}

func TestBackupsAreConsistentAndRestore(t *testing.T) {
	it := newIntegration(t)
	admin := it.connect(it.adminURL.Path[1:], "", "")
	owner, backupRole := it.name("bt_owner"), it.name("bt_backup")
	alpha, beta, gamma, scratch := it.name("bt_alpha"), it.name("bt_beta"), it.name("bt_gamma"), it.name("bt_restore")

	// Roles and databases as infra/postgres provisions them: a database per
	// owner without PUBLIC access, and the backup role with
	// pg_read_all_data and CONNECT on the databases it backs up (not gamma).
	it.exec(admin, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'owner-pw'", owner))
	it.exec(admin, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'backup-pw' NOSUPERUSER CREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", backupRole))
	it.exec(admin, fmt.Sprintf("GRANT pg_read_all_data TO %s", backupRole))
	t.Cleanup(func() {
		for _, database := range []string{alpha, beta, gamma, scratch} {
			_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
		}
		for _, role := range []string{owner, backupRole} {
			_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
		}
	})
	for _, database := range []string{alpha, beta, gamma} {
		it.exec(admin, fmt.Sprintf("CREATE DATABASE %s OWNER %s", database, owner))
		it.exec(admin, fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC", database))
		if database != gamma {
			it.exec(admin, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", database, backupRole))
		}
	}
	alphaOwner := it.connect(alpha, owner, "owner-pw")
	for _, sql := range []string{
		"CREATE TABLE notes (id serial PRIMARY KEY, body text NOT NULL)",
		"INSERT INTO notes (body) SELECT 'note ' || n FROM generate_series(1, 25) AS n",
		"CREATE TABLE empty_table (id int)",
		"CREATE SCHEMA extra",
		"CREATE TABLE extra.items (id int)",
		"INSERT INTO extra.items SELECT generate_series(1, 7)",
		fmt.Sprintf("GRANT SELECT ON extra.items TO %s", backupRole),
		"CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)",
		"INSERT INTO schema_migrations VALUES (3, false)",
	} {
		it.exec(alphaOwner, sql)
	}
	betaOwner := it.connect(beta, owner, "owner-pw")
	for _, sql := range []string{
		"CREATE TABLE events (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, at timestamptz NOT NULL DEFAULT now())",
		"INSERT INTO events (at) SELECT now() FROM generate_series(1, 40)",
		"CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)",
		"INSERT INTO schema_migrations VALUES (5, true)",
	} {
		it.exec(betaOwner, sql)
	}

	// The backup role reads but cannot write.
	backupConn := it.connect(alpha, backupRole, "backup-pw")
	if _, err := backupConn.Exec(it.ctx, "INSERT INTO notes (body) VALUES ('nope')"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("the backup role could write: %v", err)
	}

	dir := t.TempDir()
	registry := filepath.Join(dir, "service-databases.conf")
	if err := os.WriteFile(registry, []byte(fmt.Sprintf("alpha|%s|%s|LIFE_OS_ALPHA_DB_PASSWORD\nbeta|%s|%s|LIFE_OS_BETA_DB_PASSWORD\n", alpha, owner, beta, owner)), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "backups"))
	if err == nil {
		t.Fatal("OpenStore created a missing backup directory")
	}
	if err := os.Mkdir(filepath.Join(dir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if store, err = OpenStore(filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	dumper := &Dumper{Connection: it.connection(backupRole, "backup-pw"), PgDump: it.pgDump, PgRestore: it.pgRestore}
	// A write committed after the snapshot, before pg_dump reads alpha:
	// neither the counts nor the dump may contain it.
	var lateWrite error
	dumper.afterSnapshot = func(ctx context.Context, database string) {
		if database == alpha {
			_, lateWrite = alphaOwner.Exec(ctx, "INSERT INTO notes (body) SELECT 'late ' || n FROM generate_series(1, 10) AS n")
		}
	}
	runner := NewRunner(RunnerOptions{
		Store:     store,
		Dumper:    dumper,
		Registry:  func() ([]Database, error) { return LoadDatabases(registry) },
		Retention: Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3},
	})
	t.Cleanup(func() { _ = runner.Close(context.Background()) })

	job, err := runner.Start(Request{Trigger: TriggerManual, Actor: "dev:test"})
	if err != nil {
		t.Fatal(err)
	}
	summary := waitLong(t, job)
	if lateWrite != nil {
		t.Fatalf("the write during the backup failed: %v", lateWrite)
	}
	if summary.Result != ResultSucceeded || summary.ServerVersion == "" {
		t.Fatalf("run = %+v", summary)
	}
	runs, err := store.Runs()
	if err != nil || len(runs) != 1 || runs[0].Err != nil {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	manifest := runs[0].Manifest
	if !strings.HasPrefix(manifest.PgDumpVersion, "pg_dump (PostgreSQL) ") {
		t.Errorf("pgDumpVersion = %q", manifest.PgDumpVersion)
	}
	byName := map[string]DatabaseResult{}
	for _, database := range manifest.Databases {
		byName[database.Name] = database
	}
	wantRows := map[string]map[string]int64{
		alpha: {"extra.items": 7, "public.empty_table": 0, "public.notes": 25, "public.schema_migrations": 1},
		beta:  {"public.events": 40, "public.schema_migrations": 1},
	}
	wantMigration := map[string]Migration{alpha: {Version: 3}, beta: {Version: 5, Dirty: true}}
	runDir := filepath.Join(store.Root(), AutoDir, manifest.ID)
	for database, want := range wantRows {
		result := byName[database]
		if result.Result != ResultSucceeded || result.File != database+".dump" {
			t.Fatalf("%s = %+v", database, result)
		}
		if got := rowsOf(result.Tables); !maps.Equal(got, want) {
			t.Errorf("%s rows = %v, want %v", database, got, want)
		}
		if result.Migration == nil || *result.Migration != wantMigration[database] {
			t.Errorf("%s migration = %+v", database, result.Migration)
		}
		data, err := os.ReadFile(filepath.Join(runDir, result.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != result.Bytes || hex.EncodeToString(sum[:]) != result.SHA256 || !strings.HasPrefix(string(data), "PGDMP") {
			t.Errorf("%s: %d bytes, sha256 %s; manifest says %d, %s", database, len(data), hex.EncodeToString(sum[:]), result.Bytes, result.SHA256)
		}
	}
	var live int64
	if err := alphaOwner.QueryRow(it.ctx, "SELECT count(*) FROM notes").Scan(&live); err != nil || live != 35 {
		t.Fatalf("live notes = %d, %v", live, err)
	}

	// Restore alpha into a scratch database: the counts are the manifest's,
	// owners and privileges came along.
	it.exec(admin, "CREATE DATABASE "+scratch)
	restore := exec.Command(it.pgRestore, "--dbname="+scratch, "--exit-on-error", "--no-password", filepath.Join(runDir, alpha+".dump"))
	restoreConnection := it.connection(it.adminURL.User.Username(), "")
	if password, ok := it.adminURL.User.Password(); ok {
		restoreConnection.Password = password
	}
	restore.Env = restoreConnection.env(scratch)
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("pg_restore: %v\n%s", err, output)
	}
	restored := it.connect(scratch, "", "")
	for table, want := range wantRows[alpha] {
		var got int64
		if err := restored.QueryRow(it.ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil || got != want {
			t.Errorf("restored %s = %d, %v; want %d", table, got, err, want)
		}
	}
	var tableOwner string
	var readable bool
	if err := restored.QueryRow(it.ctx, "SELECT tableowner FROM pg_tables WHERE tablename = 'notes'").Scan(&tableOwner); err != nil || tableOwner != owner {
		t.Errorf("restored owner = %q, %v", tableOwner, err)
	}
	if err := restored.QueryRow(it.ctx, fmt.Sprintf("SELECT has_table_privilege('%s', 'extra.items', 'SELECT')", backupRole)).Scan(&readable); err != nil || !readable {
		t.Errorf("restored privilege = %v, %v", readable, err)
	}

	// A database the role cannot reach fails alone; the run is failed and
	// keeps the others' dumps.
	if err := os.WriteFile(registry, []byte(fmt.Sprintf("alpha|%s|%s|LIFE_OS_ALPHA_DB_PASSWORD\ngamma|%s|%s|LIFE_OS_GAMMA_DB_PASSWORD\nbeta|%s|%s|LIFE_OS_BETA_DB_PASSWORD\n", alpha, owner, gamma, owner, beta, owner)), 0o600); err != nil {
		t.Fatal(err)
	}
	dumper.afterSnapshot = nil
	job, err = runner.Start(Request{Trigger: TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	summary = waitLong(t, job)
	if summary.Result != ResultFailed || len(summary.Databases) != 3 {
		t.Fatalf("run = %+v", summary)
	}
	if failed := summary.Databases[1]; failed.Name != gamma || failed.Result != ResultFailed || !strings.Contains(failed.Error, "permission denied") {
		t.Errorf("gamma = %+v", failed)
	}
	for _, i := range []int{0, 2} {
		database := summary.Databases[i]
		if database.Result != ResultSucceeded {
			t.Errorf("%s = %+v", database.Name, database)
		}
		if _, err := os.Stat(filepath.Join(store.Root(), AutoDir, summary.ID, database.File)); err != nil {
			t.Error(err)
		}
	}
	if rows := summary.Databases[0].Rows; rows != 35+7+1 {
		t.Errorf("alpha rows in the second run = %d, want 43", rows)
	}
}

func waitLong(t *testing.T, job *Job) Summary {
	t.Helper()
	select {
	case <-job.Done():
		return job.Summary()
	case <-time.After(5 * time.Minute):
		t.Fatal("the backup did not finish")
		return Summary{}
	}
}

func rowsOf(tables []TableRows) map[string]int64 {
	rows := map[string]int64{}
	for _, table := range tables {
		rows[table.Schema+"."+table.Name] = table.Rows
	}
	return rows
}

func TestRestoreChecksRestoreEveryDumpAndDropTheirScratchDatabases(t *testing.T) {
	it := newIntegration(t)
	admin := it.connect(it.adminURL.Path[1:], "", "")
	owner, backupRole := it.name("rc_owner"), it.name("rc_backup")
	alpha, beta, gamma := it.name("rc_alpha"), it.name("rc_beta"), it.name("rc_gamma")
	foreign := ScratchName(it.name("foreign"))
	it.exec(admin, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'owner-pw'", owner))
	it.exec(admin, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'backup-pw' NOSUPERUSER CREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", backupRole))
	it.exec(admin, fmt.Sprintf("GRANT pg_read_all_data TO %s", backupRole))
	t.Cleanup(func() {
		for _, database := range []string{alpha, beta, gamma, foreign, ScratchName(alpha), ScratchName(beta), ScratchName(it.name("left"))} {
			_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
		}
		for _, role := range []string{owner, backupRole} {
			_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
		}
	})
	for _, database := range []string{alpha, beta, gamma} {
		it.exec(admin, fmt.Sprintf("CREATE DATABASE %s OWNER %s", database, owner))
		it.exec(admin, fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC", database))
		if database != gamma {
			it.exec(admin, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", database, backupRole))
		}
	}
	// What the services' migrations do: pgcrypto (a trusted extension the
	// owner creates), defaults that call it, privileges, schema_migrations.
	alphaOwner := it.connect(alpha, owner, "owner-pw")
	for _, sql := range []string{
		"CREATE EXTENSION IF NOT EXISTS pgcrypto",
		"CREATE TABLE accounts (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL)",
		"INSERT INTO accounts (name) SELECT 'account ' || n FROM generate_series(1, 12) AS n",
		"CREATE TYPE kind AS ENUM ('a', 'b')",
		"CREATE TABLE entries (id bigserial PRIMARY KEY, account_id uuid REFERENCES accounts, kind kind NOT NULL DEFAULT 'a')",
		"INSERT INTO entries (account_id) SELECT id FROM accounts",
		"CREATE VIEW account_names AS SELECT name FROM accounts",
		"CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)",
		"INSERT INTO schema_migrations VALUES (8, false)",
	} {
		it.exec(alphaOwner, sql)
	}
	betaOwner := it.connect(beta, owner, "owner-pw")
	it.exec(betaOwner, "CREATE TABLE items (id int)")
	it.exec(betaOwner, "INSERT INTO items SELECT generate_series(1, 30)")

	dir := t.TempDir()
	registry := filepath.Join(dir, "service-databases.conf")
	writeRegistry := func(names ...string) {
		t.Helper()
		var lines []string
		for _, name := range names {
			lines = append(lines, fmt.Sprintf("svc%d|%s|%s|LIFE_OS_SVC_DB_PASSWORD", len(lines), name, owner))
		}
		if err := os.WriteFile(registry, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistry(alpha, beta)
	if err := os.Mkdir(filepath.Join(dir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	connection := it.connection(backupRole, "backup-pw")
	runner := NewRunner(RunnerOptions{
		Store:     store,
		Dumper:    &Dumper{Connection: connection, PgDump: it.pgDump, PgRestore: it.pgRestore},
		Restorer:  &Restorer{Connection: connection, PgRestore: it.pgRestore},
		Registry:  func() ([]Database, error) { return LoadDatabases(registry) },
		Retention: Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3},
	})
	t.Cleanup(func() { _ = runner.Close(context.Background()) })

	// A nightly run followed by its restore check.
	job, err := runner.Start(Request{Trigger: TriggerSchedule, ThenRestoreCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	run := waitLong(t, job)
	if run.Result != ResultSucceeded {
		t.Fatalf("run = %+v", run)
	}
	checkJob := runner.CurrentCheck()
	if checkJob == nil {
		t.Fatal("no restore check followed the run")
	}
	check := waitCheckLong(t, checkJob)
	if !check.OK || len(check.Databases) != 2 || check.RunID != run.ID {
		t.Fatalf("check = %+v", check)
	}
	if alphaCheck := check.Databases[0]; alphaCheck.Tables != 3 || alphaCheck.Rows != 12+12+1 {
		t.Errorf("alpha check = %+v", alphaCheck)
	}
	it.requireNoDatabase(admin, ScratchName(alpha), ScratchName(beta))

	// A manifest whose counts the dump does not hold, and a damaged dump.
	runDir := filepath.Join(store.Root(), AutoDir, run.ID)
	manifest, err := readManifest(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Databases[0].Tables {
		if manifest.Databases[0].Tables[i].Name == "accounts" {
			manifest.Databases[0].Tables[i].Rows = 13
		}
	}
	manifest.Databases[0].Migration = &Migration{Version: 9}
	if err := writeJSONAtomic(runDir, ManifestFile, manifest); err != nil {
		t.Fatal(err)
	}
	dump, err := os.OpenFile(filepath.Join(runDir, manifest.Databases[1].File), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = dump.Write([]byte{0})
	dump.Close()
	// And a scratch database a crash left behind, beside one the role does
	// not own, which it must leave alone.
	backupConn := it.connect(alpha, backupRole, "backup-pw")
	it.exec(backupConn, "CREATE DATABASE "+ScratchName(it.name("left")))
	it.exec(admin, "CREATE DATABASE "+foreign)

	checkJob, err = runner.StartRestoreCheck(CheckRequest{RunID: run.ID, Trigger: TriggerManual, Actor: "dev:test"})
	if err != nil {
		t.Fatal(err)
	}
	check = waitCheckLong(t, checkJob)
	if check.OK {
		t.Fatalf("a check of a damaged run passed: %+v", check)
	}
	want := []Mismatch{
		{Subject: "public.accounts", Expected: "13 rows", Actual: "12 rows"},
		{Subject: "migration", Expected: "version 9", Actual: "version 8"},
	}
	if got := check.Databases[0].Mismatches; !slices.Equal(got, want) || check.Databases[0].Error != "" {
		t.Errorf("alpha = %+v, want mismatches %+v", check.Databases[0], want)
	}
	if got := check.Databases[1]; got.OK || !strings.Contains(got.Error, "the manifest says") {
		t.Errorf("beta = %+v", got)
	}
	it.requireNoDatabase(admin, ScratchName(alpha), ScratchName(beta), ScratchName(it.name("left")))
	var foreignLeft bool
	if err := admin.QueryRow(it.ctx, "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", foreign).Scan(&foreignLeft); err != nil || !foreignLeft {
		t.Errorf("a scratch-named database the role does not own was dropped: %v", err)
	}
	if last, err := runner.LastRestoreCheck(); err != nil || last == nil || last.ID != check.ID || last.Actor != "dev:test" {
		t.Errorf("LastRestoreCheck = %+v, %v", last, err)
	}

	// A registry database the role cannot reach says what to do.
	writeRegistry(gamma)
	job, err = runner.Start(Request{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if failed := waitLong(t, job); !strings.Contains(failed.Databases[0].Error, backupRole+" cannot connect to "+gamma+": run make db-provision-backup") {
		t.Errorf("gamma = %+v", failed.Databases[0])
	}
}

func (it *integration) requireNoDatabase(admin *pgx.Conn, names ...string) {
	it.t.Helper()
	for _, name := range names {
		var exists bool
		if err := admin.QueryRow(it.ctx, "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil || exists {
			it.t.Errorf("database %s is left behind (%v)", name, err)
		}
	}
}

func waitCheckLong(t *testing.T, job *CheckJob) RestoreCheck {
	t.Helper()
	select {
	case <-job.Done():
		return job.Result()
	case <-time.After(5 * time.Minute):
		t.Fatal("the restore check did not finish")
		return RestoreCheck{}
	}
}
