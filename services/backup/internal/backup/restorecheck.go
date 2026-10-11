package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ScratchPrefix starts the name of every scratch database a restore check
// restores into: lifeos_restore_check_<database>.
const ScratchPrefix = "lifeos_restore_check_"

// RestoreCheckInterval is how often a restore check is owed: when the last
// one is older than this and a margin, the next nightly run is followed by
// one whatever the day.
const RestoreCheckInterval = 7 * 24 * time.Hour

// RestoreCheck is one check that a run's dumps restore and hold what their
// manifest says.
type RestoreCheck struct {
	// ID is the check's start time in UTC, formatted like a run id.
	ID              string    `json:"id"`
	RunID           string    `json:"runId"`
	Trigger         Trigger   `json:"trigger"`
	Actor           string    `json:"actor,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	// OK is true when every database restored and matched its manifest,
	// and every media file the run stored is in the store.
	OK        bool            `json:"ok"`
	Databases []DatabaseCheck `json:"databases"`
	Media     *MediaCheck     `json:"media,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// DefaultMediaHashBudget is how many media bytes a restore check hashes;
// past it, files are checked for presence and size.
const DefaultMediaHashBudget = 4 << 30

// DatabaseCheck is the check of one database's dump.
type DatabaseCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Tables and Rows are what the restored database holds.
	Tables     int        `json:"tables"`
	Rows       int64      `json:"rows"`
	Mismatches []Mismatch `json:"mismatches"`
	Error      string     `json:"error,omitempty"`
}

