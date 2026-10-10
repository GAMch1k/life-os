// Package api contains the Banking service's HTTP transport adapters.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/banking/internal/api/generated"
	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// Handler serves the Banking API from the banking service.
type Handler struct {
	service  *banking.Service
	timeZone string
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves service; timeZone names the owner's timezone (the sync
// times are in it).
func NewHandler(service *banking.Service, timeZone string) *Handler {
	return &Handler{service: service, timeZone: timeZone}
}

// Register mounts the Banking API on router. Every write must say who makes
// it in X-Life-OS-Actor.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}

func (h *Handler) ListBankConnections(c fiber.Ctx) error {
	rows, err := h.service.Connections(c.Context())
	if err != nil {
		return internal(err)
	}
	opts := h.service.Options()
	list := generated.BankConnectionList{
		Configured: h.service.Configured(), Items: make([]generated.BankConnection, 0, len(rows)),
		Schedule: generated.BankSchedule{
			Times: opts.Schedule.Times(), TimeZone: h.timeZone, UnattendedPerDay: opts.MaxUnattended,
			ManualSyncMinutes: int(opts.ManualInterval / time.Minute),
		},
	}
	if problem := h.service.SetupProblem(); problem != "" {
		list.SetupProblem = &problem
	}
	for _, row := range rows {
		list.Items = append(list.Items, h.view(row))
	}
	return c.JSON(list)
}

func (h *Handler) AuthorizeBankConnection(c fiber.Ctx) error {
	var body generated.BankAuthorizationRequest
	if raw := c.Body(); len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return badRequest("the body must be a JSON object with optional institution and country")
		}
	}
	institution, country := "", ""
	if body.Institution != nil {
		institution = strings.TrimSpace(*body.Institution)
		if institution == "" || len(institution) > 200 {
			return badRequest("institution must be a bank name of 1-200 characters")
		}
	}
	if body.Country != nil {
		country = strings.ToUpper(strings.TrimSpace(*body.Country))
		if len(country) != 2 || strings.Trim(country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return badRequest("country must be a two-letter country code such as DE")
		}
	}
	url, err := h.service.Authorize(c.Context(), institution, country)
	if err != nil {
		return serviceError(err)
	}
	return c.JSON(generated.BankAuthorization{Url: url})
}

func (h *Handler) ConnectBankConnection(c fiber.Ctx) error {
	var body generated.BankConnectRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest("the request must be a JSON object with code and state")
	}
	if body.Code == "" || body.State == "" || len(body.Code) > 2048 || len(body.State) > 256 {
		return badRequest("code and state must be the values the bank returned to the callback")
	}
	row, err := h.service.Connect(traced(c), body.Code, body.State, psuFrom(c))
	if err != nil {
		return serviceError(err)
	}
	return c.JSON(h.view(row))
}

func (h *Handler) SyncBankConnection(c fiber.Ctx, connectionID generated.ConnectionId) error {
	row, err := h.service.SyncNow(traced(c), pgtype.UUID{Bytes: connectionID, Valid: true}, psuFrom(c))
	var throttled *banking.ThrottledError
	if errors.As(err, &throttled) {
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(int(throttled.RetryAfter.Seconds()+0.999)))
	}
	if err != nil {
		return serviceError(err)
	}
	return c.JSON(h.view(row))
}

