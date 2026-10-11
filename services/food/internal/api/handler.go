// Package api contains the Food HTTP transport adapters.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Input limits, in characters (the contract's maxLength).
const (
	nameLimit      = 200
	amountLimit    = 50
	noteLimit      = 1000
	sourceRefLimit = 200
	storeNameLimit = 100
	keywordLimit   = 100
	// batchLimit is how many items one add request may carry.
	batchLimit = 50
	// sortOrderLimit bounds a store's sortOrder either way.
	sortOrderLimit = 1_000_000
)

// Handler serves the Food API.
type Handler struct {
	pool     *pgxpool.Pool
	queries  *store.Queries
	activity *activity.Emitter
	// location is the owner's timezone: expiringWithinDays counts from
	// today there.
	location *time.Location
	now      func() time.Time
	// lastPrune is when expired Idempotency-Keys were last pruned (Unix
	// seconds; see pruneKeys).
	lastPrune atomic.Int64
	// notifications reports the expiry reminders' health (see
	// WithNotifications).
	notifications NotificationStatus
}

var _ generated.ServerInterface = (*Handler)(nil)

// Options configure a Handler.
type Options struct {
	// Location is the owner's timezone; nil means UTC.
	Location *time.Location
	// Activity records changes; nil records nothing.
	Activity *activity.Emitter
}

// NewHandler serves the Food API from pool.
func NewHandler(pool *pgxpool.Pool, options Options) *Handler {
	h := &Handler{pool: pool, activity: options.Activity, location: options.Location, now: time.Now}
	if pool != nil {
		h.queries = store.New(pool)
	}
	if h.location == nil {
		h.location = time.UTC
	}
	return h
}

// today is the owner's current date, as a UTC midnight.
func (h *Handler) today() time.Time {
	now := h.now().In(h.location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
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
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "Food could not complete the request")
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

// Request bodies.

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

// cleanName checks a name and returns it as written (without surrounding
// whitespace) and normalised.
func cleanName(field, raw string, limit int) (name, normalized string, err error) {
	name = strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > limit {
		return "", "", invalid(fmt.Sprintf("%s must be 1 to %d characters", field, limit))
	}
	normalized = domain.Normalize(name)
	if normalized == "" {
		return "", "", invalid(field + " must contain a letter or a digit")
	}
	return name, normalized, nil
}

// itemSource checks a source and its reference; no source means manual.
func itemSource(source *generated.ItemSource, ref *string) (string, *string, error) {
	kind := generated.ItemSourceManual
	if source != nil {
		if !source.Valid() {
			return "", nil, invalid("source must be manual, capture, recipe, or staple")
		}
		kind = *source
	}
	cleaned, err := cleanText("sourceRef", ref, sourceRefLimit)
	return string(kind), cleaned, err
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

func timestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

// actorOf reads a stored X-Life-OS-Actor value. Writes cannot store an
// invalid one (RequireActor), so anything else reads as the service itself.
func actorOf(value string) generated.Actor {
	actor, ok := activity.ParseActor(value)
	if !ok {
		actor = activity.System("food")
	}
	result := generated.Actor{Kind: generated.ActorKind(actor.Kind)}
	if actor.Name != "" {
		result.Name = &actor.Name
	}
	return result
}

// requestActor is the request's X-Life-OS-Actor as the rows it writes keep
// it, such as "user" or "agent:codex-inbox-processor".
func requestActor(c fiber.Ctx) string {
	actor, _ := activity.ParseActor(c.Get(activity.ActorHeader))
	if actor.Kind == "user" {
		return "user"
	}
	return actor.Kind + ":" + actor.Name
}

func pointer[T any](value T) *T { return &value }

// itemError names the item of a batch that an API error is about.
func itemError(index int, err error) error {
	var appError *httpserver.Error
	if errors.As(err, &appError) {
		return apiError(appError.Status, appError.Code, fmt.Sprintf("items[%d]: %s", index, appError.Message))
	}
	return err
}
