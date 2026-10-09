package config

import (
	"fmt"
	shared "github.com/GAMch1k/life-os/pkg/config"
)

type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"mcp"`
	Port        int    `env:"PORT" default:"8080"`
	FinanceURL  string `env:"FINANCE_SERVICE_URL" default:"http://finance:8080"`
	TasksURL    string `env:"TASKS_SERVICE_URL" default:"http://tasks:8080"`
	InboxURL    string `env:"INBOX_SERVICE_URL" default:"http://inbox:8080"`
	CalendarURL string `env:"CALENDAR_SERVICE_URL" default:"http://calendar:8080"`
	// ActivityURL enables activity events; empty disables them.
	ActivityURL string `env:"ACTIVITY_SERVICE_URL"`
}

func Load() (Config, error) {
	var c Config
	if err := shared.Load(&c); err != nil {
		return c, err
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("PORT must be between 1 and 65535")
	}
	return c, nil
}
