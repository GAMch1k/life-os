// Package api contains the service's HTTP transport adapters.
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// Handler implements the generated transport interface. Domain behavior is
// introduced with the calendar service implementation, not with the contract.
type Handler struct{}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ListEvents(c fiber.Ctx, params generated.ListEventsParams) error {
	return notImplemented()
}

func (h *Handler) CreateEvent(c fiber.Ctx) error { return notImplemented() }

func (h *Handler) DeleteEvent(c fiber.Ctx, eventID types.UUID) error { return notImplemented() }

func (h *Handler) GetEvent(c fiber.Ctx, eventID types.UUID) error { return notImplemented() }

func (h *Handler) UpdateEvent(c fiber.Ctx, eventID types.UUID) error { return notImplemented() }

func notImplemented() error {
	return fiber.NewError(fiber.StatusNotImplemented, "calendar endpoint is not implemented yet")
}
