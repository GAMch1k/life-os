// Package api contains the service's HTTP transport adapters.
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/inbox/internal/api/generated"
)

// Handler implements the generated transport interface. Domain behavior is
// introduced with the inbox service implementation, not with the contract.
type Handler struct{}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ListInboxItems(c fiber.Ctx, params generated.ListInboxItemsParams) error {
	return notImplemented()
}

func (h *Handler) CreateInboxItem(c fiber.Ctx) error { return notImplemented() }

func (h *Handler) GetInboxItem(c fiber.Ctx, itemID types.UUID) error { return notImplemented() }

func (h *Handler) DiscardInboxItem(c fiber.Ctx, itemID types.UUID) error { return notImplemented() }

func (h *Handler) ResolveInboxItem(c fiber.Ctx, itemID types.UUID) error { return notImplemented() }

func notImplemented() error {
	return fiber.NewError(fiber.StatusNotImplemented, "inbox endpoint is not implemented yet")
}
