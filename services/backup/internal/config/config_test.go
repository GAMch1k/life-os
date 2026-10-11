package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"BACKUP_DATABASE_URL": "postgres://lifeos_backup@postgres:5432/?sslmode=disable",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "backup" || cfg.Port != 8080 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.DatabasesFile != "/etc/lifeos/postgres/service-databases.conf" || cfg.Dir != "/backups" {
		t.Fatalf("unexpected paths: %+v", cfg)
	}
	if schedule := cfg.Schedule(); schedule.Time() != "03:15" || schedule.Location.String() != "UTC" {
		t.Fatalf("unexpected schedule: %+v", schedule)
	}
	if retention := cfg.Retention(); retention.Daily != 7 || retention.Weekly != 4 || retention.Monthly != 12 || retention.FailedDays != 3 {
		t.Fatalf("unexpected retention: %+v", retention)
	}
	if cfg.PgDump != "pg_dump" || cfg.PgRestore != "pg_restore" {
		t.Fatalf("unexpected programs: %+v", cfg)
	}
	if day, enabled := cfg.CheckDay(); !enabled || day != time.Sunday {
		t.Fatalf("unexpected restore check day: %s, %v", day, enabled)
	}
	connection := cfg.Connection()
	if connection.Host != "postgres" || connection.Port != "5432" || connection.User != "lifeos_backup" || connection.SSLMode != "disable" || connection.Password != "" {
		t.Fatalf("unexpected connection: %+v", connection)
	}
}

func TestLoadFromReadsTheBackupSettings(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"BACKUP_DATABASE_URL":      "postgres://lifeos_backup:in-url@db:6543",
		"BACKUP_DATABASE_PASSWORD": "from-env",
		"BACKUP_DATABASES_FILE":    "/etc/registry.conf",
		"BACKUP_DIR":               "/srv/backups",
		"BACKUP_TIME":              "02:30",
		"LIFE_OS_TIMEZONE":         "Europe/Berlin",
		"BACKUP_KEEP_DAILY":        "10",
		"BACKUP_KEEP_WEEKLY":       "0",
		"BACKUP_KEEP_MONTHLY":      "24",
		"BACKUP_KEEP_FAILED_DAYS":  "5",
		"BACKUP_PG_DUMP":           "/usr/local/bin/pg_dump",
		"BACKUP_PG_RESTORE":        "/usr/local/bin/pg_restore",
		"BACKUP_RESTORE_CHECK_DAY": "off",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabasesFile != "/etc/registry.conf" || cfg.Dir != "/srv/backups" || cfg.PgDump != "/usr/local/bin/pg_dump" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if schedule := cfg.Schedule(); schedule.Minute != 150 || schedule.Location.String() != "Europe/Berlin" {
		t.Fatalf("unexpected schedule: %+v", schedule)
	}
	if retention := cfg.Retention(); retention.Daily != 10 || retention.Weekly != 0 || retention.Monthly != 24 || retention.FailedDays != 5 {
		t.Fatalf("unexpected retention: %+v", retention)
	}
	if _, enabled := cfg.CheckDay(); enabled {
		t.Fatal("restore checks are on with BACKUP_RESTORE_CHECK_DAY=off")
	}
	// BACKUP_DATABASE_PASSWORD wins over a password in the URL.
	if connection := cfg.Connection(); connection.Password != "from-env" || connection.Port != "6543" || connection.SSLMode != "" {
		t.Fatalf("unexpected connection: %+v", connection)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"PORT":                     "70000",
		"LOG_LEVEL":                "loud",
		"BACKUP_DATABASE_URL":      "postgres://lifeos_backup@postgres:5432/finance_db",
		"BACKUP_DATABASES_FILE":    "service-databases.conf",
		"BACKUP_DIR":               "backups",
		"BACKUP_TIME":              "3:15",
		"LIFE_OS_TIMEZONE":         "Mars/Olympus",
		"BACKUP_KEEP_DAILY":        "0",
		"BACKUP_KEEP_WEEKLY":       "-1",
		"BACKUP_KEEP_MONTHLY":      "5000",
		"BACKUP_KEEP_FAILED_DAYS":  "0",
		"BACKUP_PG_DUMP":           " ",
		"BACKUP_RESTORE_CHECK_DAY": "weekly",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"PORT", "LOG_LEVEL", "BACKUP_DATABASE_URL", "BACKUP_DATABASES_FILE", "BACKUP_DIR", "BACKUP_TIME", "LIFE_OS_TIMEZONE", "BACKUP_KEEP_DAILY", "BACKUP_KEEP_WEEKLY", "BACKUP_KEEP_MONTHLY", "BACKUP_KEEP_FAILED_DAYS", "BACKUP_PG_DUMP", "BACKUP_RESTORE_CHECK_DAY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoadFromRequiresTheDatabaseURL(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "mysql://u@h/", "postgres://postgres:5432/", "postgres://u@/"} {
		lookup := map[string]string{}
		if value != "" {
			lookup["BACKUP_DATABASE_URL"] = value
		}
		if _, err := LoadFrom(mapLookup(lookup)); err == nil || !strings.Contains(err.Error(), "BACKUP_DATABASE_URL") {
			t.Errorf("BACKUP_DATABASE_URL=%q: error = %v", value, err)
		}
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
