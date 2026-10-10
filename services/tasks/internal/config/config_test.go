package config

import (
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/tasks/internal/notifications"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://tasks_user:secret@localhost/tasks_db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "tasks" || cfg.Port != 8080 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://tasks_user:secret@localhost/tasks_db",
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

func TestTaskNotificationSettings(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": "postgres://tasks_user:secret@localhost/tasks_db"}))
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := cfg.NotificationSchedule()
	if err != nil || schedule != notifications.DefaultSchedule() {
		t.Fatalf("default schedule = %+v, %v", schedule, err)
	}
	if cfg.NotificationsURL != "" || cfg.CalendarURL != "" || cfg.NotifyIntervalSeconds != 30 || cfg.NotifyGraceMinutes != 120 || cfg.TimeZone != "UTC" {
		t.Fatalf("defaults = %+v", cfg)
	}

	cfg, err = LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":              "postgres://tasks_user:secret@localhost/tasks_db",
		"NOTIFICATIONS_SERVICE_URL": "http://notifications:8080",
		"CALENDAR_SERVICE_URL":      "http://calendar:8080",
		"LIFE_OS_TIMEZONE":          "Europe/Berlin",
		"TASKS_SUMMARY_TIME":        "09:00",
		"TASKS_MORNING_TIME":        "09:00",
		"TASKS_EVENING_TIME":        "20:30",
		"TASKS_TIMED_LEAD_MINUTES":  "15",
	}))
	if err != nil {
		t.Fatal(err)
	}
	schedule, _ = cfg.NotificationSchedule()
	if want := (notifications.Schedule{Summary: notifications.Clock{Hour: 9}, Morning: notifications.Clock{Hour: 9}, Evening: notifications.Clock{Hour: 20, Minute: 30}, TimedLead: 15 * time.Minute}); schedule != want || !schedule.Merged() {
		t.Fatalf("schedule = %+v", schedule)
	}
	if zone, err := cfg.OwnerTimeZone(); zone != "Europe/Berlin" || err != nil {
		t.Fatalf("zone = %s, %v", zone, err)
	}

	_, err = LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":                  "postgres://tasks_user:secret@localhost/tasks_db",
		"TASKS_SUMMARY_TIME":            "8:00",
		"TASKS_MORNING_TIME":            "24:00",
		"TASKS_EVENING_TIME":            "evening",
		"TASKS_TIMED_LEAD_MINUTES":      "-5",
		"TASKS_NOTIFY_INTERVAL_SECONDS": "1",
		"TASKS_NOTIFY_GRACE_MINUTES":    "5000",
	}))
	for _, want := range []string{"TASKS_SUMMARY_TIME", "TASKS_MORNING_TIME", "TASKS_EVENING_TIME", "TASKS_TIMED_LEAD_MINUTES", "TASKS_NOTIFY_INTERVAL_SECONDS", "TASKS_NOTIFY_GRACE_MINUTES"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %s", err, want)
		}
	}
}

func TestUnknownTimeZoneFallsBackToUTC(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]string{"": "UTC", "Europe/Berlin": "Europe/Berlin", "Mars/Olympus": "UTC", "Local": "UTC"} {
		zone, err := Config{TimeZone: value}.OwnerTimeZone()
		if zone != want || (err != nil) != (value == "Mars/Olympus" || value == "Local") {
			t.Errorf("OwnerTimeZone(%q) = %s, %v", value, zone, err)
		}
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
