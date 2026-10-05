package httpserver_test

import (
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/gofiber/fiber/v3"
)

func ExampleNew() {
	app := httpserver.New("example")
	app.Get("/hello", func(c fiber.Ctx) error { return c.SendString("hello") })

	// In main: if err := app.Run(":8080"); err != nil { log.Fatal(err) }
}
