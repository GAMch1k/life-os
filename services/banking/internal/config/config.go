// Package config defines configuration owned by the Banking service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/services/banking/internal/banking"
)

// Config holds process-level configuration for the Banking service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"banking"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// FinanceURL is the Finance service, which receives every sync.
	FinanceURL string `env:"FINANCE_SERVICE_URL"`
	// NotificationsURL enables consent reminders; ActivityURL activity
	// events. Empty disables them.
	NotificationsURL string `env:"NOTIFICATIONS_SERVICE_URL"`
	ActivityURL      string `env:"ACTIVITY_SERVICE_URL"`
	// TimeZone is the owner's timezone (IANA): the sync times and the daily
	// read limit follow it.
	TimeZone string `env:"LIFE_OS_TIMEZONE" default:"UTC"`
	// The Enable Banking application (docs/operations/banking.md): its id,
	// the path of its private key file, the key that seals provider session
	// ids (32 bytes, base64), and the Dashboard callback registered on the
	// application. All secrets except the redirect URI; never logged.
	EnableBankingAppID   string `env:"LIFE_OS_ENABLE_BANKING_APP_ID"`
	EnableBankingKeyFile string `env:"LIFE_OS_ENABLE_BANKING_KEY_FILE"`
	TokenKey             string `env:"LIFE_OS_BANKING_TOKEN_KEY"`
	RedirectURI          string `env:"LIFE_OS_BANKING_REDIRECT_URI" default:"https://lifeos.gamch1k.com/finance/bank/callback"`
	// EnableBankingAPIURL is Enable Banking's API; tests and the
	// verification stack point it elsewhere.
	EnableBankingAPIURL string `env:"ENABLE_BANKING_API_URL" default:"https://api.enablebanking.com"`
	// SyncTimes are the scheduled syncs, HH:MM owner-local, at most four.
	SyncTimes string `env:"BANKING_SYNC_TIMES" default:"06:00,12:00,18:00,23:00"`
	// MaxUnattended is the most reads without the owner a day (1-4).
	MaxUnattended int `env:"BANKING_MAX_UNATTENDED_PER_DAY" default:"4"`
	// ManualSyncMinutes is the least time between two syncs the owner asks
	// for.
	ManualSyncMinutes int `env:"BANKING_MANUAL_SYNC_MINUTES" default:"10"`
	// InitialDays is how far back the first sync reads; OverlapDays how far
	// before the last booked date a later sync starts.
	InitialDays int `env:"BANKING_INITIAL_DAYS" default:"90"`
	OverlapDays int `env:"BANKING_OVERLAP_DAYS" default:"7"`
	// TickSeconds is how often the scheduler checks the connections.
	TickSeconds int `env:"BANKING_TICK_SECONDS" default:"60"`
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

// Validate implements the shared configuration validation hook. The Enable
// Banking settings are checked when the service starts (a missing or bad
// one leaves banking "not configured" rather than stopping the service).
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
	if _, err := time.LoadLocation(strings.TrimSpace(c.TimeZone)); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("LIFE_OS_TIMEZONE must be an IANA timezone such as Europe/Berlin"))
	}
	if _, err := banking.ParseSyncTimes(c.SyncTimes); err != nil {
		validationErrors = append(validationErrors, err)
	}
	if c.MaxUnattended < 1 || c.MaxUnattended > banking.MaxUnattendedPerDay {
		validationErrors = append(validationErrors, fmt.Errorf("BANKING_MAX_UNATTENDED_PER_DAY must be between 1 and %d", banking.MaxUnattendedPerDay))
	}
	if c.ManualSyncMinutes < 1 || c.ManualSyncMinutes > 1440 {
		validationErrors = append(validationErrors, fmt.Errorf("BANKING_MANUAL_SYNC_MINUTES must be between 1 and 1440"))
	}
	if c.InitialDays < 1 || c.InitialDays > 730 {
		validationErrors = append(validationErrors, fmt.Errorf("BANKING_INITIAL_DAYS must be between 1 and 730"))
	}
	if c.OverlapDays < 0 || c.OverlapDays > 60 {
		validationErrors = append(validationErrors, fmt.Errorf("BANKING_OVERLAP_DAYS must be between 0 and 60"))
	}
	if c.TickSeconds < 5 || c.TickSeconds > 3600 {
		validationErrors = append(validationErrors, fmt.Errorf("BANKING_TICK_SECONDS must be between 5 and 3600"))
	}
	return errors.Join(validationErrors...)
}

// Location is the owner's validated timezone.
func (c Config) Location() *time.Location {
	location, err := time.LoadLocation(strings.TrimSpace(c.TimeZone))
	if err != nil {
		return time.UTC
	}
	return location
}

// Options are the validated sync options.
func (c Config) Options() banking.Options {
	minutes, _ := banking.ParseSyncTimes(c.SyncTimes)
	overlap := c.OverlapDays
	if overlap == 0 {
		overlap = -1 // explicitly no overlap
	}
	return banking.Options{
		Schedule:       banking.Schedule{Minutes: minutes, Location: c.Location()},
		MaxUnattended:  c.MaxUnattended,
		ManualInterval: time.Duration(c.ManualSyncMinutes) * time.Minute,
		InitialDays:    c.InitialDays,
		OverlapDays:    overlap,
	}
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
