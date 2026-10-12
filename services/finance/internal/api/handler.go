// Package api contains the finance HTTP transport adapters.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// Handler serves the Finance API.
type Handler struct {
	pool            *pgxpool.Pool
	queries         *store.Queries
	activity        *activity.Emitter
	location        *time.Location
	defaultCurrency string
	onBudgetChange  func()
	onMoneyChange   func()
	now             func() time.Time
}

var _ generated.ServerInterface = (*Handler)(nil)

// Options configure a Handler.
type Options struct {
	// Location is the owner's timezone; a manual transaction without a date
	// is dated today there. Nil means UTC.
	Location *time.Location
	// DefaultCurrency is the built-in Cash account's currency when Finance
	// creates it. Empty means EUR.
	DefaultCurrency string
	// Activity records changes; nil records nothing.
	Activity *activity.Emitter
	// BudgetChanged is called after money or a budget plan changed, so the
	// budget alerts check soon; nil does nothing.
	BudgetChanged func()
	// MoneyChanged is called after transactions, accounts, subscriptions,
	// or the alert settings changed, so subscription detection and the
	// money alerts run soon; nil does nothing.
	MoneyChanged func()
}

// NewHandler serves the Finance API from pool.
func NewHandler(pool *pgxpool.Pool, options Options) *Handler {
	h := &Handler{pool: pool, activity: options.Activity, location: options.Location, defaultCurrency: options.DefaultCurrency,
		onBudgetChange: options.BudgetChanged, onMoneyChange: options.MoneyChanged, now: time.Now}
	if pool != nil {
		h.queries = store.New(pool)
	}
	if h.location == nil {
		h.location = time.UTC
	}
	if h.defaultCurrency == "" {
		h.defaultCurrency = "EUR"
	}
	return h
}

// inTx runs fn in one database transaction.
func (h *Handler) inTx(ctx context.Context, fn func(*store.Queries) error) error {
	return pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error { return fn(h.queries.WithTx(tx)) })
}

// today is the owner's current date.
func (h *Handler) today() pgtype.Date {
	return pgtype.Date{Time: h.ownerDay(h.now()), Valid: true}
}

// ownerDay is the owner's date at an instant, as midnight UTC.
func (h *Handler) ownerDay(at time.Time) time.Time {
	local := at.In(h.location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// Errors.

func apiError(status int, code, message string) error {
	return httpserver.NewError(status, code, message)
}
func invalid(message string) error {
	return apiError(fiber.StatusBadRequest, "invalid_request", message)
}
func notFound(what string) error {
	return apiError(fiber.StatusNotFound, "not_found", what+" not found")
}

func internal(err error) error {
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "Finance could not complete the request")
}

// passThrough keeps an API error and wraps anything else as internal.
func passThrough(err error) error {
	var appError *httpserver.Error
	if errors.As(err, &appError) {
		return err
	}
	return internal(err)
}

func uniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}

// decodeObject reads a JSON object body into target and returns which
// properties it names, so a PATCH can tell an absent property from null.
func decodeObject(raw []byte, target any) (map[string]json.RawMessage, error) {
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(raw, &properties); err != nil || properties == nil {
		return nil, errors.New("the body must be a JSON object")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, fmt.Errorf("invalid field: %s", fieldError(err))
	}
	return properties, nil
}

func fieldError(err error) string {
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) && typeError.Field != "" {
		return typeError.Field
	}
	return err.Error()
}

func isNull(properties map[string]json.RawMessage, name string) bool {
	value, ok := properties[name]
	return ok && strings.TrimSpace(string(value)) == "null"
}

// cleanText trims a text field: empty means none. It fails when the text is
// longer than limit characters.
func cleanText(name string, value *string, limit int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > limit {
		return nil, invalid(fmt.Sprintf("%s must be at most %d characters", name, limit))
	}
	return &trimmed, nil
}

func idempotency(c fiber.Ctx) (*string, *string) {
	key := c.Get("Idempotency-Key")
	if key == "" {
		return nil, nil
	}
	sum := sha256.Sum256(c.Body())
	hash := fmt.Sprintf("%x", sum[:])
	return &key, &hash
}

// Conversions.

func uuid(value pgtype.UUID) types.UUID   { return types.UUID(value.Bytes) }
func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }

func nullableUUID(value pgtype.UUID) *types.UUID {
	if !value.Valid {
		return nil
	}
	result := uuid(value)
	return &result
}

func optionalUUID(value *types.UUID) pgtype.UUID {
	if value == nil {
		return pgtype.UUID{}
	}
	return pgUUID(*value)
}

func pgDate(value types.Date) pgtype.Date {
	return pgtype.Date{Time: time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func optionalDate(value *types.Date) pgtype.Date {
	if value == nil {
		return pgtype.Date{}
	}
	return pgDate(*value)
}

func nullableDate(value pgtype.Date) *types.Date {
	if !value.Valid {
		return nil
	}
	return &types.Date{Time: value.Time}
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func money(amount int64, currency string) generated.Money {
	return generated.Money{Amount: amount, Currency: currency}
}

func ruleSet(rows []store.CategoryRule) []rules.Rule {
	result := make([]rules.Rule, 0, len(rows))
	for _, row := range rows {
		result = append(result, rules.Rule{ID: row.ID.Bytes, CategoryID: row.CategoryID.Bytes, Field: row.MatchField, Operator: row.MatchOperator, Text: row.MatchText})
	}
	return result
}

func pointer[T any](value T) *T { return &value }
