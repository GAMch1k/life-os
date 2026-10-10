package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/config"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

const (
	defaultListLimit   = 50
	defaultSearchLimit = 20
	maxPageLimit       = 100
	// updateAttempts bounds how often a source update re-reads a source
	// that changed between its read and its write.
	updateAttempts = 3
)

type Handler struct {
	// pool runs the transactions of writes that change an event and its
	// occurrence exceptions together; queries runs everything else.
	pool    *pgxpool.Pool
	queries *store.Queries
	// ownerTimeZone is the IANA timezone reads compare all-day dates in
	// when the request names none; recurring timed events without a
	// timeZone repeat on its wall clock.
	ownerTimeZone string
	ownerLocation *time.Location
	now           func() time.Time
	// refresher runs source refreshes; fetcher validates feed URLs; wake
	// tells the scheduler a source needs a refresh now.
	refresher *sources.Refresher
	fetcher   *ics.Fetcher
	wake      func()
	// activity records event changes (see WithActivity); nil records none.
	activity *activity.Emitter
	// google is the Google Calendar connection (see WithGoogle); nil reads
	// as not configured.
	google *google.Service
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the Calendar API from pool. ownerTimeZone is the owner's
// validated IANA timezone (see config.OwnerTimeZone); empty means UTC.
func NewHandler(pool *pgxpool.Pool, ownerTimeZone string) *Handler {
	if ownerTimeZone == "" {
		ownerTimeZone = "UTC"
	}
	location, err := time.LoadLocation(ownerTimeZone)
	if err != nil {
		ownerTimeZone, location = "UTC", time.UTC
	}
	return &Handler{
		pool: pool, queries: store.New(pool), ownerTimeZone: ownerTimeZone, ownerLocation: location, now: time.Now,
		fetcher: ics.NewFetcher(0, 0, false), wake: func() {},
	}
}

// WithSources enables source refreshes: refresher runs them (its fetcher
// also validates feed URLs) and wake, if not nil, prompts the scheduler.
func (h *Handler) WithSources(refresher *sources.Refresher, wake func()) *Handler {
	h.refresher = refresher
	if refresher != nil {
		h.fetcher = refresher.Fetcher()
	}
	if wake != nil {
		h.wake = wake
	}
	return h
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
	zone, err := time.LoadLocation(timeZone)
	if err != nil {
		return badRequest("timeZone must be an IANA timezone such as Europe/Berlin")
	}
	if !params.To.After(params.From) {
		return badRequest("to must be after from")
	}
	sourceKind, err := sourceKindFilter(params.SourceKind)
	if err != nil {
		return err
	}
	includeCancelled := params.IncludeCancelled != nil && *params.IncludeCancelled
	// One row beyond the page tells whether another page exists.
	query := store.ListEventsParams{
		TimeZone: timeZone, RangeStart: timestamp(&params.From), RangeEnd: timestamp(&params.To),
		IncludeCancelled: includeCancelled, SourceKind: sourceKind, SourceID: sourceIDFilter(params.SourceId), PageLimit: int32(limit + 1),
	}
	var after *listPosition
	if params.Cursor != nil {
		position, err := decodeListCursor(*params.Cursor)
		if err != nil {
			return badRequest("invalid cursor")
		}
		after = &position
		query.AfterStartsAt, query.AfterID = timestamp(&position.StartsAt), pgID(position.ID)
	}
	rows, err := h.queries.ListEvents(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	recurring, err := h.queries.ListRecurringEvents(c.Context(), store.ListRecurringEventsParams{
		TimeZone: timeZone, RangeStart: query.RangeStart, RangeEnd: query.RangeEnd,
		IncludeCancelled: includeCancelled, SourceKind: sourceKind, SourceID: query.SourceID,
	})
	if err != nil {
		return internal(err)
	}
	ids := make([]pgtype.UUID, 0, len(recurring))
	for _, row := range recurring {
		ids = append(ids, row.Event.ID)
	}
	exceptions, err := h.recurringExceptions(c.Context(), ids)
	if err != nil {
		return internal(err)
	}
	merged, err := h.listOccurrences(recurring, exceptions, params.From, params.To, zone, includeCancelled, after, limit+1)
	if err != nil {
		return internal(err)
	}
	for _, row := range rows {
		item, err := event(row.Event)
		if err != nil {
			return internal(err)
		}
		merged = append(merged, listed{startsAt: row.StartsAt.Time, id: row.Event.ID.Bytes, item: item})
	}
	sortListed(merged)
	result := generated.EventList{Items: make([]generated.CalendarEvent, 0, min(len(merged), limit))}
	for index, entry := range merged {
		if index == limit {
			last := merged[index-1]
			cursor := encodeListCursor(listPosition{StartsAt: last.startsAt, ID: last.id})
			result.NextCursor = &cursor
			break
		}
		result.Items = append(result.Items, entry.item)
	}
	if err := h.resolveSources(c.Context(), result.Items); err != nil {
		return internal(err)
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
	zone, err := time.LoadLocation(timeZone)
	if err != nil {
		return badRequest("timeZone must be an IANA timezone such as Europe/Berlin")
	}
	if params.From != nil && params.To != nil && !params.To.After(*params.From) {
		return badRequest("to must be after from")
	}
	sourceKind, err := sourceKindFilter(params.SourceKind)
	if err != nil {
		return err
	}
	includeCancelled := params.IncludeCancelled != nil && *params.IncludeCancelled
	anchor := h.now().UTC()
	query := store.SearchEventsParams{
		TimeZone: timeZone, Patterns: patterns, RangeStart: timestamp(params.From), RangeEnd: timestamp(params.To),
		IncludeCancelled: includeCancelled, SourceKind: sourceKind, SourceID: sourceIDFilter(params.SourceId), PageLimit: int32(limit + 1),
	}
	var after *searchPosition
	if params.Cursor != nil {
		position, err := decodeSearchCursor(*params.Cursor)
		if err != nil {
			return badRequest("invalid cursor")
		}
		// Later pages split upcoming from past where the first page did.
		after, anchor = &position, position.Anchor
		query.AfterRelevance, query.AfterBucket, query.AfterSortKey, query.AfterID = &position.Relevance, &position.Bucket, &position.SortKey, pgID(position.ID)
	}
	query.Anchor = timestamp(&anchor)
	rows, err := h.queries.SearchEvents(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	recurring, err := h.queries.SearchRecurringEvents(c.Context(), store.SearchRecurringEventsParams{
		Patterns: patterns, RangeEnd: query.RangeEnd, TimeZone: timeZone,
		IncludeCancelled: includeCancelled, SourceKind: sourceKind, SourceID: query.SourceID,
	})
	if err != nil {
		return internal(err)
	}
	ids := make([]pgtype.UUID, 0, len(recurring))
	for _, row := range recurring {
		ids = append(ids, row.Event.ID)
	}
	exceptions, err := h.recurringExceptions(c.Context(), ids)
	if err != nil {
		return internal(err)
	}
	merged, err := h.searchOccurrences(recurring, exceptions, params.From, params.To, zone, includeCancelled, anchor, after, limit+1)
	if err != nil {
		return internal(err)
	}
	for _, row := range rows {
		item, err := event(row.Event)
		if err != nil {
			return internal(err)
		}
		merged = append(merged, ranked{relevance: row.Relevance, bucket: row.Bucket, sortKey: row.SortKey, id: row.Event.ID.Bytes, item: item})
	}
	sortRanked(merged)
	result := generated.EventList{Items: make([]generated.CalendarEvent, 0, min(len(merged), limit))}
	for index, entry := range merged {
		if index == limit {
			last := merged[index-1]
			cursor := encodeSearchCursor(searchPosition{Anchor: anchor, Relevance: last.relevance, Bucket: last.bucket, SortKey: last.sortKey, ID: last.id})
			result.NextCursor = &cursor
			break
		}
		result.Items = append(result.Items, entry.item)
	}
	if err := h.resolveSources(c.Context(), result.Items); err != nil {
		return internal(err)
	}
	return c.JSON(result)
}

func (h *Handler) CreateEvent(c fiber.Ctx) error {
	var body generated.CreateEventJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return badRequest("invalid event request")
	}
	fields := fieldsFromCreate(body)
	if err := fields.validate(h.ownerLocation); err != nil {
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
	h.recordEvent(c, eventCreated, item, nil, nil)
	h.google.QueueExport(c.Context(), item.ID)
	return h.respond(c, fiber.StatusCreated, item)
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
	return h.respond(c, fiber.StatusOK, item)
}

// respond renders one stored event, with its effective reminders and source.
func (h *Handler) respond(c fiber.Ctx, status int, item store.Event) error {
	output, err := event(item)
	if err != nil {
		return internal(err)
	}
	items := []generated.CalendarEvent{output}
	if err := h.resolveSources(c.Context(), items); err != nil {
		return internal(err)
	}
	return c.Status(status).JSON(items[0])
}

// UpdateEvent merges the body into the stored event, validates the result,
// and writes it (All events for a recurring event), holding the event so
// its occurrence exceptions change with it. A synced Google event is
// written to Google first (see updateGoogleEvent); a Life OS event's
// change is queued for export.
func (h *Handler) UpdateEvent(c fiber.Ctx, id types.UUID) error {
	patch, err := decodePatch(c.Body())
	if err != nil {
		return badRequest(err.Error())
	}
	if h.google != nil {
		current, err := h.queries.GetEvent(c.Context(), pgUUID(id))
		if err != nil {
			return databaseError(err)
		}
		if current.SourceKind == string(generated.EventSourceKindGoogle) {
			target, write, err := h.googleTarget(c.Context(), current, &patch)
			if err != nil {
				return err
			}
			if write {
				return h.updateGoogleEvent(c, current, target, patch, fiber.StatusOK)
			}
		}
	}
	var current, item store.Event
	err = h.inTx(c.Context(), func(queries *store.Queries) error {
		current, err = queries.LockEvent(c.Context(), pgUUID(id))
		if err != nil {
			return databaseError(err)
		}
		item, err = h.updateLocked(c.Context(), queries, current, patch)
		return err
	})
	if err != nil {
		return err
	}
	// An update that changes nothing records nothing.
	if eventType, changes := classifyUpdate(current, item); eventType != "" {
		h.recordEvent(c, eventType, item, &current, changes)
		if item.SourceKind == string(generated.EventSourceKindManual) {
			h.google.QueueExport(c.Context(), item.ID)
		}
	}
	return h.respond(c, fiber.StatusOK, item)
}

// updateLocked applies patch to current, an event held by the transaction
// of queries, and removes the exceptions of occurrences the change ends.
func (h *Handler) updateLocked(ctx context.Context, queries *store.Queries, current store.Event, patch eventPatch) (store.Event, error) {
	if current.SourceKind != string(generated.EventSourceKindManual) && !patch.onlyReminders() {
		return store.Event{}, managedBySource()
	}
	fields, err := fieldsFromStore(current)
	if err != nil {
		return store.Event{}, internal(err)
	}
	merged, err := patch.apply(fields, h.now(), h.ownerLocation)
	if err != nil {
		return store.Event{}, badRequest(err.Error())
	}
	params, err := merged.updateParams(current.ID, current.UpdatedAt)
	if err != nil {
		return store.Event{}, internal(err)
	}
	item, err := queries.UpdateEvent(ctx, params)
	if err != nil {
		return store.Event{}, databaseError(err)
	}
	if err := h.dropLostExceptions(ctx, queries, current, item); err != nil {
		return store.Event{}, internal(err)
	}
	return item, nil
}

// DeleteEvent deletes a Life OS event (and queues deleting its copy in
// Google) or an editable synced Google event (in Google first). A feed
// event's source owns it (and cancels it when it leaves the feed), so it
// cannot be deleted here.
func (h *Handler) DeleteEvent(c fiber.Ctx, id types.UUID) error {
	current, err := h.queries.GetEvent(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	if current.SourceKind == string(generated.EventSourceKindGoogle) && h.google != nil {
		target, _, err := h.googleTarget(c.Context(), current, nil)
		if err != nil {
			return err
		}
		return h.deleteGoogleEvent(c, current, target)
	}
	if current.SourceKind != string(generated.EventSourceKindManual) {
		return managedBySource()
	}
	affected, err := h.queries.DeleteEvent(c.Context(), pgUUID(id))
	if err != nil {
		return internal(err)
	}
	if affected == 0 {
		return notFound()
	}
	h.recordEvent(c, eventDeleted, current, nil, nil)
	h.google.QueueDeletion(c.Context(), current.ID)
	return c.SendStatus(fiber.StatusNoContent)
}

// inTx runs fn on queries bound to one transaction, committed when fn
// returns no error and rolled back otherwise.
func (h *Handler) inTx(ctx context.Context, fn func(*store.Queries) error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(h.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return internal(err)
	}
	return nil
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

func managedBySource() error {
	return httpserver.NewError(fiber.StatusConflict, "managed_by_source",
		"this event is imported from a calendar source and is read-only except for its reminders; its source updates, cancels, and restores it")
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
