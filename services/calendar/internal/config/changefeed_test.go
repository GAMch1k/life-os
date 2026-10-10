package config

import (
	"strings"
	"testing"
)

func TestLoadFromReadsTheChangeFeedRetention(t *testing.T) {
	t.Parallel()

	const database = "postgres://calendar_user:secret@localhost/calendar_db"
	cfg, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": database}))
	if err != nil || cfg.ChangeRetentionDays != 30 {
		t.Fatalf("default retention: %+v, %v", cfg, err)
	}
	cfg, err = LoadFrom(mapLookup(map[string]string{"DATABASE_URL": database, "CALENDAR_CHANGE_RETENTION_DAYS": "7"}))
	if err != nil || cfg.ChangeRetentionDays != 7 {
		t.Fatalf("retention 7: %+v, %v", cfg, err)
	}
	for _, value := range []string{"0", "3651"} {
		_, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": database, "CALENDAR_CHANGE_RETENTION_DAYS": value}))
		if err == nil || !strings.Contains(err.Error(), "CALENDAR_CHANGE_RETENTION_DAYS") {
			t.Errorf("retention %s: error = %v", value, err)
		}
	}
}
