// Package config defines configuration owned by one generated service.
package config

import (
	"errors"
	"fmt"
	"log/slog"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/pkg/database"
)

// Config holds process-level configuration for a generated service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"inbox"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// ActivityURL enables activity events; empty disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// NotificationsURL enables notifications to the owner (Human Review,
	// failed captures); empty disables them.
	NotificationsURL string `env:"NOTIFICATIONS_SERVICE_URL"`
	// MediaDir holds capture images (the inbox-media volume in Compose).
	MediaDir string `env:"INBOX_MEDIA_DIR" default:"/var/lib/lifeos/inbox-media"`
	// MaxImageBytes limits one capture image (10 MiB by default).
	MaxImageBytes int64 `env:"INBOX_MEDIA_MAX_BYTES" default:"10485760"`
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
	if c.MediaDir == "" {
		validationErrors = append(validationErrors, fmt.Errorf("INBOX_MEDIA_DIR must not be empty"))
	}
	if c.MaxImageBytes < 1 || c.MaxImageBytes > maxImageLimit {
		validationErrors = append(validationErrors, fmt.Errorf("INBOX_MEDIA_MAX_BYTES must be between 1 and %d", int64(maxImageLimit)))
	}
	return errors.Join(validationErrors...)
}

// maxImageLimit caps INBOX_MEDIA_MAX_BYTES at 20 MiB, so a full upload of
// five images stays within what one request holds in memory.
const maxImageLimit = 20 << 20

// multipartAllowance is the room an upload needs above its images for
// multipart framing.
const multipartAllowance = 1 << 20

// RequestBodyLimit is the HTTP request body limit of the Inbox API: room for
// an upload of five images at the size limit. Without it Fiber's 4 MiB
// default would refuse ordinary photos; within it the upload handler answers
// 413 itself for an image over MaxImageBytes.
func (c Config) RequestBodyLimit() int {
	return int(c.MaxImageBytes)*maxImagesPerUpload + multipartAllowance
}

// maxImagesPerUpload matches media.MaxPerCapture.
const maxImagesPerUpload = 5

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
