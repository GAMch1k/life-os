// Package config defines configuration owned by the Notifications service.
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

// Config holds process-level configuration for the Notifications service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"notifications"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// ActivityURL enables activity events (notification created, delivered,
	// delivery failed); empty disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// Channels names the delivery channels, comma-separated: "ntfy" (the
	// owner's phone) and "log" (the service log, for development). Empty
	// (the default) means none: notifications are stored and listed only.
	// Unknown names stop the service at startup.
	Channels string `env:"NOTIFICATIONS_CHANNELS"`
	// The ntfy channel (docs/operations/ntfy.md): the ntfy server on the
	// private network, the one private topic, and the publisher's access
	// token (write-only on the topic; a secret, never logged). Required when
	// NOTIFICATIONS_CHANNELS names ntfy.
	NtfyURL   string `env:"NOTIFICATIONS_NTFY_URL"`
	NtfyTopic string `env:"NOTIFICATIONS_NTFY_TOPIC" default:"lifeos"`
	NtfyToken string `env:"NOTIFICATIONS_NTFY_TOKEN"`
	// LinkBaseURL is Life OS's public URL, such as
	// https://lifeos.gamch1k.com: tapping a notification on the phone opens
	// its link there.
	LinkBaseURL string `env:"NOTIFICATIONS_LINK_BASE_URL"`
	// WorkerPollMillis is how often the delivery worker looks for due
	// deliveries. A new notification also wakes it at once.
	WorkerPollMillis int `env:"NOTIFICATIONS_WORKER_POLL_MILLIS" default:"1000"`
	// LeaseSeconds is how long a claimed delivery stays with one worker
	// before another may claim it again (after a crash).
	LeaseSeconds int `env:"NOTIFICATIONS_LEASE_SECONDS" default:"60"`
	// SendTimeoutSeconds bounds one attempt; it must be shorter than the
	// lease.
	SendTimeoutSeconds int `env:"NOTIFICATIONS_SEND_TIMEOUT_SECONDS" default:"15"`
	// RetryBaseSeconds and RetryMaxSeconds shape the exponential backoff
	// between attempts: base, 2×base, 4×base, … capped at max.
	RetryBaseSeconds int `env:"NOTIFICATIONS_RETRY_BASE_SECONDS" default:"10"`
	RetryMaxSeconds  int `env:"NOTIFICATIONS_RETRY_MAX_SECONDS" default:"900"`
	// MaxAgeSeconds is how long after a notification was created its
	// deliveries are still tried; after that they are failed, so a stale
	// notification never arrives late.
	MaxAgeSeconds int `env:"NOTIFICATIONS_DELIVERY_MAX_AGE_SECONDS" default:"86400"`
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
	if c.WorkerPollMillis < 100 || c.WorkerPollMillis > 60000 {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_WORKER_POLL_MILLIS must be between 100 and 60000"))
	}
	if c.LeaseSeconds < 5 || c.LeaseSeconds > 3600 {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_LEASE_SECONDS must be between 5 and 3600"))
	}
	if c.SendTimeoutSeconds < 1 || c.SendTimeoutSeconds >= c.LeaseSeconds {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_SEND_TIMEOUT_SECONDS must be at least 1 and shorter than NOTIFICATIONS_LEASE_SECONDS"))
	}
	if c.RetryBaseSeconds < 1 || c.RetryBaseSeconds > c.RetryMaxSeconds {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_RETRY_BASE_SECONDS must be at least 1 and at most NOTIFICATIONS_RETRY_MAX_SECONDS"))
	}
	if c.RetryMaxSeconds > 86400 {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_RETRY_MAX_SECONDS must be at most 86400"))
	}
	if c.MaxAgeSeconds < 60 || c.MaxAgeSeconds > 30*86400 {
		validationErrors = append(validationErrors, fmt.Errorf("NOTIFICATIONS_DELIVERY_MAX_AGE_SECONDS must be between 60 and 2592000"))
	}
	if _, err := ParseChannels(c.Channels); err != nil {
		validationErrors = append(validationErrors, err)
	}
	return errors.Join(validationErrors...)
}

// ParseChannels splits NOTIFICATIONS_CHANNELS into channel names: lowercase,
// trimmed, unique, in the configured order. Whether a name is a known channel
// is decided when the channels are built.
func ParseChannels(value string) ([]string, error) {
	names := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("NOTIFICATIONS_CHANNELS names %q twice", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// ChannelNames returns the validated channel names.
func (c Config) ChannelNames() []string {
	names, _ := ParseChannels(c.Channels)
	return names
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

// Seconds converts a validated whole-second setting to a duration.
func Seconds(value int) time.Duration {
	return time.Duration(value) * time.Second
}
