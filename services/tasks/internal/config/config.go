// Package config defines configuration owned by one generated service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/services/tasks/internal/notifications"
)

// Config holds process-level configuration for a generated service.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"tasks"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	DatabaseURL string `env:"DATABASE_URL" required:"true"`
	// ActivityURL enables activity events; empty disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
	// TimeZone is the owner's timezone (LIFE_OS_TIMEZONE, an IANA name such
	// as Europe/Berlin): Task notifications are due on its wall clock, and
	// a due date without a time is its local midnight.
	TimeZone string `env:"LIFE_OS_TIMEZONE" default:"UTC"`
	// Task notifications: the Notifications service they are sent to (empty
	// turns them off), the Calendar service the morning summary reads the
	// day's events from (empty: no events), how often due notifications are
	// looked for, how late one may still be sent after Tasks was down, and
	// when they are due (owner-local HH:MM; the timed reminder's lead in
	// minutes).
	NotificationsURL      string `env:"NOTIFICATIONS_SERVICE_URL"`
	CalendarURL           string `env:"CALENDAR_SERVICE_URL"`
	NotifyIntervalSeconds int    `env:"TASKS_NOTIFY_INTERVAL_SECONDS" default:"30"`
	NotifyGraceMinutes    int    `env:"TASKS_NOTIFY_GRACE_MINUTES" default:"120"`
	SummaryTime           string `env:"TASKS_SUMMARY_TIME" default:"08:00"`
	MorningTime           string `env:"TASKS_MORNING_TIME" default:"09:00"`
	EveningTime           string `env:"TASKS_EVENING_TIME" default:"19:00"`
	TimedLeadMinutes      int    `env:"TASKS_TIMED_LEAD_MINUTES" default:"30"`
}

// Load reads Config from the process environment.
func Load() (Config, error) {
	return LoadFrom(nil)
}

// LoadFrom reads Config through lookup. Passing nil reads the process
// environment and keeps tests independent from global process state.
func LoadFrom(lookup sharedconfig.LookupFunc) (Config, error) {
	var cfg Config
	var err error
	if lookup == nil {
		err = sharedconfig.Load(&cfg)
	} else {
		err = sharedconfig.LoadFrom(&cfg, lookup)
	}
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate implements the shared configuration validation hook.
func (c Config) Validate() error {
	var validationErrors []error
	if c.ServiceName == "" {
		validationErrors = append(validationErrors, fmt.Errorf("SERVICE_NAME must not be empty"))
	}
	if c.Port < 1 || c.Port > 65535 {
		validationErrors = append(validationErrors, fmt.Errorf("PORT must be between 1 and 65535"))
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("LOG_LEVEL must be a slog level: %w", err))
	}
	if c.NotifyIntervalSeconds < 5 || c.NotifyIntervalSeconds > 300 {
		validationErrors = append(validationErrors, fmt.Errorf("TASKS_NOTIFY_INTERVAL_SECONDS must be between 5 and 300"))
	}
	if c.NotifyGraceMinutes < 10 || c.NotifyGraceMinutes > 1440 {
		validationErrors = append(validationErrors, fmt.Errorf("TASKS_NOTIFY_GRACE_MINUTES must be between 10 and 1440"))
	}
	if _, err := c.NotificationSchedule(); err != nil {
		validationErrors = append(validationErrors, err)
	}
	return errors.Join(validationErrors...)
}

// NotificationSchedule returns when Task notifications are due.
func (c Config) NotificationSchedule() (notifications.Schedule, error) {
	var schedule notifications.Schedule
	var validationErrors []error
	for _, setting := range []struct {
		name, value string
		target      *notifications.Clock
	}{
		{"TASKS_SUMMARY_TIME", c.SummaryTime, &schedule.Summary},
		{"TASKS_MORNING_TIME", c.MorningTime, &schedule.Morning},
		{"TASKS_EVENING_TIME", c.EveningTime, &schedule.Evening},
	} {
		clock, err := notifications.ParseClock(strings.TrimSpace(setting.value))
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("%s must be a 24-hour HH:MM time: %w", setting.name, err))
		}
		*setting.target = clock
	}
	if c.TimedLeadMinutes < 0 || c.TimedLeadMinutes > 1440 {
		validationErrors = append(validationErrors, fmt.Errorf("TASKS_TIMED_LEAD_MINUTES must be between 0 and 1440"))
	}
	schedule.TimedLead = time.Duration(c.TimedLeadMinutes) * time.Minute
	return schedule, errors.Join(validationErrors...)
}

// OwnerTimeZone returns the validated LIFE_OS_TIMEZONE name. Unset means UTC.
// An unknown name also falls back to UTC, as in Calendar, the Dashboard, and
// the processor, and is returned as an error to log, so a typo does not stop
// the service.
func (c Config) OwnerTimeZone() (string, error) {
	name := strings.TrimSpace(c.TimeZone)
	if name == "" {
		return "UTC", nil
	}
	if !ValidTimeZone(name) {
		return "UTC", fmt.Errorf("LIFE_OS_TIMEZONE %q is not an IANA timezone name such as Europe/Berlin; using UTC", name)
	}
	return name, nil
}

// ValidTimeZone reports whether name is an IANA timezone such as
// Europe/Berlin. "Local" (the process's own zone) is not one here.
func ValidTimeZone(name string) bool {
	if name == "" || name == "Local" || strings.TrimSpace(name) != name {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// DatabaseConfig adapts service configuration to the shared pool helper.
func (c Config) DatabaseConfig() database.Config {
	return database.Config{
		URL:             c.DatabaseURL,
		ApplicationName: c.ServiceName,
	}
}
