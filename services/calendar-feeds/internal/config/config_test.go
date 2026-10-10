package config

import (
	"strings"
	"testing"
)

var required = map[string]string{
	"DATABASE_URL":         "postgres://calendar_feeds_user:secret@localhost/calendar_feeds_db",
	"CALENDAR_SERVICE_URL": "http://calendar:8080",
}

func with(values map[string]string) map[string]string {
	merged := map[string]string{}
	for key, value := range required {
		merged[key] = value
	}
	for key, value := range values {
		merged[key] = value
	}
	return merged
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(required))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "calendar-feeds" || cfg.Port != 8080 || cfg.LogLevel != "info" || cfg.TimeZone != "UTC" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.RefreshAt != "04:30" || cfg.MaxBytes != 5<<20 || cfg.TimeoutSeconds != 30 || cfg.AllowPrivateURLs || cfg.ActivityURL != "" || cfg.ZAWICSURL != "" {
		t.Fatalf("unexpected feed defaults: %+v", cfg)
	}
}

// The ZAW feed comes from exactly LIFE_OS_ZAW_ICS_URL, the variable the
// verification stack blanks.
func TestLoadFromReadsZAWFeed(t *testing.T) {
	t.Parallel()

	const feed = "https://feeds.example.test/abholtermine.ics?area=<area>"
	cfg, err := LoadFrom(mapLookup(with(map[string]string{"LIFE_OS_ZAW_ICS_URL": feed})))
	if err != nil || cfg.ZAWICSURL != feed {
		t.Fatalf("LoadFrom() = %q, %v", cfg.ZAWICSURL, err)
	}
}

func TestLoadFromRejectsInvalidFeedSettings(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(with(map[string]string{
		"CALENDAR_FEEDS_REFRESH_AT":      "4:30",
		"CALENDAR_FEEDS_MAX_BYTES":       "10",
		"CALENDAR_FEEDS_TIMEOUT_SECONDS": "0",
		"PORT":                           "70000",
		"LOG_LEVEL":                      "loud",
	})))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"CALENDAR_FEEDS_REFRESH_AT", "CALENDAR_FEEDS_MAX_BYTES", "CALENDAR_FEEDS_TIMEOUT_SECONDS", "PORT", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoadFromRequiresDatabaseAndCalendar(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "CALENDAR_SERVICE_URL") {
		t.Fatalf("LoadFrom() error = %v", err)
	}
}

func TestOwnerTimeZone(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]string{"": "UTC", "Europe/Berlin": "Europe/Berlin", "Mars/Olympus": "UTC", "Local": "UTC"} {
		got, _ := Config{TimeZone: value}.OwnerTimeZone()
		if got != want {
			t.Errorf("OwnerTimeZone(%q) = %q, want %q", value, got, want)
		}
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