// Mismatch is one difference between the restored database and the
// manifest: a table's row count, a table only one of them has, or the
// migration state.
type Mismatch struct {
	Subject  string `json:"subject"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// Restorer restores one database's dump into a scratch database and
// compares it with the manifest.
type Restorer struct {
	Connection Connection
	PgRestore  string
	// Timeout bounds one database (DefaultDatabaseTimeout when zero).
	Timeout time.Duration
}

// ScratchName is the scratch database a check of database restores into.
func ScratchName(database string) string { return ScratchPrefix + database }

// Check verifies the dump's size and SHA-256 against the manifest, restores
// it into a fresh scratch database as the backup role (--no-owner
// --no-acl), counts every table's rows and reads the migration state there,
// and compares them with the manifest. The scratch database is always
// dropped, whatever happens. dir is the run directory.
func (r *Restorer) Check(ctx context.Context, dir string, database DatabaseResult) DatabaseCheck {
	check := DatabaseCheck{Name: database.Name, Mismatches: []Mismatch{}}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultDatabaseTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.check(ctx, dir, database, &check); err != nil {
		check.Error = err.Error()
		return check
	}
	check.OK = len(check.Mismatches) == 0
	return check
}

func (r *Restorer) check(ctx context.Context, dir string, database DatabaseResult, check *DatabaseCheck) error {
	scratch := ScratchName(database.Name)
	if len(scratch) > 63 {
		return fmt.Errorf("the scratch database name %s is longer than PostgreSQL allows", scratch)
	}
	path := filepath.Join(dir, database.File)
	if err := verifyFile(path, database.Bytes, database.SHA256); err != nil {
		return err
	}

	// The service database itself is where the role may connect to create
	// and drop the scratch database.
	admin, err := r.connect(ctx, database.Name)
	if err != nil {
		return err
	}
	defer closeConn(admin)
	if err := dropDatabase(ctx, admin, scratch); err != nil {
		return err
	}
	name := pgx.Identifier{scratch}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
		return fmt.Errorf("create %s: %w", scratch, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dropDatabase(cleanup, admin, scratch)
	}()
	if _, err := admin.Exec(ctx, "REVOKE ALL ON DATABASE "+name+" FROM PUBLIC"); err != nil {
		return fmt.Errorf("close %s to other roles: %w", scratch, err)
	}

	cmd := exec.CommandContext(ctx, r.PgRestore, "--no-owner", "--no-acl", "--exit-on-error", "--no-password", "--dbname="+scratch, path)
	cmd.Env = r.Connection.env(scratch)
	cmd.WaitDelay = 10 * time.Second
	stderr := &tailBuffer{limit: 4096}
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return commandError(ctx, "pg_restore", err, stderr)
	}

	restored, err := r.connect(ctx, scratch)
	if err != nil {
		return err
	}
	defer closeConn(restored)
	tx, err := restored.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("read %s: %w", scratch, err)
	}
	tables, err := countRows(ctx, tx)
	if err != nil {
		return err
	}
	migration, err := readMigration(ctx, tx)
	if err != nil {
		return err
	}
	check.Tables = len(tables)
	for _, table := range tables {
		check.Rows += table.Rows
	}
	check.Mismatches = compare(database, tables, migration)
	return nil
}

// compare lists how the restored tables and migration state differ from
// the manifest's.
func compare(database DatabaseResult, tables []TableRows, migration *Migration) []Mismatch {
	mismatches := []Mismatch{}
	restored := map[string]int64{}
	for _, table := range tables {
		restored[table.Schema+"."+table.Name] = table.Rows
	}
	expected := map[string]bool{}
	for _, table := range database.Tables {
		key := table.Schema + "." + table.Name
		expected[key] = true
		rows, ok := restored[key]
		switch {
		case !ok:
			mismatches = append(mismatches, Mismatch{Subject: key, Expected: rowsLabel(table.Rows), Actual: "missing"})
		case rows != table.Rows:
			mismatches = append(mismatches, Mismatch{Subject: key, Expected: rowsLabel(table.Rows), Actual: rowsLabel(rows)})
		}
	}
	for _, table := range tables {
		if key := table.Schema + "." + table.Name; !expected[key] {
			mismatches = append(mismatches, Mismatch{Subject: key, Expected: "absent", Actual: rowsLabel(table.Rows)})
		}
	}
	if want, got := migrationLabel(database.Migration), migrationLabel(migration); want != got {
		mismatches = append(mismatches, Mismatch{Subject: "migration", Expected: want, Actual: got})
	}
	return mismatches
}

func rowsLabel(rows int64) string {
	if rows == 1 {
		return "1 row"
	}
	return strconv.FormatInt(rows, 10) + " rows"
}

func migrationLabel(migration *Migration) string {
	if migration == nil {
		return "none"
	}
	label := "version " + strconv.FormatInt(migration.Version, 10)
	if migration.Dirty {
		label += " (dirty)"
	}
	return label
}

// verifyFile checks a dump's size and SHA-256 against its manifest.
func verifyFile(path string, size int64, sum string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open the dump: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	read, err := io.Copy(hash, file)
	if err != nil {
		return fmt.Errorf("read the dump: %w", err)
	}
	if read != size {
		return fmt.Errorf("the dump has %d bytes, the manifest says %d", read, size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, sum) {
		return fmt.Errorf("the dump's SHA-256 is %s, the manifest says %s", got, sum)
	}
	return nil
}

// DropLeftovers drops the scratch databases the backup role owns, the
// remains of checks a crash interrupted. It connects through the first of
// databases that accepts it and returns the names it dropped.
func (r *Restorer) DropLeftovers(ctx context.Context, databases []string) ([]string, error) {
	var conn *pgx.Conn
	var errs []error
	for _, database := range databases {
		c, err := r.connect(ctx, database)
		if err == nil {
			conn = c
			break
		}
		errs = append(errs, err)
	}
	if conn == nil {
		return nil, errors.Join(append([]error{errors.New("no database to connect through")}, errs...)...)
	}
	defer closeConn(conn)
	rows, err := conn.Query(ctx, `
		SELECT datname FROM pg_catalog.pg_database
		WHERE datname LIKE 'lifeos\_restore\_check\_%' AND pg_catalog.pg_get_userbyid(datdba) = current_user
		ORDER BY datname`)
	if err != nil {
		return nil, fmt.Errorf("list scratch databases: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list scratch databases: %w", err)
	}
	var dropped []string
	for _, name := range names {
		if err := dropDatabase(ctx, conn, name); err != nil {
			return dropped, err
		}
		dropped = append(dropped, name)
	}
	return dropped, nil
}

func (r *Restorer) connect(ctx context.Context, database string) (*pgx.Conn, error) {
	config, err := r.Connection.config(database)
	if err != nil {
		return nil, fmt.Errorf("connection settings: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, connectError(r.Connection.User, database, err)
	}
	return conn, nil
}

func dropDatabase(ctx context.Context, conn *pgx.Conn, name string) error {
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return nil
}

func closeConn(conn *pgx.Conn) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.Close(cleanup)
}
