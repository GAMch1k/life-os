package database

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParsePoolConfig(t *testing.T) {
	t.Parallel()

	config, err := ParsePoolConfig(Config{
		URL:               "postgres://user:secret@localhost:5432/example?sslmode=disable",
		ApplicationName:   "example-service",
		MinConnections:    2,
		MaxConnections:    8,
		MaxConnectionIdle: 3 * time.Minute,
		MaxConnectionLife: time.Hour,
		HealthCheckPeriod: 30 * time.Second,
		ConnectTimeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.MinConns != 2 || config.MaxConns != 8 {
		t.Fatalf("connection bounds = (%d, %d)", config.MinConns, config.MaxConns)
	}
	if config.MaxConnIdleTime != 3*time.Minute || config.MaxConnLifetime != time.Hour {
		t.Fatalf("connection lifetimes = (%s, %s)", config.MaxConnIdleTime, config.MaxConnLifetime)
	}
	if config.HealthCheckPeriod != 30*time.Second || config.ConnConfig.ConnectTimeout != 2*time.Second {
		t.Fatalf("health/connect durations = (%s, %s)", config.HealthCheckPeriod, config.ConnConfig.ConnectTimeout)
	}
	if got := config.ConnConfig.RuntimeParams["application_name"]; got != "example-service" {
		t.Fatalf("application_name = %q", got)
	}
}

func TestParsePoolConfigRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{name: "missing URL", config: Config{}, want: "URL is required"},
		{name: "negative minimum", config: Config{URL: "postgres://localhost/db", MinConnections: -1}, want: "minimum connections"},
		{name: "negative maximum", config: Config{URL: "postgres://localhost/db", MaxConnections: -1}, want: "maximum connections"},
		{name: "minimum above maximum", config: Config{URL: "postgres://localhost/db", MinConnections: 3, MaxConnections: 2}, want: "must not exceed"},
		{name: "negative duration", config: Config{URL: "postgres://localhost/db", ConnectTimeout: -1}, want: "durations"},
		{name: "malformed URL", config: Config{URL: "://"}, want: "parse connection"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePoolConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParsePoolConfig() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParsePoolConfigUsesDefaultConnectTimeout(t *testing.T) {
	t.Parallel()

	config, err := ParsePoolConfig(Config{URL: "postgres://localhost/example"})
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.ConnectTimeout != 5*time.Second {
		t.Fatalf("connect timeout = %s, want 5s", config.ConnConfig.ConnectTimeout)
	}
}

func TestOpenConfigRejectsNilInputs(t *testing.T) {
	t.Parallel()
	if _, err := OpenConfig(nil, nil); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := OpenConfig(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("nil config error = %v", err)
	}
}

func TestClosePoolAcceptsNil(t *testing.T) {
	t.Parallel()
	if err := ClosePool(nil)(context.Background()); err != nil {
		t.Fatal(err)
	}
}