func (h *Handler) DisconnectBankConnection(c fiber.Ctx, connectionID generated.ConnectionId) error {
	if err := h.service.Disconnect(traced(c), pgtype.UUID{Bytes: connectionID, Valid: true}, psuFrom(c)); err != nil {
		return serviceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// view renders a connection without its session or account uids.
func (h *Handler) view(row store.BankConnection) generated.BankConnection {
	accounts := banking.Accounts(row)
	views := make([]generated.BankAccount, 0, len(accounts))
	for _, account := range accounts {
		view := generated.BankAccount{Id: account.Key, Name: account.Name, Currency: account.Currency, Kind: generated.BankAccountKind(account.Kind())}
		if account.Product != "" {
			product := account.Product
			view.Product = &product
		}
		if account.IBANMasked != "" {
			masked := account.IBANMasked
			view.IbanMasked = &masked
		}
		views = append(views, view)
	}
	now := h.service.Now()
	opts := h.service.Options()
	result := generated.BankConnection{
		Id: row.ID.Bytes, Provider: row.Provider, Institution: row.Institution, Country: row.Country,
		Status: generated.BankConnectionStatus(row.Status), ConsentValidUntil: optional(row.ConsentValidUntil), Accounts: views,
		LastSyncAt: optional(row.LastSyncAt), LastSuccessAt: optional(row.LastSuccessAt), NextSyncAt: optional(row.NextSyncAt),
		ConsecutiveFailures: int(row.ConsecutiveFailures), LastError: row.LastError,
		WaitingForFinance: len(row.PendingImport) > 0, ConnectedAt: optional(row.ConnectedAt), DisconnectedAt: optional(row.DisconnectedAt),
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if row.UnattendedReadsDate.Valid && row.UnattendedReadsDate.Time.Equal(opts.Schedule.Day(now)) {
		result.UnattendedReadsToday = int(row.UnattendedReadsCount)
	}
	if row.LastManualSyncAt.Valid {
		if available := row.LastManualSyncAt.Time.Add(opts.ManualInterval); available.After(now) {
			at := available.UTC()
			result.ManualSyncAvailableAt = &at
		}
	}
	if len(row.LastCounts) > 0 {
		var counts generated.BankSyncCounts
		if json.Unmarshal(row.LastCounts, &counts) == nil {
			result.LastCounts = &counts
		}
	}
	if row.Status != banking.StatusPending && row.Status != banking.StatusConnected {
		result.NextSyncAt = nil
	}
	return result
}

// psuFrom is the owner's presence for an owner-present read: only for the
// owner's own requests (actor user), with the address the gateway saw
// (X-Real-IP) and the browser's user agent.
func psuFrom(c fiber.Ctx) *enablebanking.PSU {
	if activity.FromRequest(c).Actor.Kind != "user" {
		return nil
	}
	ip := strings.TrimSpace(c.Get("X-Real-IP"))
	if net.ParseIP(ip) == nil {
		return nil
	}
	agent := strings.TrimSpace(c.Get(fiber.HeaderUserAgent))
	if agent == "" || len(agent) > 512 {
		return nil
	}
	language := strings.TrimSpace(c.Get(fiber.HeaderAcceptLanguage))
	if len(language) > 128 {
		language = ""
	}
	return &enablebanking.PSU{IPAddress: ip, UserAgent: agent, AcceptLanguage: language}
}

// traced carries the request's actor and workflow to the activity events
// the service records.
func traced(c fiber.Ctx) context.Context {
	return activity.WithTrace(c.Context(), activity.FromRequest(c))
}

func serviceError(err error) error {
	var throttled *banking.ThrottledError
	var syncError *banking.SyncError
	switch {
	case errors.Is(err, banking.ErrNotConfigured):
		return httpserver.NewError(fiber.StatusServiceUnavailable, "banking_not_configured", err.Error())
	case errors.Is(err, banking.ErrNotFound):
		return httpserver.NewError(fiber.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, banking.ErrInactive):
		return httpserver.NewError(fiber.StatusConflict, "connection_inactive", err.Error())
	case errors.Is(err, banking.ErrSyncInProgress):
		return httpserver.NewError(fiber.StatusConflict, "sync_in_progress", err.Error())
	case errors.Is(err, banking.ErrInvalidState):
		return httpserver.NewError(fiber.StatusBadRequest, "invalid_state", err.Error())
	case errors.Is(err, banking.ErrCodeRejected):
		return httpserver.NewError(fiber.StatusBadRequest, "bank_code_rejected", err.Error())
	case errors.Is(err, banking.ErrNoAccounts):
		return httpserver.NewError(fiber.StatusUnprocessableEntity, "no_accounts", err.Error())
	case errors.Is(err, banking.ErrUnknownBank):
		return httpserver.NewError(fiber.StatusUnprocessableEntity, "unknown_bank", err.Error())
	case errors.Is(err, banking.ErrDailyLimit):
		return httpserver.NewError(fiber.StatusTooManyRequests, "daily_limit", err.Error())
	case errors.As(err, &throttled):
		return httpserver.NewError(fiber.StatusTooManyRequests, "sync_throttled", err.Error())
	case errors.As(err, &syncError):
		code := map[string]string{
			"bank": "bank_unavailable", "expired": "bank_expired", "refused": "bank_refused",
			"finance": "finance_unavailable", "finance_refused": "finance_refused",
		}[syncError.Kind]
		if code == "" {
			code = "bank_unavailable"
		}
		return httpserver.NewError(fiber.StatusBadGateway, code, syncError.Message)
	}
	return internal(err)
}

func badRequest(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "bad_request", message)
}

func internal(err error) error {
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "banking request failed")
}

func optional(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	value := t.Time.UTC()
	return &value
}
