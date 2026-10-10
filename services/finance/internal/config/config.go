// Package config defines configuration owned by one generated service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/pkg/database"
)

// Config holds process-level configuration for a generated service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"finance"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// ActivityURL is the Activity service; empty records nothing.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// TimeZone is the owner's timezone (LIFE_OS_TIMEZONE, an IANA name such
	// as Europe/Berlin): a manual transaction without a date is dated today
	// there. Unset or unknown means UTC.
	TimeZone string `env:"LIFE_OS_TIMEZONE" default:"UTC"`
	// DefaultCurrency is the currency of the built-in Cash account when
	// Finance creates it, and of budgets.
	DefaultCurrency string `env:"FINANCE_DEFAULT_CURRENCY" default:"EUR"`
	// NotificationsURL is the Notifications service the budget alerts send
	// to; empty sends nothing.
	NotificationsURL string `env:"NOTIFICATIONS_SERVICE_URL"`
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

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
	if !currencyCode.MatchString(c.DefaultCurrency) {
		validationErrors = append(validationErrors, fmt.Errorf("FINANCE_DEFAULT_CURRENCY must be a three-letter code such as EUR"))
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

// OwnerLocation returns the owner's timezone. Unset means UTC; an unknown
// name also falls back to UTC, as in Calendar, and is returned as an error to
// log, so a typo does not stop the service.
func (c Config) OwnerLocation() (*time.Location, error) {
	name := strings.TrimSpace(c.TimeZone)
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return time.UTC, fmt.Errorf("LIFE_OS_TIMEZONE %q is not an IANA timezone name such as Europe/Berlin; using UTC", name)
	}
	return location, nil
}
