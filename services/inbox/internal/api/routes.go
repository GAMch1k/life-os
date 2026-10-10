package api

import (
	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/inbox/internal/api/generated"
)

// Register mounts the Inbox API on router. Every write must say who makes it
// in X-Life-OS-Actor; activity.RequireActor rejects it otherwise, so no change
// is attributed to the owner by default.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}
