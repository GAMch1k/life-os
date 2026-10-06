// Package api contains the service's HTTP transport adapters.
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
)

// Handler implements the generated transport interface. Domain behavior is
// introduced with the tasks service implementation, not with the contract.
type Handler struct{}

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ListTasks(c fiber.Ctx, params generated.ListTasksParams) error {
	return notImplemented()
}

func (h *Handler) CreateTask(c fiber.Ctx) error { return notImplemented() }

func (h *Handler) GetTask(c fiber.Ctx, taskID types.UUID) error { return notImplemented() }

func (h *Handler) UpdateTask(c fiber.Ctx, taskID types.UUID) error { return notImplemented() }

func (h *Handler) CompleteTask(c fiber.Ctx, taskID types.UUID) error { return notImplemented() }

func notImplemented() error {
	return fiber.NewError(fiber.StatusNotImplemented, "tasks endpoint is not implemented yet")
}
