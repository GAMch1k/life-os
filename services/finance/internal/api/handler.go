// Package api contains the service's HTTP transport adapters.
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

// Handler implements the generated transport interface. Domain behavior is
// introduced with the finance service implementation, not with the contract.
type Handler struct{}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ListAccounts(c fiber.Ctx) error { return notImplemented() }

func (h *Handler) ListCategories(c fiber.Ctx) error { return notImplemented() }

func (h *Handler) UpdateCategory(c fiber.Ctx, categoryID types.UUID) error {
	return notImplemented()
}

func (h *Handler) CreateCsvImport(c fiber.Ctx) error { return notImplemented() }

func (h *Handler) GetMonthlySummary(c fiber.Ctx, params generated.GetMonthlySummaryParams) error {
	return notImplemented()
}

func (h *Handler) ListTransactions(c fiber.Ctx, params generated.ListTransactionsParams) error {
	return notImplemented()
}

func (h *Handler) CreateManualTransaction(c fiber.Ctx) error { return notImplemented() }

func notImplemented() error {
	return fiber.NewError(fiber.StatusNotImplemented, "finance endpoint is not implemented yet")
}
