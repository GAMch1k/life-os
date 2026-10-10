package config

import (
	"strings"
	"testing"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://calendar_user:secret@localhost/calendar_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "calendar" || cfg.Port != 8080 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.SourceRefreshAt != "04:30" || cfg.ICSMaxBytes != 5<<20 || cfg.ICSTimeoutSeconds != 30 || cfg.ICSAllowPrivateURLs || cfg.ActivityURL != "" {
		t.Fatalf("unexpected source defaults: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidSourceSettings(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                 "postgres://calendar_user:secret@localhost/calendar_db",
		"CALENDAR_SOURCE_REFRESH_AT":   "4:30",
		"CALENDAR_ICS_MAX_BYTES":       "10",
		"CALENDAR_ICS_TIMEOUT_SECONDS": "0",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"CALENDAR_SOURCE_REFRESH_AT", "CALENDAR_ICS_MAX_BYTES", "CALENDAR_ICS_TIMEOUT_SECONDS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://calendar_user:secret@localhost/calendar_db",
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

func TestOwnerTimeZone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value, want string
		warns       bool
	}{
		{"", "UTC", false},
		{"Europe/Berlin", "Europe/Berlin", false},
		{"Mars/Olympus", "UTC", true},
		{"Local", "UTC", true},
	} {
		values := map[string]string{"DATABASE_URL": "postgres://calendar_user:secret@localhost/calendar_db"}
		if tc.value != "" {
			values["LIFE_OS_TIMEZONE"] = tc.value
		}
		cfg, err := LoadFrom(mapLookup(values))
		if err != nil {
			t.Fatal(err)
		}
		got, warning := cfg.OwnerTimeZone()
		if got != tc.want || (warning != nil) != tc.warns {
			t.Errorf("LIFE_OS_TIMEZONE=%q: %q, %v; want %q (warning %v)", tc.value, got, warning, tc.want, tc.warns)
		}
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
