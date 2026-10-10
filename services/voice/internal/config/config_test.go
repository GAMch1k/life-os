package config

import (
	"strings"
	"testing"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://voice_user:secret@localhost/voice_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "voice" || cfg.Port != 8080 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://voice_user:secret@localhost/voice_db",
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

func TestRequestBodyLimitLeavesRoomForMultipartFraming(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://voice_user:secret@localhost/voice_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// 25 MiB of audio plus 1 MiB of framing: the gateway's 26m body limit.
	if cfg.MaxUploadBytes != 25<<20 || cfg.RequestBodyLimit() != 26<<20 {
		t.Fatalf("MaxUploadBytes %d, RequestBodyLimit %d", cfg.MaxUploadBytes, cfg.RequestBodyLimit())
	}
}
