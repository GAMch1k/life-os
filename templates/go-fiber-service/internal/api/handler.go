// Package api contains the service's HTTP transport adapters.
package api

import (
	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/templates/go-fiber-service/internal/api/generated"
)

// Handler implements generated transport interfaces and translates transport
// data at the boundary. Domain services are deliberately not generated here.
type Handler struct{}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) GetPing(c fiber.Ctx) error {
	return c.JSON(generated.PingResponse{Message: "pong"})
}

func (h *Handler) GetEcho(c fiber.Ctx, params generated.GetEchoParams) error {
	return c.JSON(generated.EchoResponse{Message: params.Message})
}
