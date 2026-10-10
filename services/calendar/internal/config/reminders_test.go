package config

import (
	"strings"
	"testing"
)

func TestLoadFromReadsReminderDelivery(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": "postgres://calendar_user:secret@localhost/calendar_db"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NotificationsURL != "" || cfg.ReminderIntervalSeconds != 30 || cfg.ReminderGraceMinutes != 120 {
		t.Fatalf("reminder defaults: %+v", cfg)
	}
	cfg, err = LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                       "postgres://calendar_user:secret@localhost/calendar_db",
		"NOTIFICATIONS_SERVICE_URL":          "http://notifications:8080",
		"CALENDAR_REMINDER_INTERVAL_SECONDS": "15",
		"CALENDAR_REMINDER_GRACE_MINUTES":    "60",
	}))
	if err != nil || cfg.NotificationsURL != "http://notifications:8080" || cfg.ReminderIntervalSeconds != 15 || cfg.ReminderGraceMinutes != 60 {
		t.Fatalf("LoadFrom() = %+v, %v", cfg, err)
	}
}

func TestLoadFromRejectsInvalidReminderDelivery(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                       "postgres://calendar_user:secret@localhost/calendar_db",
		"CALENDAR_REMINDER_INTERVAL_SECONDS": "1",
		"CALENDAR_REMINDER_GRACE_MINUTES":    "5",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	for _, want := range []string{"CALENDAR_REMINDER_INTERVAL_SECONDS", "CALENDAR_REMINDER_GRACE_MINUTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
