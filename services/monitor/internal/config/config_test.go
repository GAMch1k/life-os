package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "monitor" || cfg.Port != 8080 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.StatusURL != "http://localhost:3000/api/status" || cfg.NotificationsURL != "" {
		t.Fatalf("unexpected URLs: %+v", cfg)
	}
	if cfg.Interval() != time.Minute || cfg.Confirmations != 2 {
		t.Fatalf("unexpected timing: %+v", cfg)
	}
}

func TestLoadFromReadsTheMonitorSettings(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"MONITOR_STATUS_URL":        "http://dashboard:3000/api/status",
		"NOTIFICATIONS_SERVICE_URL": "http://notifications:8080",
		"MONITOR_INTERVAL_SECONDS":  "30",
		"MONITOR_CONFIRMATIONS":     "3",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatusURL != "http://dashboard:3000/api/status" || cfg.NotificationsURL != "http://notifications:8080" ||
		cfg.Interval() != 30*time.Second || cfg.Confirmations != 3 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"PORT":                      "70000",
		"LOG_LEVEL":                 "loud",
		"MONITOR_STATUS_URL":        "dashboard:3000/api/status",
		"NOTIFICATIONS_SERVICE_URL": "notifications",
		"MONITOR_INTERVAL_SECONDS":  "1",
		"MONITOR_CONFIRMATIONS":     "0",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"PORT", "LOG_LEVEL", "MONITOR_STATUS_URL", "NOTIFICATIONS_SERVICE_URL", "MONITOR_INTERVAL_SECONDS", "MONITOR_CONFIRMATIONS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
