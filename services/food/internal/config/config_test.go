package config

import (
	"strings"
	"testing"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://food_user:secret@localhost/food_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "food" || cfg.Port != 8080 || cfg.LogLevel != "info" || cfg.TimeZone != "UTC" || cfg.ActivityURL != "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	// Expiry reminders are off without Notifications, due at 08:00.
	clock, err := cfg.ExpiryClock()
	if cfg.NotificationsURL != "" || cfg.NotifyIntervalSeconds != 30 || cfg.NotifyGraceMinutes != 120 || err != nil || clock.String() != "08:00" {
		t.Fatalf("unexpected reminder config: %+v, %s, %v", cfg, clock, err)
	}
}

func TestExpiryReminderSettings(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":              "postgres://food_user:secret@localhost/food_db",
		"NOTIFICATIONS_SERVICE_URL": "http://notifications:8080",
		"FOOD_EXPIRY_TIME":          "07:30",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if clock, err := cfg.ExpiryClock(); err != nil || clock.String() != "07:30" || cfg.NotificationsURL != "http://notifications:8080" {
		t.Fatalf("config = %+v, %s, %v", cfg, clock, err)
	}
	_, err = LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                 "postgres://food_user:secret@localhost/food_db",
		"FOOD_EXPIRY_TIME":             "8am",
		"FOOD_NOTIFY_INTERVAL_SECONDS": "1",
		"FOOD_NOTIFY_GRACE_MINUTES":    "5",
	}))
	for _, want := range []string{"FOOD_EXPIRY_TIME", "FOOD_NOTIFY_INTERVAL_SECONDS", "FOOD_NOTIFY_GRACE_MINUTES"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %s", err, want)
		}
	}
}

func TestOwnerTimeZone(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]string{"": "UTC", "Europe/Berlin": "Europe/Berlin", "Mars/Olympus": "UTC", "Local": "UTC"} {
		got, err := Config{TimeZone: value}.OwnerTimeZone()
		if got != want || (err != nil) != (value == "Mars/Olympus" || value == "Local") {
			t.Errorf("OwnerTimeZone(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://food_user:secret@localhost/food_db",
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
