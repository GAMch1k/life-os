package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/config"
)

type serviceConfig struct {
	Port        int           `env:"PORT" default:"8080"`
	ServiceName string        `env:"SERVICE_NAME" required:"true"`
	LogLevel    string        `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string        `env:"DATABASE_URL" required:"true"`
	Timeout     time.Duration `env:"TIMEOUT" default:"5s"`
}

func (c serviceConfig) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("PORT must be between 1 and 65535")
	}
	return nil
}

func TestLoadFromLoadsRequiredValuesAndDefaults(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"SERVICE_NAME": "tasks",
		"DATABASE_URL": "postgres://tasks:secret@localhost/tasks",
	}
	var got serviceConfig

	err := config.LoadFrom(&got, mapLookup(values))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if got.Port != 8080 {
		t.Errorf("Port = %d, want 8080", got.Port)
	}
	if got.ServiceName != "tasks" {
		t.Errorf("ServiceName = %q, want tasks", got.ServiceName)
	}
	if got.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", got.LogLevel)
	}
	if got.DatabaseURL != values["DATABASE_URL"] {
		t.Errorf("DatabaseURL = %q, want %q", got.DatabaseURL, values["DATABASE_URL"])
	}
	if got.Timeout != 5*time.Second {
		t.Errorf("Timeout = %s, want 5s", got.Timeout)
	}
}

func TestLoadFromOverridesDefaults(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"PORT":         "9090",
		"SERVICE_NAME": "finance",
		"LOG_LEVEL":    "debug",
		"DATABASE_URL": "postgres://finance:secret@localhost/finance",
		"TIMEOUT":      "750ms",
	}
	var got serviceConfig

	err := config.LoadFrom(&got, mapLookup(values))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if got.Port != 9090 || got.ServiceName != "finance" || got.LogLevel != "debug" || got.Timeout != 750*time.Millisecond {
		t.Fatalf("LoadFrom() result = %+v", got)
	}
}

func TestLoadFromReportsAllMissingRequiredVariables(t *testing.T) {
	t.Parallel()

	var got serviceConfig
	err := config.LoadFrom(&got, mapLookup(nil))
	if err == nil {
		t.Fatal("LoadFrom() error = nil, want missing required variables")
	}

	message := err.Error()
	for _, want := range []string{"invalid configuration", "SERVICE_NAME", "DATABASE_URL", "required environment variable is not set"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}
}

func TestLoadFromRejectsEmptyRequiredVariable(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"SERVICE_NAME": "   ",
		"DATABASE_URL": "postgres://localhost/tasks",
	}
	var got serviceConfig

	err := config.LoadFrom(&got, mapLookup(values))
	if err == nil || !strings.Contains(err.Error(), "SERVICE_NAME: required environment variable must not be empty") {
		t.Fatalf("LoadFrom() error = %v", err)
	}
}

func TestLoadFromReportsInvalidTypedValueWithoutMutatingTarget(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"PORT":         "not-a-port",
		"SERVICE_NAME": "tasks",
		"DATABASE_URL": "postgres://localhost/tasks",
	}
	original := serviceConfig{Port: 1234, ServiceName: "unchanged"}
	got := original

	err := config.LoadFrom(&got, mapLookup(values))
	if err == nil || !strings.Contains(err.Error(), "PORT: must be an integer") {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if got != original {
		t.Fatalf("failed LoadFrom() mutated target: got %+v, want %+v", got, original)
	}
}

func TestLoadFromRunsValidator(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"PORT":         "70000",
		"SERVICE_NAME": "tasks",
		"DATABASE_URL": "postgres://localhost/tasks",
	}
	var got serviceConfig

	err := config.LoadFrom(&got, mapLookup(values))
	if err == nil || !strings.Contains(err.Error(), "validation failed: PORT must be between 1 and 65535") {
		t.Fatalf("LoadFrom() error = %v", err)
	}
}

func TestLoadFromRejectsInvalidTarget(t *testing.T) {
	t.Parallel()

	for _, target := range []any{nil, serviceConfig{}, (*serviceConfig)(nil)} {
		if err := config.LoadFrom(target, mapLookup(nil)); err == nil || !strings.Contains(err.Error(), "non-nil pointer to a struct") {
			t.Errorf("LoadFrom(%T) error = %v", target, err)
		}
	}
}

func mapLookup(values map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
