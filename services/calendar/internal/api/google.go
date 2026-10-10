package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// WithGoogle serves the Google Calendar connection from service. Without it
// Google reads as not configured and its writes are refused.
func (h *Handler) WithGoogle(service *google.Service) *Handler {
	h.google = service
	return h
}

func (h *Handler) GetGoogleConnection(c fiber.Ctx) error {
	view := generated.GoogleConnection{
		Configured: h.google.Configured(), Status: generated.GoogleConnectionStatus(google.StatusNotConnected),
		Scopes: []string{}, MissingScopes: []string{},
	}
	if h.google == nil {
		return c.JSON(view)
	}
	row, found, err := h.google.Connection(c.Context())
	if err != nil {
		return internal(err)
	}
	if found {
		view = googleConnection(row, view.Configured)
	}
	return c.JSON(view)
}

func (h *Handler) AuthorizeGoogle(c fiber.Ctx) error {
	if !h.google.Configured() {
		return googleError(google.ErrNotConfigured)
	}
	url, err := h.google.Authorize(c.Context())
	if err != nil {
		return googleError(err)
	}
	return c.JSON(generated.GoogleAuthorization{Url: url})
}

func (h *Handler) ConnectGoogle(c fiber.Ctx) error {
	var body generated.ConnectGoogleJSONRequestBody
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest("the request must be a JSON object with code and state")
	}
	if body.Code == "" || body.State == "" || len(body.Code) > 2048 || len(body.State) > 256 {
		return badRequest("code and state must be the values Google returned to the callback")
	}
	if !h.google.Configured() {
		return googleError(google.ErrNotConfigured)
	}
	row, err := h.google.Connect(h.traced(c), body.Code, body.State)
	if err != nil {
		return googleError(err)
	}
	return c.JSON(googleConnection(row, true))
}

func (h *Handler) DisconnectGoogle(c fiber.Ctx) error {
	if h.google != nil {
		if _, err := h.google.Disconnect(h.traced(c)); err != nil {
			return internal(err)
		}
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) ListGoogleCalendars(c fiber.Ctx) error {
	if h.google == nil {
		return c.JSON(generated.GoogleCalendarList{Items: []generated.GoogleCalendar{}})
	}
	rows, err := h.google.Calendars(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(googleCalendarList(rows))
}

func (h *Handler) RefreshGoogleCalendars(c fiber.Ctx) error {
	if !h.google.Configured() {
		return googleError(google.ErrNotConfigured)
	}
	rows, err := h.google.RefreshCalendars(h.traced(c))
	if err != nil {
		return googleError(err)
	}
	return c.JSON(googleCalendarList(rows))
}

func (h *Handler) UpdateGoogleCalendar(c fiber.Ctx, calendarID types.UUID) error {
	var body struct {
		SyncEnabled *bool `json:"syncEnabled"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.SyncEnabled == nil {
		return badRequest("the update must set syncEnabled to true or false")
	}
	if h.google == nil {
		return googleCalendarNotFound()
	}
	row, err := h.google.SetCalendarSync(c.Context(), calendarID, *body.SyncEnabled)
	if err != nil {
		return googleError(err)
	}
	return c.JSON(googleCalendar(row))
}

// traced carries the request's actor and workflow to the Activity events
// the Google service records.
func (h *Handler) traced(c fiber.Ctx) context.Context {
	return activity.WithTrace(c.Context(), activity.FromRequest(c))
}

func googleConnection(row store.GoogleConnection, configured bool) generated.GoogleConnection {
	email := row.AccountEmail
	scopes := row.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return generated.GoogleConnection{
		Configured: configured, Status: generated.GoogleConnectionStatus(row.Status), AccountEmail: &email,
		Scopes: scopes, MissingScopes: google.MissingScopes(scopes), ConnectedAt: nullableTime(row.ConnectedAt), LastError: row.LastError,
	}
}

func googleCalendarList(rows []store.GoogleCalendar) generated.GoogleCalendarList {
	items := make([]generated.GoogleCalendar, 0, len(rows))
	for _, row := range rows {
		items = append(items, googleCalendar(row))
	}
	return generated.GoogleCalendarList{Items: items}
}

func googleCalendar(row store.GoogleCalendar) generated.GoogleCalendar {
	return generated.GoogleCalendar{
		Id: uuid(row.ID), GoogleCalendarId: row.GoogleCalendarID, Summary: row.Summary, Color: row.Color,
		AccessRole: generated.GoogleCalendarAccessRole(row.AccessRole), Primary: row.IsPrimary, SyncEnabled: row.SyncEnabled,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func googleCalendarNotFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "Google calendar not found")
}

// googleError turns a Google service failure into a response whose message
// says what to do. Google's own messages never contain tokens or secrets.
func googleError(err error) error {
	var oauth *google.OAuthError
	var apiErr *google.APIError
	var unavailable *google.UnavailableError
	switch {
	case errors.Is(err, google.ErrNotConfigured):
		return httpserver.NewError(fiber.StatusServiceUnavailable, "google_not_configured", err.Error())
	case errors.Is(err, google.ErrNotConnected):
		return httpserver.NewError(fiber.StatusConflict, "google_not_connected", err.Error())
	case errors.Is(err, google.ErrExpired):
		return httpserver.WrapError(err, fiber.StatusConflict, "google_expired", google.ErrExpired.Error())
	case errors.Is(err, google.ErrInvalidState):
		return httpserver.NewError(fiber.StatusBadRequest, "invalid_state", err.Error())
	case errors.Is(err, google.ErrCodeRejected):
		return httpserver.WrapError(err, fiber.StatusBadRequest, "google_code_rejected", err.Error())
	case errors.Is(err, google.ErrScopeMissing):
		return httpserver.NewError(fiber.StatusForbidden, "google_scope_missing", err.Error())
	case errors.Is(err, google.ErrCalendarNotFound):
		return googleCalendarNotFound()
	case errors.Is(err, google.ErrNoRefreshToken), errors.Is(err, google.ErrNoPrimary):
		return httpserver.NewError(fiber.StatusBadGateway, "google_rejected", err.Error())
	case errors.Is(err, google.ErrUnreadableToken):
		return httpserver.WrapError(err, fiber.StatusConflict, "google_expired",
			"the stored Google token cannot be decrypted with LIFE_OS_GOOGLE_TOKEN_KEY (was the key changed?); reconnect Google in Calendar")
	case errors.As(err, &oauth), errors.As(err, &apiErr):
		return httpserver.WrapError(err, fiber.StatusBadGateway, "google_rejected", err.Error())
	case errors.As(err, &unavailable):
		return httpserver.WrapError(err, fiber.StatusBadGateway, "google_unavailable", "Google could not be reached; try again later")
	}
	return internal(err)
}
