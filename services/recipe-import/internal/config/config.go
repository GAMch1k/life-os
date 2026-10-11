// Package config defines the recipe-import service's configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
)

// Config holds the recipe-import process's configuration.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"recipe-import"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	// FoodURL is the Food service recipes are saved in.
	FoodURL string `env:"FOOD_SERVICE_URL" required:"true"`
	// ActivityURL enables Activity events; empty disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// Fetch limits: how long a page may take and how large it may be.
	TimeoutSeconds int `env:"RECIPE_IMPORT_TIMEOUT_SECONDS" default:"10"`
	MaxBytes       int `env:"RECIPE_IMPORT_MAX_BYTES" default:"5242880"`
	// AllowHosts are host names fetched whatever their address (comma
	// separated): for the verification stack's fixture server only. They
	// lift the protection against reaching internal services for those
	// hosts; never set it on the deployed stack.
	AllowHosts string `env:"RECIPE_IMPORT_ALLOW_HOSTS"`
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
	if strings.TrimSpace(c.FoodURL) == "" {
		validationErrors = append(validationErrors, fmt.Errorf("FOOD_SERVICE_URL must name the Food service"))
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 {
		validationErrors = append(validationErrors, fmt.Errorf("RECIPE_IMPORT_TIMEOUT_SECONDS must be between 1 and 60"))
	}
	if c.MaxBytes < 64<<10 || c.MaxBytes > 20<<20 {
		validationErrors = append(validationErrors, fmt.Errorf("RECIPE_IMPORT_MAX_BYTES must be between 65536 and 20971520"))
	}
	return errors.Join(validationErrors...)
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// Timeout is how long a page may take.
func (c Config) Timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }

// AllowedHosts are the hosts that skip the address check.
func (c Config) AllowedHosts() []string {
	var hosts []string
	for _, host := range strings.Split(c.AllowHosts, ",") {
		if host = strings.TrimSpace(host); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}
