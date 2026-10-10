// Package config defines configuration owned by one generated service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/pkg/database"
)

// Config holds process-level configuration for a generated service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"calendar"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// TimeZone is the owner's timezone (LIFE_OS_TIMEZONE, an IANA name such
	// as Europe/Berlin): the default in which all-day dates are compared
	// with instants when a read does not name a timeZone.
	TimeZone string `env:"LIFE_OS_TIMEZONE" default:"UTC"`
	// ActivityURL enables activity events (source refresh outcomes); empty
	// disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// SourceRefreshAt is the daily quiet-hour time (HH:MM, owner-local) at
	// which calendar sources are refreshed.
	SourceRefreshAt string `env:"CALENDAR_SOURCE_REFRESH_AT" default:"04:30"`
	// ICSMaxBytes and ICSTimeoutSeconds bound one feed download.
	ICSMaxBytes       int64 `env:"CALENDAR_ICS_MAX_BYTES" default:"5242880"`
	ICSTimeoutSeconds int   `env:"CALENDAR_ICS_TIMEOUT_SECONDS" default:"30"`
	// ICSAllowPrivateURLs lets feeds use http:// and private or loopback
	// addresses. For local development and verification only: it disables
	// the protection against reaching internal services.
	ICSAllowPrivateURLs bool `env:"CALENDAR_ICS_ALLOW_PRIVATE_URLS" default:"false"`
	// ZAWICSURL is the owner's personal ZAW (MyMüll) waste-collection iCal
	// feed. When set, Calendar keeps one "ZAW" source for it, configured at
	// startup; empty leaves any existing one alone. It names a street:
	// never log it.
	ZAWICSURL string `env:"CALENDAR_ZAW_ICS_URL"`
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
	if refreshAt, err := time.Parse("15:04", c.SourceRefreshAt); err != nil || refreshAt.Format("15:04") != c.SourceRefreshAt {
		validationErrors = append(validationErrors, fmt.Errorf("CALENDAR_SOURCE_REFRESH_AT must be a 24-hour HH:MM time such as 04:30"))
	}
	if c.ICSMaxBytes < 1024 || c.ICSMaxBytes > 100<<20 {
		validationErrors = append(validationErrors, fmt.Errorf("CALENDAR_ICS_MAX_BYTES must be between 1024 and 104857600"))
	}
	if c.ICSTimeoutSeconds < 1 || c.ICSTimeoutSeconds > 300 {
		validationErrors = append(validationErrors, fmt.Errorf("CALENDAR_ICS_TIMEOUT_SECONDS must be between 1 and 300"))
	}
	return errors.Join(validationErrors...)
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// DatabaseConfig adapts service configuration to the shared pool helper.
func (c Config) DatabaseConfig() database.Config {
	return database.Config{
		URL:             c.DatabaseURL,
		ApplicationName: c.ServiceName,
	}
}

// OwnerTimeZone returns the validated LIFE_OS_TIMEZONE name. Unset means UTC.
// An unknown name also falls back to UTC, as in the Dashboard and the
// processor, and is returned as an error to log, so a typo does not stop the
// service.
func (c Config) OwnerTimeZone() (string, error) {
	name := strings.TrimSpace(c.TimeZone)
	if name == "" {
		return "UTC", nil
	}
	if !ValidTimeZone(name) {
		return "UTC", fmt.Errorf("LIFE_OS_TIMEZONE %q is not an IANA timezone name such as Europe/Berlin; using UTC", name)
	}
	return name, nil
}

// ValidTimeZone reports whether name is an IANA timezone such as
// Europe/Berlin. "Local" (the process's own zone) is not one here.
func ValidTimeZone(name string) bool {
	if name == "" || name == "Local" || strings.TrimSpace(name) != name {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
