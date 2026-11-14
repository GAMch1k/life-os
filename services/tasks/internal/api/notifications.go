package api

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/identity"
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

// GetNotificationStatus is Status's details, so the server's admins'
// (docs/architecture/multi-user.md#roles-and-checks): a member's identity
// token is refused; the Dashboard reads it without one.
func (h *Handler) GetNotificationStatus(c fiber.Ctx) error {
	if err := identity.RequireAdmin(c, identity.AdminPolicy{}); err != nil {
		return err
	}
	if h.notifications == nil {
		return c.JSON(generated.TaskNotificationStatus{})
	}
	status, err := h.notifications.Status(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(status)
}
