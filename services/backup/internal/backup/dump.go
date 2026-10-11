package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultDatabaseTimeout bounds the backup of one database, so a stuck
// connection cannot hold the run (and its lock) forever.
const DefaultDatabaseTimeout = 30 * time.Minute

// Connection is how the service reaches PostgreSQL: the server and login of
// BACKUP_DATABASE_URL, used for every database in the registry.
type Connection struct {
	Host           string
	Port           string
	User           string
	Password       string
	SSLMode        string
	ConnectTimeout string
}

// ParseConnection reads a postgres:// URL that names the server and login
// but no database, such as postgres://lifeos_backup@postgres:5432/?sslmode=disable.
// password, when not empty, replaces a password in the URL.
func ParseConnection(rawURL, password string) (Connection, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Connection{}, errors.New("not a URL")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return Connection{}, errors.New("must be a postgres:// URL")
	}
	if parsed.Hostname() == "" || parsed.User == nil || parsed.User.Username() == "" {
		return Connection{}, errors.New("must name a host and a user")
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return Connection{}, errors.New("must not name a database: the databases come from the registry")
	}
	connection := Connection{
		Host:           parsed.Hostname(),
		Port:           parsed.Port(),
		User:           parsed.User.Username(),
		SSLMode:        parsed.Query().Get("sslmode"),
		ConnectTimeout: parsed.Query().Get("connect_timeout"),
	}
	if connection.Port == "" {
		connection.Port = "5432"
	}
	if connection.ConnectTimeout == "" {
		connection.ConnectTimeout = "10"
	}
	if secret, ok := parsed.User.Password(); ok {
		connection.Password = secret
	}
	if password != "" {
		connection.Password = password
	}
	return connection, nil
}

// config is the pgx configuration for database.
func (c Connection) config(database string) (*pgx.ConnConfig, error) {
	u := url.URL{Scheme: "postgres", User: url.User(c.User), Host: c.Host + ":" + c.Port, Path: "/" + database}
	query := url.Values{"connect_timeout": {c.ConnectTimeout}}
	if c.SSLMode != "" {
		query.Set("sslmode", c.SSLMode)
	}
	u.RawQuery = query.Encode()
	config, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	config.Password = c.Password
	config.RuntimeParams["application_name"] = "backup"
	return config, nil
}

// env is the environment of pg_dump and pg_restore: the connection through
// libpq's variables, so the password never appears in a command line, and
// nothing else of the service's environment.
func (c Connection) env(database string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"PGHOST=" + c.Host,
		"PGPORT=" + c.Port,
		"PGUSER=" + c.User,
		"PGPASSWORD=" + c.Password,
		"PGCONNECT_TIMEOUT=" + c.ConnectTimeout,
		"PGAPPNAME=backup",
	}
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	if c.SSLMode != "" {
		env = append(env, "PGSSLMODE="+c.SSLMode)
	}
	if database != "" {
		env = append(env, "PGDATABASE="+database)
	}
	return env
}

// Dumper backs up one database at a time.
type Dumper struct {
	Connection Connection
	// PgDump and PgRestore are the client programs, from the same
	// PostgreSQL major version as the server.
	PgDump    string
	PgRestore string
	// Timeout bounds one database (DefaultDatabaseTimeout when zero).
	Timeout time.Duration
	Now     func() time.Time

	// afterSnapshot, for tests, runs once the snapshot is exported and
	// counted, before pg_dump starts.
	afterSnapshot func(ctx context.Context, database string)
}

// Version is the pg_dump version line, such as "pg_dump (PostgreSQL) 18.6".
func (d *Dumper) Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, d.PgDump, "--version")
	cmd.Env = d.Connection.env("")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", d.PgDump, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Dump backs up database into dir as <database>.dump, consistent with the
// row counts and migration state it records:
//
//  1. A REPEATABLE READ, READ ONLY transaction exports its snapshot
//     (pg_export_snapshot).
//  2. In that snapshot it counts the rows of every user table and reads
//     schema_migrations.
//  3. While the transaction stays open, pg_dump --snapshot dumps the same
//     snapshot in the custom format, with owners and privileges. Its output
//     goes to the file through a SHA-256 hash and is flushed to disk.
//  4. pg_restore --list must read the archive back.
//
// Any failure is returned in the result; the file is then removed.
func (d *Dumper) Dump(ctx context.Context, database Database, dir string) (result DatabaseResult, serverVersion string) {
	result = DatabaseResult{Service: database.Service, Name: database.Name, StartedAt: d.now(), Tables: []TableRows{}}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultDatabaseTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	serverVersion, err = d.dump(ctx, database, dir, &result)
	result.FinishedAt = d.now()
	if err != nil {
		result.Result = ResultFailed
		result.Error = err.Error()
		result.File, result.Bytes, result.SHA256 = "", 0, ""
		return result, serverVersion
	}
	result.Result = ResultSucceeded
	return result, serverVersion
}

