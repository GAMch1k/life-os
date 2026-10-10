package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
)

func notImplemented() error {
	return httpserver.NewError(fiber.StatusNotImplemented, "not_implemented", "not implemented yet")
}

func (h *Handler) GetEventOccurrence(c fiber.Ctx, id types.UUID, recurrenceID string) error {
	return notImplemented()
}

func (h *Handler) UpdateEventOccurrence(c fiber.Ctx, id types.UUID, recurrenceID string) error {
	return notImplemented()
}

func (h *Handler) SplitEventSeries(c fiber.Ctx, id types.UUID, recurrenceID string) error {
	return notImplemented()
}

func (h *Handler) TruncateEventSeries(c fiber.Ctx, id types.UUID, recurrenceID string) error {
	return notImplemented()
}
