// Package config defines configuration owned by the backup service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/services/backup/internal/backup"
)

// Config holds process-level configuration for the backup service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"backup"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	// DatabaseURL names the PostgreSQL server and the backup login, not a
	// database: postgres://lifeos_backup@postgres:5432/?sslmode=disable.
	DatabaseURL string `env:"BACKUP_DATABASE_URL" required:"true"`
	// DatabasePassword is the backup login's password (the URL may carry
	// it instead). Never logged.
	DatabasePassword string `env:"BACKUP_DATABASE_PASSWORD"`
	// DatabasesFile is the database registry: every database in it is
	// backed up.
	DatabasesFile string `env:"BACKUP_DATABASES_FILE" default:"/etc/lifeos/postgres/service-databases.conf"`
	// Dir is the backup directory; the service writes under its auto/.
	Dir string `env:"BACKUP_DIR" default:"/backups"`
	// Time is the nightly run, HH:MM in TimeZone.
	Time     string `env:"BACKUP_TIME" default:"03:15"`
	TimeZone string `env:"LIFE_OS_TIMEZONE" default:"UTC"`
	// The rotation: successful full runs kept per day, ISO week, and month,
	// and the days failed runs are kept.
	KeepDaily      int `env:"BACKUP_KEEP_DAILY" default:"7"`
	KeepWeekly     int `env:"BACKUP_KEEP_WEEKLY" default:"4"`
	KeepMonthly    int `env:"BACKUP_KEEP_MONTHLY" default:"12"`
	KeepFailedDays int `env:"BACKUP_KEEP_FAILED_DAYS" default:"3"`
	// PgDump and PgRestore are the PostgreSQL client programs, of the
	// server's major version.
	PgDump    string `env:"BACKUP_PG_DUMP" default:"pg_dump"`
	PgRestore string `env:"BACKUP_PG_RESTORE" default:"pg_restore"`
}

// Load reads Config from the process environment.
func Load() (Config, error) {
	return LoadFrom(nil)
}

// LoadFrom reads Config through lookup. Passing nil reads the process
// environment and keeps tests independent from global process state.
func LoadFrom(lookup sharedconfig.LookupFunc) (Config, error) {
	var cfg Config
	var err error
	if lookup == nil {
		err = sharedconfig.Load(&cfg)
	} else {
		err = sharedconfig.LoadFrom(&cfg, lookup)
	}
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate implements the shared configuration validation hook.
func (c Config) Validate() error {
	var validationErrors []error
	if c.ServiceName == "" {
		validationErrors = append(validationErrors, fmt.Errorf("SERVICE_NAME must not be empty"))
	}
	if c.Port < 1 || c.Port > 65535 {
		validationErrors = append(validationErrors, fmt.Errorf("PORT must be between 1 and 65535"))
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("LOG_LEVEL must be a slog level: %w", err))
	}
	if _, err := backup.ParseConnection(c.DatabaseURL, c.DatabasePassword); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_DATABASE_URL %w", err))
	}
	if !filepath.IsAbs(c.DatabasesFile) {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_DATABASES_FILE must be an absolute path"))
	}
	if !filepath.IsAbs(c.Dir) {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_DIR must be an absolute path"))
	}
	if _, err := backup.ParseTime(c.Time); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_TIME: %w", err))
	}
	if _, err := time.LoadLocation(strings.TrimSpace(c.TimeZone)); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("LIFE_OS_TIMEZONE must be an IANA timezone such as Europe/Berlin"))
	}
	if c.KeepDaily < 1 || c.KeepDaily > 366 {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_KEEP_DAILY must be between 1 and 366"))
	}
	if c.KeepWeekly < 0 || c.KeepWeekly > 520 {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_KEEP_WEEKLY must be between 0 and 520"))
	}
	if c.KeepMonthly < 0 || c.KeepMonthly > 1200 {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_KEEP_MONTHLY must be between 0 and 1200"))
	}
	if c.KeepFailedDays < 1 || c.KeepFailedDays > 366 {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_KEEP_FAILED_DAYS must be between 1 and 366"))
	}
	if strings.TrimSpace(c.PgDump) == "" || strings.TrimSpace(c.PgRestore) == "" {
		validationErrors = append(validationErrors, fmt.Errorf("BACKUP_PG_DUMP and BACKUP_PG_RESTORE must not be empty"))
	}
	return errors.Join(validationErrors...)
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// Location is the owner's validated timezone.
func (c Config) Location() *time.Location {
	location, err := time.LoadLocation(strings.TrimSpace(c.TimeZone))
	if err != nil {
		return time.UTC
	}
	return location
}

// Schedule is the validated nightly schedule.
func (c Config) Schedule() backup.Schedule {
	minute, _ := backup.ParseTime(c.Time)
	return backup.Schedule{Minute: minute, Location: c.Location()}
}

// Retention is the validated rotation policy.
func (c Config) Retention() backup.Retention {
	return backup.Retention{Daily: c.KeepDaily, Weekly: c.KeepWeekly, Monthly: c.KeepMonthly, FailedDays: c.KeepFailedDays}
}

// Connection is the validated PostgreSQL connection.
func (c Config) Connection() backup.Connection {
	connection, _ := backup.ParseConnection(c.DatabaseURL, c.DatabasePassword)
	return connection
}
