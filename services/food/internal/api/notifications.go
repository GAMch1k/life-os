package api

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

// NotificationStatus reports the expiry reminders' health
// (internal/notifications).
type NotificationStatus interface {
	Status(ctx context.Context) (generated.FoodNotificationStatus, error)
}

// WithNotifications serves GET /v1/notifications/status from status.
// Without it expiry reminders read as not enabled.
func (h *Handler) WithNotifications(status NotificationStatus) *Handler {
	h.notifications = status
	return h
}

func (h *Handler) GetNotificationStatus(c fiber.Ctx) error {
	if h.notifications == nil {
		return c.JSON(generated.FoodNotificationStatus{})
	}
	status, err := h.notifications.Status(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(status)
}
