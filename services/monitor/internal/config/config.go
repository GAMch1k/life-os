// Package config defines configuration owned by the monitor service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
)

// Config holds process-level configuration for the monitor.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"monitor"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	// StatusURL is the Dashboard's runtime status endpoint, which applies the
	// Status page's rules.
	StatusURL string `env:"MONITOR_STATUS_URL" default:"http://localhost:3000/api/status"`
	// NotificationsURL is the Notifications service's base URL. Empty
	// disables sending: status changes are only logged.
	NotificationsURL string `env:"NOTIFICATIONS_SERVICE_URL"`
	// IntervalSeconds is the time between two evaluations of the status.
	IntervalSeconds int `env:"MONITOR_INTERVAL_SECONDS" default:"60"`
	// Confirmations is how many evaluations in a row must agree on a check's
	// new state before it is notified.
	Confirmations int `env:"MONITOR_CONFIRMATIONS" default:"2"`
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
	if !httpURL(c.StatusURL) {
		validationErrors = append(validationErrors, fmt.Errorf("MONITOR_STATUS_URL must be an absolute HTTP or HTTPS URL"))
	}
	if c.NotificationsURL != "" && !httpURL(c.NotificationsURL) {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_SERVICE_URL must be empty or an absolute HTTP or HTTPS URL"))
	}
	if c.IntervalSeconds < 5 || c.IntervalSeconds > 3600 {
		validationErrors = append(validationErrors, fmt.Errorf("MONITOR_INTERVAL_SECONDS must be between 5 and 3600"))
	}
	if c.Confirmations < 1 || c.Confirmations > 10 {
		validationErrors = append(validationErrors, fmt.Errorf("MONITOR_CONFIRMATIONS must be between 1 and 10"))
	}
	return errors.Join(validationErrors...)
}

func httpURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// Interval is the validated time between two evaluations.
func (c Config) Interval() time.Duration {
	return time.Duration(c.IntervalSeconds) * time.Second
}
