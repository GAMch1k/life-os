package config_test

import (
	"fmt"
	"log"

	"github.com/GAMch1k/life-os/pkg/config"
)

func ExampleLoadFrom() {
	type Config struct {
		Port        int    `env:"PORT" default:"8080"`
		ServiceName string `env:"SERVICE_NAME" required:"true"`
		LogLevel    string `env:"LOG_LEVEL" default:"info"`
		DatabaseURL string `env:"DATABASE_URL" required:"true"`
	}

	environment := map[string]string{
		"SERVICE_NAME": "tasks",
		"DATABASE_URL": "postgres://tasks:secret@localhost/tasks",
	}
	lookup := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}

	var cfg Config
	if err := config.LoadFrom(&cfg, lookup); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%s listens on :%d at %s level\n", cfg.ServiceName, cfg.Port, cfg.LogLevel)
	// Output: tasks listens on :8080 at info level
}
