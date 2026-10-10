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
	ServiceName               string `env:"SERVICE_NAME" default:"voice"`
	Port                      int    `env:"PORT" default:"8080"`
	LogLevel                  string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL               string `env:"DATABASE_URL" required:"true"`
	StorageDir                string `env:"VOICE_STORAGE_DIR" default:"/var/lib/lifeos/voice"`
	RetentionHours            int    `env:"VOICE_RETENTION_HOURS" default:"168"`
	MaxUploadBytes            int64  `env:"VOICE_MAX_UPLOAD_BYTES" default:"26214400"`
	InboxURL                  string `env:"INBOX_SERVICE_URL" default:"http://inbox:8080"`
	WhisperURL                string `env:"WHISPER_SERVICE_URL" default:"http://whisper:8080"`
	WorkerPollMillis          int    `env:"VOICE_WORKER_POLL_MILLIS" default:"1000"`
	WorkerMaxAttempts         int    `env:"VOICE_WORKER_MAX_ATTEMPTS" default:"3"`
	WorkerLeaseSeconds        int    `env:"VOICE_WORKER_LEASE_SECONDS" default:"120"`
	WorkerRetryBackoffSeconds int    `env:"VOICE_WORKER_RETRY_BACKOFF_SECONDS" default:"60"`
	// ActivityURL enables activity events; empty disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
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
	if c.StorageDir == "" || c.RetentionHours < 1 || c.MaxUploadBytes < 1 {
		validationErrors = append(validationErrors, fmt.Errorf("VOICE_STORAGE_DIR and positive VOICE_RETENTION_HOURS are required"))
	}
	if c.InboxURL == "" || c.WhisperURL == "" || c.WorkerPollMillis < 100 || c.WorkerMaxAttempts < 1 || c.WorkerLeaseSeconds < 1 || c.WorkerRetryBackoffSeconds < 1 {
		validationErrors = append(validationErrors, fmt.Errorf("worker URLs, poll period, and max attempts must be valid"))
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("LOG_LEVEL must be a slog level: %w", err))
	}
	return errors.Join(validationErrors...)
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// multipartAllowance is the room an upload request needs above
// MaxUploadBytes for multipart framing and the small form fields. With the
// default 25 MiB audio limit it matches the gateway's 26m body limit.
const multipartAllowance = 1 << 20

// RequestBodyLimit is the HTTP request body limit of the Voice API. Without
// it Fiber's 4 MiB default would refuse valid recordings long before
// MaxUploadBytes; within this limit the upload handler answers 413 itself
// for audio over MaxUploadBytes.
func (c Config) RequestBodyLimit() int {
	return int(c.MaxUploadBytes) + multipartAllowance
}

// DatabaseConfig adapts service configuration to the shared pool helper.
func (c Config) DatabaseConfig() database.Config {
	return database.Config{
		URL:             c.DatabaseURL,
		ApplicationName: c.ServiceName,
	}
}
