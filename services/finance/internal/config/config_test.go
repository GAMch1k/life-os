package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://finance_user:secret@localhost/finance_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "finance" || cfg.Port != 8080 || cfg.LogLevel != "info" || cfg.DefaultCurrency != "EUR" || cfg.ActivityURL != "" || cfg.NotificationsURL != "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	withNotifications, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://finance_user:secret@localhost/finance_db", "NOTIFICATIONS_SERVICE_URL": "http://notifications:8080",
	}))
	if err != nil || withNotifications.NotificationsURL != "http://notifications:8080" {
		t.Fatalf("notifications URL = %q %v", withNotifications.NotificationsURL, err)
	}
	if location, err := cfg.OwnerLocation(); err != nil || location != time.UTC {
		t.Fatalf("owner location = %v %v", location, err)
	}
}

func TestOwnerLocationFallsBackToUTC(t *testing.T) {
	t.Parallel()

	berlin, err := Config{TimeZone: "Europe/Berlin"}.OwnerLocation()
	if err != nil || berlin.String() != "Europe/Berlin" {
		t.Fatalf("Europe/Berlin = %v %v", berlin, err)
	}
	if location, err := (Config{TimeZone: "Mars/Olympus"}).OwnerLocation(); err == nil || location != time.UTC {
		t.Fatalf("unknown zone = %v %v", location, err)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":             "postgres://finance_user:secret@localhost/finance_db",
		"PORT":                     "70000",
		"LOG_LEVEL":                "loud",
		"FINANCE_DEFAULT_CURRENCY": "euro",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"PORT", "LOG_LEVEL", "FINANCE_DEFAULT_CURRENCY"} {
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
