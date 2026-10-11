package config

import (
	"strings"
	"testing"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://inbox_user:secret@localhost/inbox_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "inbox" || cfg.Port != 8080 || cfg.LogLevel != "info" || cfg.NotificationsURL != "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadFromReadsTheNotificationsURL(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":              "postgres://inbox_user:secret@localhost/inbox_db",
		"NOTIFICATIONS_SERVICE_URL": "http://notifications:8080",
	}))
	if err != nil || cfg.NotificationsURL != "http://notifications:8080" {
		t.Fatalf("NotificationsURL = %q, err = %v", cfg.NotificationsURL, err)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://inbox_user:secret@localhost/inbox_db",
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

func TestLoadFromReadsTheCaptureMediaSettings(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://inbox_user:secret@localhost/inbox_db",
	}))
	if err != nil || cfg.MediaDir != "/var/lib/lifeos/inbox-media" || cfg.MaxImageBytes != 10<<20 {
		t.Fatalf("defaults: %+v, err = %v", cfg, err)
	}
	// Five images at the limit, plus multipart framing.
	if cfg.RequestBodyLimit() != 5*(10<<20)+(1<<20) {
		t.Fatalf("body limit = %d", cfg.RequestBodyLimit())
	}
	cfg, err = LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":          "postgres://inbox_user:secret@localhost/inbox_db",
		"INBOX_MEDIA_DIR":       "/data/media",
		"INBOX_MEDIA_MAX_BYTES": "2097152",
	}))
	if err != nil || cfg.MediaDir != "/data/media" || cfg.MaxImageBytes != 2<<20 {
		t.Fatalf("configured: %+v, err = %v", cfg, err)
	}
	for _, value := range []string{"0", "20971521"} {
		if _, err := LoadFrom(mapLookup(map[string]string{
			"DATABASE_URL":          "postgres://inbox_user:secret@localhost/inbox_db",
			"INBOX_MEDIA_MAX_BYTES": value,
		})); err == nil || !strings.Contains(err.Error(), "INBOX_MEDIA_MAX_BYTES") {
			t.Errorf("INBOX_MEDIA_MAX_BYTES=%s: err = %v", value, err)
		}
	}
}
