package api

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/activity/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/activity/internal/repository/generated"
)

// Server diagnostics (docs/architecture/multi-user.md#logs): services post
// what went wrong as codes, ids, counts, and times (pkg/diagnostics); the
// server's admins read them. No field can carry content: every one is
// checked again here, with pkg/diagnostics' own rules.

// DiagnosticsRetention is how long diagnostics are kept.
const DiagnosticsRetention = 90 * 24 * time.Hour

// pruneEvery bounds how often recording deletes the old ones.
const pruneEvery = time.Hour

// readPolicy: admins who signed in. Diagnostics are new with accounts, so
// legacy access is not enough; a request without a token is an internal
// caller (the Dashboard renders them after its own admin check).
var readPolicy = identity.AdminPolicy{RefuseLegacy: true}

type pruner struct {
	mu   sync.Mutex
	last time.Time
}

func (p *pruner) due(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.last) < pruneEvery {
		return false
	}
	p.last = now
	return true
}

func adminRead(c fiber.Ctx) error {
	principal, ok := identity.FromFiber(c)
	return identity.CheckAdmin(principal, ok, readPolicy)
}

func (h *Handler) RecordDiagnosticEvents(c fiber.Ctx) error {
	// Services post diagnostics on the private network; the gateway lets
	// only GET through to Activity, and a person's request is refused here
	// as well.
	if principal, ok := identity.FromFiber(c); ok && principal.Kind != identity.KindService {
		return httpserver.NewError(fiber.StatusForbidden, "services_only", "Only Life OS's services record diagnostics.")
	}
	var body generated.DiagnosticBatch
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(400, "invalid diagnostics")
	}
	if len(body.Events) > 1000 {
		return fiber.NewError(400, "at most 1000 diagnostics at once")
	}
	params := make([]store.RecordDiagnosticEventParams, 0, len(body.Events))
	for _, event := range body.Events {
		param, err := diagnosticParams(event)
		if err != nil {
			return httpserver.NewError(400, "invalid_diagnostic", err.Error())
		}
		params = append(params, param)
	}
	for _, param := range params {
		if err := h.store.RecordDiagnosticEvent(c.Context(), param); err != nil {
			return internal(err)
		}
	}
	if now := h.now(); h.prune.due(now) {
		if _, err := h.store.DeleteDiagnosticEventsBefore(c.Context(), pgtype.Timestamptz{Time: now.Add(-DiagnosticsRetention), Valid: true}); err != nil {
			return internal(err)
		}
	}
	return c.Status(fiber.StatusAccepted).JSON(generated.DiagnosticRecorded{Recorded: len(params)})
}

// diagnosticParams checks one diagnostic with pkg/diagnostics' rules (the
// same the services' recorder applies) and the contract's list of codes.
func diagnosticParams(body generated.DiagnosticEventCreate) (store.RecordDiagnosticEventParams, error) {
	event := diagnostics.Event{
		Service: body.Service,
		Code:    diagnostics.Code(body.Code),
		Count:   body.Count,
		FirstAt: body.FirstAt,
		LastAt:  body.LastAt,
	}
	if body.Route != nil {
		event.Route = *body.Route
	}
	if body.Status != nil {
		event.Status = *body.Status
	}
	if body.DurationMs != nil {
		event.DurationMs = *body.DurationMs
	}
	if !body.Code.Valid() {
		return store.RecordDiagnosticEventParams{}, errors.New("code must be one of the diagnostic codes")
	}
	if err := event.Validate(); err != nil {
		return store.RecordDiagnosticEventParams{}, err
	}
	params := store.RecordDiagnosticEventParams{
		Service:    event.Service,
		Code:       string(event.Code),
		Route:      body.Route,
		Count:      int32(min(event.Count, 1<<30)),
		DurationMs: body.DurationMs,
		FirstAt:    pgtype.Timestamptz{Time: event.FirstAt.UTC(), Valid: true},
		LastAt:     pgtype.Timestamptz{Time: event.LastAt.UTC(), Valid: true},
	}
	if body.Route != nil && *body.Route == "" {
		params.Route = nil
	}
	if body.Status != nil {
		status := int32(*body.Status)
		params.Status = &status
	}
	if body.SubjectId != nil {
		params.SubjectID = pgtype.UUID{Bytes: *body.SubjectId, Valid: true}
	}
	if body.RequestId != nil {
		params.RequestID = pgtype.UUID{Bytes: *body.RequestId, Valid: true}
	}
	return params, nil
}

