// Package httpserver provides the small, shared HTTP runtime used by Life OS
// services. It keeps Fiber's API visible while installing consistent
// observability, error, health, and lifecycle behavior.
package httpserver

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gofiber/fiber/v3"
)

// Error is an HTTP-safe application error. Message is returned to the caller;
// Cause is retained for server-side inspection and logging only.
type Error struct {
	Status  int
	Code    string
	Message string
	Cause   error
}

// NewError constructs an HTTP-safe error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WrapError constructs an HTTP-safe error which wraps an internal cause.
func WrapError(cause error, status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message, Cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Cause)
	}
	return e.Code
}

// Unwrap exposes the internal cause to errors.Is and errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type errorEnvelope struct {
	Error     errorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func errorHandler(c fiber.Ctx, err error) error {
	status, code, message := publicError(err)
	return c.Status(status).JSON(errorEnvelope{
		Error: errorBody{
			Code:    code,
			Message: message,
		},
		RequestID: RequestID(c),
	})
}

func publicError(err error) (status int, code, message string) {
	status = fiber.StatusInternalServerError
	code = "internal_error"
	message = "Internal Server Error"

	var appError *Error
	if errors.As(err, &appError) && appError != nil {
		if appError.Status >= 400 && appError.Status <= 599 {
			status = appError.Status
		}
		if appError.Code != "" {
			code = appError.Code
		}
		if appError.Message != "" {
			message = appError.Message
		}
		return status, code, message
	}

	var fiberError *fiber.Error
	if errors.As(err, &fiberError) && fiberError != nil {
		if fiberError.Code >= 400 && fiberError.Code <= 599 {
			status = fiberError.Code
		}
		code = statusCode(status)
		message = http.StatusText(status)
	}

	return status, code, message
}

func statusCode(status int) string {
	switch status {
	case fiber.StatusBadRequest:
		return "bad_request"
	case fiber.StatusUnauthorized:
		return "unauthorized"
	case fiber.StatusForbidden:
		return "forbidden"
	case fiber.StatusNotFound:
		return "not_found"
	case fiber.StatusConflict:
		return "conflict"
	case fiber.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case fiber.StatusTooManyRequests:
		return "too_many_requests"
	case fiber.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		return "http_error"
	}
}
