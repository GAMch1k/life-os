package api

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
)

// NotificationStatus reports Task notifications' health
// (internal/notifications).
type NotificationStatus interface {
	Status(ctx context.Context) (generated.TaskNotificationStatus, error)
}

// WithNotifications serves GET /v1/notifications/status from status.
// Without it Task notifications read as not enabled.
func (h *Handler) WithNotifications(status NotificationStatus) *Handler {
	h.notifications = status
	return h
}

func (h *Handler) GetNotificationStatus(c fiber.Ctx) error {
	if h.notifications == nil {
		return c.JSON(generated.TaskNotificationStatus{})
	}
	status, err := h.notifications.Status(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(status)
}
