package config

import (
	"strings"
	"testing"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://notifications_user:secret@localhost/notifications_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "notifications" || cfg.Port != 8080 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://notifications_user:secret@localhost/notifications_db",
		"PORT":         "70000",
		"LOG_LEVEL":    "loud",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"PORT", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoadFromRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("LoadFrom() error = %v", err)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadFromAppliesDeliveryDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://notifications_user:secret@localhost/notifications_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ChannelNames()) != 0 {
		t.Fatalf("channels = %v, want none by default", cfg.ChannelNames())
	}
	if cfg.WorkerPollMillis != 1000 || cfg.LeaseSeconds != 60 || cfg.SendTimeoutSeconds != 15 ||
		cfg.RetryBaseSeconds != 10 || cfg.RetryMaxSeconds != 900 || cfg.MaxAgeSeconds != 86400 {
		t.Fatalf("unexpected delivery defaults: %+v", cfg)
	}
}

func TestLoadFromParsesChannels(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":           "postgres://notifications_user:secret@localhost/notifications_db",
		"NOTIFICATIONS_CHANNELS": " Log , ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ChannelNames(); len(got) != 1 || got[0] != "log" {
		t.Fatalf("channels = %v, want [log]", got)
	}
}

func TestLoadFromRejectsInvalidDeliverySettings(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                           "postgres://notifications_user:secret@localhost/notifications_db",
		"NOTIFICATIONS_CHANNELS":                 "log,log",
		"NOTIFICATIONS_LEASE_SECONDS":            "10",
		"NOTIFICATIONS_SEND_TIMEOUT_SECONDS":     "10",
		"NOTIFICATIONS_RETRY_BASE_SECONDS":       "60",
		"NOTIFICATIONS_RETRY_MAX_SECONDS":        "30",
		"NOTIFICATIONS_DELIVERY_MAX_AGE_SECONDS": "5",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"twice", "SEND_TIMEOUT", "RETRY_BASE", "MAX_AGE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
