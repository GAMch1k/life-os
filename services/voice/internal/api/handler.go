// Package api contains the service's HTTP transport adapters.
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/voice/internal/api/generated"
)

// Handler implements generated transport interfaces and translates transport
// data at the boundary. Domain services are deliberately not generated here.
type Handler struct{}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler() *Handler { return &Handler{} }

// The Phase 6 design contract is generated now; P6-03 supplies ingestion and
// worker behavior behind these transport methods.
func (h *Handler) CreateAudioJob(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusNotImplemented, "voice ingestion is not deployed")
}
func (h *Handler) GetAudioJob(c fiber.Ctx, _ types.UUID) error {
	return fiber.NewError(fiber.StatusNotImplemented, "voice ingestion is not deployed")
}
