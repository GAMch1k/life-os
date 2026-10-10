// Package api contains the Google Calendar integration's HTTP transport
// adapters.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// Handler serves the integration's API from the Google service.
type Handler struct {
	google *google.Service
	now    func() time.Time
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves service.
func NewHandler(service *google.Service) *Handler {
	return &Handler{google: service, now: time.Now}
}

// Register mounts the API on router. Every write must say who makes it in
// X-Life-OS-Actor; activity.RequireActor rejects it otherwise.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}

func (h *Handler) GetGoogleConnection(c fiber.Ctx) error {
	view := generated.GoogleConnection{
		Configured: h.google.Configured(), Status: generated.GoogleConnectionStatus(google.StatusNotConnected),
		Scopes: []string{}, MissingScopes: []string{}, ExportEnabled: true,
	}
	row, found, err := h.google.Connection(c.Context())
	if err != nil {
		return internal(err)
	}
	if found {
		if view, err = h.connectionView(c.Context(), row, view.Configured); err != nil {
			return internal(err)
		}
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
	row, err := h.google.Connect(traced(c), body.Code, body.State)
	if err != nil {
		return googleError(err)
	}
	view, err := h.connectionView(c.Context(), row, true)
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

func (h *Handler) DisconnectGoogle(c fiber.Ctx) error {
	if _, err := h.google.Disconnect(traced(c)); err != nil {
		return internal(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// UpdateGoogleConnection changes the connection's settings: exporting Life
// OS events on or off.
func (h *Handler) UpdateGoogleConnection(c fiber.Ctx) error {
	var body struct {
		ExportEnabled *bool `json:"exportEnabled"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.ExportEnabled == nil {
		return badRequest("the update must set exportEnabled to true or false")
	}
	row, err := h.google.SetExportEnabled(traced(c), *body.ExportEnabled)
	if err != nil {
		return googleError(err)
	}
	view, err := h.connectionView(c.Context(), row, h.google.Configured())
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

func (h *Handler) ListGoogleCalendars(c fiber.Ctx) error {
	rows, err := h.google.Calendars(c.Context())
	if err != nil {
		return internal(err)
	}
	return h.respondCalendars(c, rows)
}

func (h *Handler) RefreshGoogleCalendars(c fiber.Ctx) error {
	if !h.google.Configured() {
		return googleError(google.ErrNotConfigured)
	}
	rows, err := h.google.RefreshCalendars(traced(c))
	if err != nil {
		return googleError(err)
	}
	return h.respondCalendars(c, rows)
}

func (h *Handler) UpdateGoogleCalendar(c fiber.Ctx, calendarID types.UUID) error {
	var body struct {
		SyncEnabled *bool `json:"syncEnabled"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.SyncEnabled == nil {
		return badRequest("the update must set syncEnabled to true or false")
	}
	row, err := h.google.SetCalendarSync(traced(c), calendarID, *body.SyncEnabled)
	if err != nil {
		return googleError(err)
	}
	return h.respondCalendar(c, row)
}

// SyncGoogleCalendar syncs a calendar now and returns it with its outcome.
func (h *Handler) SyncGoogleCalendar(c fiber.Ctx, calendarID types.UUID, params generated.SyncGoogleCalendarParams) error {
	row, err := h.google.SyncCalendar(traced(c), calendarID, params.Full != nil && *params.Full)
	if err != nil {
		return googleError(err)
	}
	return h.respondCalendar(c, row)
}

// maxExportIDs bounds the events one export lookup names.
const maxExportIDs = 200

// ListEventExports says where the Life OS events asked for are in Google.
func (h *Handler) ListEventExports(c fiber.Ctx, params generated.ListEventExportsParams) error {
	if len(params.EventId) == 0 || len(params.EventId) > maxExportIDs {
		return badRequest("give 1 to 200 eventId parameters")
	}
	result := generated.EventExportList{Items: []generated.EventExport{}}
	connection, found, err := h.google.Connection(c.Context())
	if err != nil {
		return internal(err)
	}
	if !found {
		return c.JSON(result)
	}
	ids := make([]pgtype.UUID, 0, len(params.EventId))
	for _, id := range params.EventId {
		ids = append(ids, pgtype.UUID{Bytes: id, Valid: true})
	}
	exports, err := h.google.Exports(c.Context(), ids)
	if err != nil {
		return internal(err)
	}
	name := google.LifeOSCalendarName
	var calendarID *types.UUID
	if row, listed, err := h.google.LifeOSCalendar(c.Context(), connection); err != nil {
		return internal(err)
	} else if listed {
		id := types.UUID(row.ID.Bytes)
		name, calendarID = row.Summary, &id
	}
	for _, export := range exports {
		if export.Deleted || export.ConnectionID != connection.ID {
			continue
		}
		state := generated.EventExportState(export.State)
		if export.GoogleEventID == nil && state == generated.EventExportStateSynced {
			continue // not in Google (cancelled, or its copy deleted there)
		}
		if !connection.ExportEnabled && export.GoogleEventID == nil {
			continue
		}
		item := generated.EventExport{EventId: types.UUID(export.EventID.Bytes), CalendarId: calendarID, CalendarName: name, LifeOs: true, State: state}
		if state == generated.EventExportStateFailed {
			item.Error = export.LastError
		}
		result.Items = append(result.Items, item)
	}
	return c.JSON(result)
}

func (h *Handler) respondCalendar(c fiber.Ctx, row store.GoogleCalendar) error {
	lifeOS, err := h.lifeOSCalendarID(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(h.calendarView(row, lifeOS))
}

func (h *Handler) respondCalendars(c fiber.Ctx, rows []store.GoogleCalendar) error {
	lifeOS, err := h.lifeOSCalendarID(c.Context())
	if err != nil {
		return internal(err)
	}
	items := make([]generated.GoogleCalendar, 0, len(rows))
	for _, row := range rows {
		items = append(items, h.calendarView(row, lifeOS))
	}
	return c.JSON(generated.GoogleCalendarList{Items: items})
}

// lifeOSCalendarID is the Life OS calendar's Google id ("" when none).
func (h *Handler) lifeOSCalendarID(ctx context.Context) (string, error) {
	row, found, err := h.google.Connection(ctx)
	if err != nil || !found || row.LifeosCalendarID == nil {
		return "", err
	}
	return *row.LifeosCalendarID, nil
}

// traced carries the request's actor and workflow to the Activity events
// the service records.
func traced(c fiber.Ctx) context.Context {
	return activity.WithTrace(c.Context(), activity.FromRequest(c))
}

// connectionView is the connection with its export, edit, and change feed
// state and its Life OS calendar.
func (h *Handler) connectionView(ctx context.Context, row store.GoogleConnection, configured bool) (generated.GoogleConnection, error) {
	email := row.AccountEmail
	scopes := row.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	view := generated.GoogleConnection{
		Configured: configured, Status: generated.GoogleConnectionStatus(row.Status), AccountEmail: &email,
		Scopes: scopes, MissingScopes: google.MissingScopes(scopes), ConnectedAt: nullableTime(row.ConnectedAt), LastError: row.LastError,
		ExportEnabled: row.ExportEnabled,
	}
	summary, err := h.google.ExportSummary(ctx, row)
	if err != nil {
		return view, err
	}
	view.Export = generated.GoogleExportState{Pending: int(summary.Pending), Failed: int(summary.Failed), LastError: summary.LastError, LastExportedAt: nullableTime(summary.LastExportedAt)}
	if row.LifeosCalendarID == nil && row.LifeosCalendarNote != nil && row.ExportEnabled {
		// The Life OS calendar could not be created: nothing is exported.
		view.Export.LastError = row.LifeosCalendarNote
	}
	if row.LifeosCalendarID != nil {
		lifeOS := generated.LifeOsCalendar{GoogleCalendarId: *row.LifeosCalendarID, Summary: google.LifeOSCalendarName, CreatedAt: nullableTime(row.LifeosCalendarCreatedAt), Note: row.LifeosCalendarNote}
		listed, found, err := h.google.LifeOSCalendar(ctx, row)
		if err != nil {
			return view, err
		}
		if found {
			id := types.UUID(listed.ID.Bytes)
			lifeOS.CalendarId, lifeOS.Summary = &id, listed.Summary
		}
		view.LifeOsCalendar = &lifeOS
	}
	pushes, err := h.google.PushSummary(ctx, row)
	if err != nil {
		return view, err
	}
	view.Changes = &generated.ChangeFeedState{
		LastReadAt: nullableTime(row.FeedReadAt), LastError: row.FeedError, LastErrorAt: nullableTime(row.FeedErrorAt),
		PendingEdits: int(pushes.Pending), FailedEdits: int(pushes.Failed), LastEditError: pushes.LastError,
	}
	return view, nil
}

// calendarView renders a calendar with its sync state; lifeOS is the Life
// OS calendar's Google id.
func (h *Handler) calendarView(row store.GoogleCalendar, lifeOS string) generated.GoogleCalendar {
	sync := generated.GoogleCalendarSync{
		State: generated.GoogleCalendarSyncStateOff, ConsecutiveFailures: int(row.SyncFailures), LastAttemptAt: nullableTime(row.LastSyncAttemptAt),
		LastSyncedAt: nullableTime(row.LastSyncedAt), LastError: row.LastSyncError,
		Syncing: row.SyncEnabled && row.SyncingUntil.Valid && row.SyncingUntil.Time.After(h.now()),
	}
	if row.SyncEnabled {
		sync.NextSyncAt = nullableTime(row.NextSyncAt)
		switch {
		case row.SyncFailures > 0:
			sync.State = generated.GoogleCalendarSyncStateFailing
		case row.LastSyncedAt.Valid:
			sync.State = generated.GoogleCalendarSyncStateSynced
		default:
			sync.State = generated.GoogleCalendarSyncStatePending
		}
		if row.SourceID.Valid {
			id := types.UUID(row.SourceID.Bytes)
			sync.SourceId = &id
		}
	}
	if row.LastSyncCounts != nil && row.SyncEnabled {
		var counts sources.Counts
		if err := json.Unmarshal(row.LastSyncCounts, &counts); err == nil {
			skipped := counts.Skipped
			sync.LastCounts = &generated.SyncCounts{
				FeedEvents: counts.FeedEvents, Imported: counts.Imported, Inserted: counts.Inserted, Updated: counts.Updated,
				Removed: counts.Removed, Restored: counts.Restored, Unchanged: counts.Unchanged, Skipped: &skipped,
			}
		}
	}
	return generated.GoogleCalendar{
		Id: types.UUID(row.ID.Bytes), GoogleCalendarId: row.GoogleCalendarID, Summary: row.Summary, Color: row.Color,
		AccessRole: generated.GoogleCalendarAccessRole(row.AccessRole), Primary: row.IsPrimary, SyncEnabled: row.SyncEnabled,
		LifeOs: lifeOS != "" && row.GoogleCalendarID == lifeOS, Sync: sync, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	instant := value.Time.UTC()
	return &instant
}

func badRequest(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "invalid_request", message)
}

func internal(err error) error {
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "the Google Calendar integration failed")
}

func calendarNotFound() error {
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
		return calendarNotFound()
	case errors.Is(err, google.ErrSyncDisabled):
		return httpserver.NewError(fiber.StatusConflict, "google_sync_disabled", err.Error())
	case errors.Is(err, google.ErrSyncInProgress):
		return httpserver.NewError(fiber.StatusConflict, "sync_in_progress", err.Error())
	case errors.Is(err, google.ErrSyncDeferred):
		return httpserver.NewError(fiber.StatusConflict, "sync_in_progress", err.Error())
	case errors.Is(err, google.ErrSyncSuperseded):
		return httpserver.NewError(fiber.StatusConflict, "conflict", err.Error())
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
