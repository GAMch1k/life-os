package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://banking_user:secret@localhost/banking_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "banking" || cfg.Port != 8080 || cfg.LogLevel != "info" || cfg.TimeZone != "UTC" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.RedirectURI != "https://lifeos.gamch1k.com/finance/bank/callback" || cfg.EnableBankingAPIURL != "https://api.enablebanking.com" {
		t.Fatalf("unexpected Enable Banking defaults: %+v", cfg)
	}
	opts := cfg.Options()
	if strings.Join(opts.Schedule.Times(), ",") != "06:00,12:00,18:00,23:00" || opts.MaxUnattended != 4 ||
		opts.ManualInterval != 10*time.Minute || opts.InitialDays != 90 || opts.OverlapDays != 7 || opts.HistoryDays != 730 {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                   "postgres://banking_user:secret@localhost/banking_db",
		"PORT":                           "70000",
		"LOG_LEVEL":                      "loud",
		"LIFE_OS_TIMEZONE":               "Mars/Olympus",
		"BANKING_SYNC_TIMES":             "06:00,09:00,12:00,15:00,18:00",
		"BANKING_MAX_UNATTENDED_PER_DAY": "5",
		"BANKING_MANUAL_SYNC_MINUTES":    "0",
		"BANKING_HISTORY_DAYS":           "3651",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"PORT", "LOG_LEVEL", "LIFE_OS_TIMEZONE", "BANKING_SYNC_TIMES", "BANKING_MAX_UNATTENDED_PER_DAY", "BANKING_MANUAL_SYNC_MINUTES", "BANKING_HISTORY_DAYS"} {
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

func TestZeroOverlapMeansNone(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": "postgres://x@localhost/banking_db", "BANKING_OVERLAP_DAYS": "0", "LIFE_OS_TIMEZONE": "Europe/Berlin"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Options().OverlapDays != -1 || cfg.Location().String() != "Europe/Berlin" {
		t.Fatalf("options = %+v", cfg.Options())
	}
}

func TestZeroHistoryDaysTurnTheHistoryReadOff(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": "postgres://x@localhost/banking_db", "BANKING_HISTORY_DAYS": "0"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Options().HistoryDays != -1 {
		t.Fatalf("options = %+v", cfg.Options())
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
