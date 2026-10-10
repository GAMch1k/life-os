package api

import (
	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
)

// Register mounts the Notifications API on router. Every write (sending a
// notification, marking read) must say who makes it in X-Life-OS-Actor;
// activity.RequireActor rejects it otherwise.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}