func (d *Dumper) dump(ctx context.Context, database Database, dir string, result *DatabaseResult) (string, error) {
	config, err := d.Connection.config(database.Name)
	if err != nil {
		return "", fmt.Errorf("connection settings: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", fmt.Errorf("begin the snapshot transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var snapshot, serverVersion string
	if err := tx.QueryRow(ctx, "SELECT pg_export_snapshot(), current_setting('server_version')").Scan(&snapshot, &serverVersion); err != nil {
		return "", fmt.Errorf("export the snapshot: %w", err)
	}
	if result.Tables, err = countRows(ctx, tx); err != nil {
		return serverVersion, err
	}
	if result.Migration, err = readMigration(ctx, tx); err != nil {
		return serverVersion, err
	}
	if d.afterSnapshot != nil {
		d.afterSnapshot(ctx, database.Name)
	}

	file := database.Name + ".dump"
	path := filepath.Join(dir, file)
	partial := path + ".partial"
	bytesWritten, sum, err := d.runDump(ctx, database.Name, snapshot, partial)
	if err == nil {
		err = d.check(ctx, partial)
	}
	if err == nil {
		err = os.Rename(partial, path)
	}
	if err != nil {
		_ = os.Remove(partial)
		return serverVersion, err
	}
	result.File, result.Bytes, result.SHA256 = file, bytesWritten, sum
	return serverVersion, nil
}

// countRows counts the rows of every user table (plain tables and
// partitions, each without its inheritance children) in the transaction's
// snapshot.
func countRows(ctx context.Context, tx pgx.Tx) ([]TableRows, error) {
	rows, err := tx.Query(ctx, `
		SELECT n.nspname, c.relname
		FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
		  AND c.relpersistence <> 't'
		  AND n.nspname <> 'information_schema'
		  AND n.nspname NOT LIKE 'pg\_%'
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TableRows, error) {
		var table TableRows
		err := row.Scan(&table.Schema, &table.Name)
		return table, err
	})
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	for i := range tables {
		name := pgx.Identifier{tables[i].Schema, tables[i].Name}.Sanitize()
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ONLY "+name).Scan(&tables[i].Rows); err != nil {
			return nil, fmt.Errorf("count rows of %s.%s: %w", tables[i].Schema, tables[i].Name, err)
		}
	}
	if tables == nil {
		tables = []TableRows{}
	}
	return tables, nil
}

// readMigration reads golang-migrate's schema_migrations row, preferring the
// public schema. A database without one has no migration state.
func readMigration(ctx context.Context, tx pgx.Tx) (*Migration, error) {
	var schema string
	err := tx.QueryRow(ctx, `
		SELECT n.nspname
		FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE c.relname = 'schema_migrations' AND c.relkind = 'r'
		ORDER BY n.nspname = 'public' DESC, n.nspname
		LIMIT 1`).Scan(&schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find schema_migrations: %w", err)
	}
	var migration Migration
	err = tx.QueryRow(ctx, "SELECT version, dirty FROM "+pgx.Identifier{schema, "schema_migrations"}.Sanitize()+" LIMIT 1").
		Scan(&migration.Version, &migration.Dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return &migration, nil
}

// runDump runs pg_dump on the exported snapshot and streams its output to
// path, hashing it on the way, then flushes the file to disk.
func (d *Dumper) runDump(ctx context.Context, database, snapshot, path string) (int64, string, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()

	cmd := exec.CommandContext(ctx, d.PgDump, "--format=custom", "--snapshot="+snapshot, "--no-password")
	cmd.Env = d.Connection.env(database)
	cmd.WaitDelay = 10 * time.Second
	stderr := &tailBuffer{limit: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, "", err
	}
	if err := cmd.Start(); err != nil {
		return 0, "", fmt.Errorf("start pg_dump: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), stdout)
	waitErr := cmd.Wait()
	switch {
	case waitErr != nil:
		return 0, "", commandError(ctx, "pg_dump", waitErr, stderr)
	case copyErr != nil:
		return 0, "", fmt.Errorf("write the dump: %w", copyErr)
	case written == 0:
		return 0, "", errors.New("pg_dump wrote nothing")
	}
	if err := file.Sync(); err != nil {
		return 0, "", fmt.Errorf("flush the dump: %w", err)
	}
	if err := file.Close(); err != nil {
		return 0, "", fmt.Errorf("close the dump: %w", err)
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

// check reads the archive's table of contents back with pg_restore --list.
func (d *Dumper) check(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, d.PgRestore, "--list", path)
	cmd.Env = d.Connection.env("")
	cmd.WaitDelay = 10 * time.Second
	stderr := &tailBuffer{limit: 4096}
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return commandError(ctx, "pg_restore --list", err, stderr)
	}
	return nil
}

func (d *Dumper) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func commandError(ctx context.Context, name string, err error, stderr *tailBuffer) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s stopped: %w", name, context.Cause(ctx))
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		return fmt.Errorf("%s failed: %s", name, message)
	}
	return fmt.Errorf("%s failed: %w", name, err)
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if extra := t.buf.Len() - t.limit; extra > 0 {
		t.buf.Next(extra)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return t.buf.String() }
