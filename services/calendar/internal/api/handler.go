package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/config"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

const (
	defaultListLimit   = 50
	defaultSearchLimit = 20
	maxPageLimit       = 100
	// updateAttempts bounds how often an update re-reads an event that
	// changed between its read and its write.
	updateAttempts = 3
)

type Handler struct {
	queries *store.Queries
	// ownerTimeZone is the IANA timezone reads compare all-day dates in
	// when the request names none.
	ownerTimeZone string
	now           func() time.Time
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the Calendar API from queries. ownerTimeZone is the
// owner's validated IANA timezone (see config.OwnerTimeZone); empty means UTC.
func NewHandler(queries *store.Queries, ownerTimeZone string) *Handler {
	if ownerTimeZone == "" {
		ownerTimeZone = "UTC"
	}
	return &Handler{queries: queries, ownerTimeZone: ownerTimeZone, now: time.Now}
}

func (h *Handler) ListEvents(c fiber.Ctx, params generated.ListEventsParams) error {
	limit, err := pageLimit(params.Limit, defaultListLimit)
	if err != nil {
		return err
	}
	timeZone, err := h.timeZone(params.TimeZone)
	if err != nil {
		return err
	}
	if !params.To.After(params.From) {
		return badRequest("to must be after from")
	}
	sourceKind, err := sourceKindFilter(params.SourceKind)
	if err != nil {
		return err
	}
	// One row beyond the page tells whether another page exists.
	query := store.ListEventsParams{
		TimeZone: timeZone, RangeStart: timestamp(&params.From), RangeEnd: timestamp(&params.To),
		IncludeCancelled: params.IncludeCancelled != nil && *params.IncludeCancelled,
		SourceKind:       sourceKind, SourceID: sourceIDFilter(params.SourceId), PageLimit: int32(limit + 1),
	}
	if params.Cursor != nil {
		after, err := decodeListCursor(*params.Cursor)
		if err != nil {
			return badRequest("invalid cursor")
		}
		query.AfterStartsAt, query.AfterID = timestamp(&after.StartsAt), pgID(after.ID)
	}
	rows, err := h.queries.ListEvents(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	result := generated.EventList{Items: make([]generated.CalendarEvent, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			cursor := encodeListCursor(listPosition{StartsAt: last.StartsAt.Time, ID: last.Event.ID.Bytes})
			result.NextCursor = &cursor
			break
		}
		item, err := event(row.Event)
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, item)
	}
	return c.JSON(result)
}

func (h *Handler) SearchEvents(c fiber.Ctx, params generated.SearchEventsParams) error {
	patterns, err := searchPatterns(params.Q)
	if err != nil {
		return badRequest(err.Error())
	}
	limit, err := pageLimit(params.Limit, defaultSearchLimit)
	if err != nil {
		return err
	}
	timeZone, err := h.timeZone(params.TimeZone)
	if err != nil {
		return err
	}
	if params.From != nil && params.To != nil && !params.To.After(*params.From) {
		return badRequest("to must be after from")
	}
	sourceKind, err := sourceKindFilter(params.SourceKind)
	if err != nil {
		return err
	}
	anchor := h.now().UTC()
	query := store.SearchEventsParams{
		TimeZone: timeZone, Patterns: patterns, RangeStart: timestamp(params.From), RangeEnd: timestamp(params.To),
		IncludeCancelled: params.IncludeCancelled != nil && *params.IncludeCancelled,
		SourceKind:       sourceKind, SourceID: sourceIDFilter(params.SourceId), PageLimit: int32(limit + 1),
	}
	if params.Cursor != nil {
		after, err := decodeSearchCursor(*params.Cursor)
		if err != nil {
			return badRequest("invalid cursor")
		}
		// Later pages split upcoming from past where the first page did.
		anchor = after.Anchor
		query.AfterRelevance, query.AfterBucket, query.AfterSortKey, query.AfterID = &after.Relevance, &after.Bucket, &after.SortKey, pgID(after.ID)
	}
	query.Anchor = timestamp(&anchor)
	rows, err := h.queries.SearchEvents(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	result := generated.EventList{Items: make([]generated.CalendarEvent, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			cursor := encodeSearchCursor(searchPosition{Anchor: anchor, Relevance: last.Relevance, Bucket: last.Bucket, SortKey: last.SortKey, ID: last.Event.ID.Bytes})
			result.NextCursor = &cursor
			break
		}
		item, err := event(row.Event)
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, item)
	}
	return c.JSON(result)
}

func (h *Handler) CreateEvent(c fiber.Ctx) error {
	var body generated.CreateEventJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return badRequest("invalid event request")
	}
	fields := fieldsFromCreate(body)
	if err := fields.validate(); err != nil {
		return badRequest(err.Error())
	}
	params, err := fields.createParams()
	if err != nil {
		return internal(err)
	}
	if body.ExternalReference != nil {
		params.ExternalProvider, params.ExternalID = &body.ExternalReference.Provider, &body.ExternalReference.ExternalId
	}
	key, hash := idempotency(c)
	params.AutomationIdempotencyKey, params.AutomationPayloadHash = key, hash
	item, err := h.queries.CreateEvent(c.Context(), params)
	if err != nil {
		return internal(err)
	}
	if key != nil && (item.AutomationPayloadHash == nil || *item.AutomationPayloadHash != *hash) {
		return httpserver.NewError(fiber.StatusConflict, "conflict", "idempotency key was already used with a different payload")
	}
	output, err := event(item)
	if err != nil {
		return internal(err)
	}
	return c.Status(fiber.StatusCreated).JSON(output)
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

func (h *Handler) GetEvent(c fiber.Ctx, id types.UUID) error {
	item, err := h.queries.GetEvent(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	output, err := event(item)
	if err != nil {
		return internal(err)
	}
	return c.JSON(output)
}

// UpdateEvent merges the body into the stored event, validates the result,
// and writes it only if the event has not changed since it was read.
func (h *Handler) UpdateEvent(c fiber.Ctx, id types.UUID) error {
	patch, err := decodePatch(c.Body())
	if err != nil {
		return badRequest(err.Error())
	}
	for range updateAttempts {
		current, err := h.queries.GetEvent(c.Context(), pgUUID(id))
		if err != nil {
			return databaseError(err)
		}
		fields, err := fieldsFromStore(current)
		if err != nil {
			return internal(err)
		}
		merged, err := patch.apply(fields, h.now())
		if err != nil {
			return badRequest(err.Error())
		}
		params, err := merged.updateParams(current.ID, current.UpdatedAt)
		if err != nil {
			return internal(err)
		}
		item, err := h.queries.UpdateEvent(c.Context(), params)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // changed or deleted meanwhile: read it again
		}
		if err != nil {
			return internal(err)
		}
		output, err := event(item)
		if err != nil {
			return internal(err)
		}
		return c.JSON(output)
	}
	return httpserver.NewError(fiber.StatusConflict, "conflict", "the event kept changing; retry the update")
}

func (h *Handler) DeleteEvent(c fiber.Ctx, id types.UUID) error {
	affected, err := h.queries.DeleteEvent(c.Context(), pgUUID(id))
	if err != nil {
		return internal(err)
	}
	if affected == 0 {
		return notFound()
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// timeZone is the request's IANA timezone, else the owner's.
func (h *Handler) timeZone(requested *string) (string, error) {
	if requested == nil {
		return h.ownerTimeZone, nil
	}
	if !config.ValidTimeZone(*requested) {
		return "", badRequest("timeZone must be an IANA timezone such as Europe/Berlin")
	}
	return *requested, nil
}

func pageLimit(requested *int, fallback int) (int, error) {
	if requested == nil {
		return fallback, nil
	}
	if *requested < 1 || *requested > maxPageLimit {
		return 0, badRequest("limit must be between 1 and 100")
	}
	return *requested, nil
}

func sourceKindFilter(kind *generated.EventSourceKind) (*string, error) {
	if kind == nil {
		return nil, nil
	}
	if !kind.Valid() {
		return nil, badRequest("sourceKind must be manual, ics, or google")
	}
	value := string(*kind)
	return &value, nil
}

func sourceIDFilter(id *types.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgUUID(*id)
}

// badRequest is a 400 whose message tells the caller what to fix.
func badRequest(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "bad_request", message)
}

func notFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "event not found")
}

func internal(err error) error {
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "calendar query failed")
}

func databaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	return internal(err)
}