func (h *Handler) ListDiagnosticEvents(c fiber.Ctx, params generated.ListDiagnosticEventsParams) error {
	if err := adminRead(c); err != nil {
		return err
	}
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 200 {
			return fiber.NewError(400, "limit must be between 1 and 200")
		}
		limit = *params.Limit
	}
	query := store.ListDiagnosticEventsParams{
		Service:   params.Service,
		LastFrom:  timestamp(params.From),
		LastTo:    timestamp(params.To),
		PageLimit: int32(limit + 1),
	}
	if params.Code != nil {
		if !params.Code.Valid() {
			return fiber.NewError(400, "invalid code")
		}
		code := string(*params.Code)
		query.Code = &code
	}
	if params.Cursor != nil {
		at, id, err := decodeDiagnosticCursor(*params.Cursor)
		if err != nil {
			return fiber.NewError(400, "invalid cursor")
		}
		query.CursorAt, query.CursorID = pgtype.Timestamptz{Time: at, Valid: true}, &id
	}
	rows, err := h.store.ListDiagnosticEvents(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	result := generated.DiagnosticEventList{Items: make([]generated.DiagnosticEvent, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			cursor := encodeDiagnosticCursor(rows[index-1])
			result.NextCursor = &cursor
			break
		}
		result.Items = append(result.Items, diagnosticOutput(row))
	}
	return c.JSON(result)
}

func (h *Handler) SummarizeDiagnosticEvents(c fiber.Ctx, params generated.SummarizeDiagnosticEventsParams) error {
	if err := adminRead(c); err != nil {
		return err
	}
	hours := 24
	if params.Hours != nil {
		if *params.Hours < 1 || *params.Hours > 2160 {
			return fiber.NewError(400, "hours must be between 1 and 2160")
		}
		hours = *params.Hours
	}
	since := h.now().Add(-time.Duration(hours) * time.Hour).UTC()
	rows, err := h.store.SummarizeDiagnosticEvents(c.Context(), pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		return internal(err)
	}
	result := generated.DiagnosticSummary{Since: since, Items: make([]generated.DiagnosticCount, 0, len(rows))}
	for _, row := range rows {
		result.Items = append(result.Items, generated.DiagnosticCount{
			Service: row.Service, Code: generated.DiagnosticCode(row.Code), Count: row.Total, LastAt: row.LastAt.Time,
		})
	}
	return c.JSON(result)
}

func diagnosticOutput(row store.DiagnosticEvent) generated.DiagnosticEvent {
	result := generated.DiagnosticEvent{
		Id:         row.ID,
		Service:    row.Service,
		Code:       generated.DiagnosticCode(row.Code),
		Route:      row.Route,
		Count:      int(row.Count),
		DurationMs: row.DurationMs,
		FirstAt:    row.FirstAt.Time,
		LastAt:     row.LastAt.Time,
		RecordedAt: row.RecordedAt.Time,
	}
	if row.Status != nil {
		status := int(*row.Status)
		result.Status = &status
	}
	if row.SubjectID.Valid {
		id := types.UUID(row.SubjectID.Bytes)
		result.SubjectId = &id
	}
	if row.RequestID.Valid {
		id := types.UUID(row.RequestID.Bytes)
		result.RequestId = &id
	}
	return result
}

func encodeDiagnosticCursor(row store.DiagnosticEvent) string {
	raw := row.LastAt.Time.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(row.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeDiagnosticCursor(cursor string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, 0, errors.New("malformed cursor")
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, 0, err
	}
	parsedID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return time.Time{}, 0, err
	}
	return parsedAt, parsedID, nil
}
