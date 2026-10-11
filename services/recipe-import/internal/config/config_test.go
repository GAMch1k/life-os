package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	cfg, err := LoadFrom(mapLookup(map[string]string{"FOOD_SERVICE_URL": "http://food:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "recipe-import" || cfg.Port != 8080 || cfg.Timeout() != 10*time.Second || cfg.MaxBytes != 5<<20 || cfg.AllowedHosts() != nil {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	_, err := LoadFrom(mapLookup(map[string]string{
		"FOOD_SERVICE_URL": "http://food:8080", "RECIPE_IMPORT_TIMEOUT_SECONDS": "0", "RECIPE_IMPORT_MAX_BYTES": "10", "LOG_LEVEL": "loud",
	}))
	for _, want := range []string{"RECIPE_IMPORT_TIMEOUT_SECONDS", "RECIPE_IMPORT_MAX_BYTES", "LOG_LEVEL"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %s", err, want)
		}
	}
	if _, err := LoadFrom(mapLookup(nil)); err == nil || !strings.Contains(err.Error(), "FOOD_SERVICE_URL") {
		t.Fatalf("without Food: %v", err)
	}
}

func TestAllowedHosts(t *testing.T) {
	cfg := Config{AllowHosts: " ics-fixture , ,other "}
	if got := cfg.AllowedHosts(); !reflect.DeepEqual(got, []string{"ics-fixture", "other"}) {
		t.Fatalf("hosts = %q", got)
	}
}
