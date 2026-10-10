package api

import (
	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// The integration API (P11-06): see docs/services/calendar-integration-api.md.

func notImplemented() error {
	return httpserver.NewError(fiber.StatusNotImplemented, "not_implemented", "the integration API is not implemented yet")
}

func (h *Handler) ListIntegrationSources(c fiber.Ctx, integration string) error {
	return notImplemented()
}

func (h *Handler) GetIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	return notImplemented()
}

func (h *Handler) RegisterIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	return notImplemented()
}

func (h *Handler) DeleteIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	return notImplemented()
}

func (h *Handler) SyncIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	return notImplemented()
}

func (h *Handler) ListChanges(c fiber.Ctx, params generated.ListChangesParams) error {
	return notImplemented()
}
