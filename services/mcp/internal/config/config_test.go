package config

import (
	"os"
	"testing"
)

// MCP calls each owning service at its compose address unless told otherwise.
func TestLoadNamesEveryDownstreamService(t *testing.T) {
	for _, name := range []string{"FINANCE_SERVICE_URL", "TASKS_SERVICE_URL", "INBOX_SERVICE_URL", "CALENDAR_SERVICE_URL", "FOOD_SERVICE_URL", "RECIPE_IMPORT_SERVICE_URL", "PORT", "LIFE_OS_TIMEZONE"} {
		t.Setenv(name, "") // restored after the test
		_ = os.Unsetenv(name)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{ServiceName: cfg.ServiceName, Port: 8080, FinanceURL: "http://finance:8080", TasksURL: "http://tasks:8080", InboxURL: "http://inbox:8080", CalendarURL: "http://calendar:8080", FoodURL: "http://food:8080", RecipeImportURL: "http://recipe-import:8080", ActivityURL: cfg.ActivityURL, TimeZone: "UTC"}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
	t.Setenv("FOOD_SERVICE_URL", "http://food.test:9000")
	if cfg, err := Load(); err != nil || cfg.FoodURL != "http://food.test:9000" {
		t.Fatalf("FoodURL = %q, err = %v", cfg.FoodURL, err)
	}
	// The owner's timezone, for the Task and event tools' times and moves.
	t.Setenv("LIFE_OS_TIMEZONE", "Europe/Berlin")
	if cfg, err := Load(); err != nil || cfg.TimeZone != "Europe/Berlin" {
		t.Fatalf("TimeZone = %q, err = %v", cfg.TimeZone, err)
	}
}
