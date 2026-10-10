// Package config defines configuration owned by the Google Calendar
// integration service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
)

// Config holds process-level configuration for the service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"google-calendar"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// CalendarURL is the Calendar service, reached only through its
	// integration and events API.
	CalendarURL string `env:"CALENDAR_SERVICE_URL"`
	// ActivityURL records activity events; empty records none.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// TimeZone is the owner's timezone (IANA): an all-day series' end is a
	// date in it, and the Life OS calendar is created in it.
	TimeZone string `env:"LIFE_OS_TIMEZONE" default:"UTC"`
	// Google Calendar connection: the OAuth Web client (from the owner's
	// Google Cloud project), the base64 32-byte key that encrypts refresh
	// tokens at rest (the same key Calendar used before P11-07, so the
	// handed-over token opens), and the Dashboard route Google returns to.
	// Google is not configured until the first three are set. Secrets:
	// never log them.
	GoogleClientID     string `env:"LIFE_OS_GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"LIFE_OS_GOOGLE_CLIENT_SECRET"`
	GoogleTokenKey     string `env:"LIFE_OS_GOOGLE_TOKEN_KEY"`
	GoogleRedirectURI  string `env:"LIFE_OS_GOOGLE_REDIRECT_URI" default:"https://lifeos.gamch1k.com/calendar/google/callback"`
	// SyncSeconds is how often each Google calendar chosen to sync is
	// synced (changes since the last sync, with Google's sync token).
	SyncSeconds int `env:"GOOGLE_CALENDAR_SYNC_SECONDS" default:"120"`
	// FeedSeconds is how often Calendar's change feed is read, so Life OS
	// events and edits reach Google within seconds.
	FeedSeconds int `env:"GOOGLE_CALENDAR_FEED_SECONDS" default:"15"`
	// GoogleAuthURL, GoogleTokenURL, GoogleRevokeURL, and GoogleCalendarAPI
	// are Google's endpoints; tests and the verification stack point them
	// elsewhere.
	GoogleAuthURL     string `env:"GOOGLE_AUTH_URL" default:"https://accounts.google.com/o/oauth2/v2/auth"`
	GoogleTokenURL    string `env:"GOOGLE_TOKEN_URL" default:"https://oauth2.googleapis.com/token"`
	GoogleRevokeURL   string `env:"GOOGLE_REVOKE_URL" default:"https://oauth2.googleapis.com/revoke"`
	GoogleCalendarAPI string `env:"GOOGLE_CALENDAR_API_URL" default:"https://www.googleapis.com/calendar/v3"`
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
	if _, err := time.LoadLocation(c.TimeZone); err != nil || c.TimeZone == "" || c.TimeZone == "Local" || strings.TrimSpace(c.TimeZone) != c.TimeZone {
		validationErrors = append(validationErrors, fmt.Errorf("LIFE_OS_TIMEZONE must be an IANA timezone such as Europe/Berlin"))
	}
	if c.SyncSeconds < 30 || c.SyncSeconds > 86400 {
		validationErrors = append(validationErrors, fmt.Errorf("GOOGLE_CALENDAR_SYNC_SECONDS must be between 30 and 86400"))
	}
	if c.FeedSeconds < 1 || c.FeedSeconds > 3600 {
		validationErrors = append(validationErrors, fmt.Errorf("GOOGLE_CALENDAR_FEED_SECONDS must be between 1 and 3600"))
	}
	return errors.Join(validationErrors...)
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// Location is the owner's timezone (validated).
func (c Config) Location() *time.Location {
	location, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return time.UTC
	}
	return location
}

// DatabaseConfig adapts service configuration to the shared pool helper.
func (c Config) DatabaseConfig() database.Config {
	return database.Config{
		URL:             c.DatabaseURL,
		ApplicationName: c.ServiceName,
	}
}

// Google returns the Google Calendar configuration. A partial or invalid one
// is returned as an error to log and leaves Google unconfigured, so a typo
// does not stop the service.
func (c Config) Google() (google.Config, error) {
	return google.NewConfig(c.GoogleClientID, c.GoogleClientSecret, c.GoogleTokenKey, c.GoogleRedirectURI)
}

// Endpoints are Google's endpoints.
func (c Config) Endpoints() google.Endpoints {
	return google.Endpoints{
		AuthURL: strings.TrimRight(c.GoogleAuthURL, "/"), TokenURL: c.GoogleTokenURL, RevokeURL: c.GoogleRevokeURL,
		CalendarAPI: strings.TrimRight(c.GoogleCalendarAPI, "/"),
	}
}
