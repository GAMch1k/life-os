package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// The change feed (P11-06): the changes, in commit order, of the events an
// integration may write elsewhere: Life OS events and those of sources
// integrations sync that accept changes. Entries are written by triggers
// (migration 000014) in the transaction of each change; the feed reads the
// events' current state.

const (
	defaultChangeLimit = 100
	maxChangeLimit     = 500
)

var errMalformedCursor = errors.New("malformed cursor")

// feedPosition is a change-feed cursor: after the entry seq (phase
// changes), or within the snapshot that starts a feed: after the event
// afterID (nil before the first), with the changes after head to follow.
type feedPosition struct {
	snapshot bool
	seq      int64
	head     int64
	afterID  *googleuuid.UUID
}

func encodeFeedCursor(position feedPosition) string {
	if !position.snapshot {
		return encodeCursor("feed", strconv.FormatInt(position.seq, 10))
	}
	after := ""
	if position.afterID != nil {
		after = position.afterID.String()
	}
	return encodeCursor("feed-snapshot", strconv.FormatInt(position.head, 10), after)
}

func decodeFeedCursor(cursor string) (feedPosition, error) {
	if fields, err := decodeCursor(cursor, "feed", 1); err == nil {
		seq, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || seq < 0 {
			return feedPosition{}, errMalformedCursor
		}
		return feedPosition{seq: seq}, nil
	}
	fields, err := decodeCursor(cursor, "feed-snapshot", 2)
	if err != nil {
		return feedPosition{}, err
	}
	head, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || head < 0 {
		return feedPosition{}, errMalformedCursor
	}
	position := feedPosition{snapshot: true, head: head}
	if fields[1] != "" {
		id, err := googleuuid.Parse(fields[1])
		if err != nil {
			return feedPosition{}, err
		}
		position.afterID = &id
	}
	return position, nil
}

// changeFeedPage is a ChangeFeedPage. It is rendered here rather than with
// the generated types: occurrence changes keep their stored form, where a
// null description or location clears it.
type changeFeedPage struct {
	Items      []changeFeedEntry `json:"items"`
	NextCursor string            `json:"nextCursor"`
	HasMore    bool              `json:"hasMore"`
	Phase      string            `json:"phase"`
}

type changeFeedEntry struct {
	EventID    types.UUID                  `json:"eventId"`
	Deleted    bool                        `json:"deleted"`
	Event      *generated.CalendarEvent    `json:"event"`
	Exceptions []occurrenceExceptionView   `json:"exceptions"`
	Source     *generated.ChangeFeedSource `json:"source"`
	Actor      *string                     `json:"actor"`
	Actors     []string                    `json:"actors"`
	ChangedAt  time.Time                   `json:"changedAt"`
	Sequence   *int64                      `json:"sequence"`
}

type occurrenceExceptionView struct {
	RecurrenceID string          `json:"recurrenceId"`
	Cancelled    bool            `json:"cancelled"`
	CancelledAt  *time.Time      `json:"cancelledAt"`
	Changes      json.RawMessage `json:"changes"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

func cursorExpired() error {
	return httpserver.NewError(fiber.StatusGone, "cursor_expired",
		"the changes after this cursor were pruned; start again without a cursor (a snapshot of every followed event comes first)")
}

func (h *Handler) ListChanges(c fiber.Ctx, params generated.ListChangesParams) error {
	actor, ok := activity.ParseActor(c.Get(activity.ActorHeader))
	if !ok || actor.Kind != "system" && actor.Kind != "dev" {
		return forbidden("only Life OS components (X-Life-OS-Actor system:<name>) and dev:<name> tools may read the change feed")
	}
	limit := defaultChangeLimit
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > maxChangeLimit {
			return badRequest("limit must be between 1 and 500")
		}
		limit = *params.Limit
	}
	// own: Life OS events and the reading integration's sources' events.
	var integration *string
	scope := generated.All
	if actor.Kind == "system" {
		scope = generated.Own
	}
	if params.Scope != nil {
		scope = *params.Scope
	}
	switch scope {
	case generated.Own:
		if actor.Kind != "system" {
			return badRequest("scope own needs a system:<name> actor, whose sources it keeps")
		}
		name := actor.Name
		integration = &name
	case generated.All:
	default:
		return badRequest("scope must be own or all")
	}
	position := feedPosition{snapshot: true}
	fresh := params.Cursor == nil
	if !fresh {
		var err error
		if position, err = decodeFeedCursor(*params.Cursor); err != nil {
			return httpserver.NewError(fiber.StatusBadRequest, "invalid_cursor", "invalid cursor")
		}
	}

	ctx := c.Context()
	// One snapshot of the database for the whole page: the feed's state,
	// its entries, and the events' state agree.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := h.queries.WithTx(tx)
	feed, err := queries.GetChangeFeed(ctx)
	if err != nil {
		return internal(err)
	}
	var page changeFeedPage
	switch {
	case fresh:
		position.head = feed.Head
		page, err = h.snapshotPage(ctx, queries, position, integration, limit)
	case position.snapshot && position.head < feed.PrunedThrough, !position.snapshot && position.seq < feed.PrunedThrough:
		return cursorExpired()
	case position.snapshot:
		page, err = h.snapshotPage(ctx, queries, position, integration, limit)
	default:
		page, err = h.changesPage(ctx, queries, position.seq, integration, limit)
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(page)
}

// snapshotPage lists the followed events after position, as they are.
func (h *Handler) snapshotPage(ctx context.Context, queries *store.Queries, position feedPosition, integration *string, limit int) (changeFeedPage, error) {
	params := store.ListFollowedEventsParams{Integration: integration, PageLimit: int32(limit + 1)}
	if position.afterID != nil {
		params.AfterID = pgID(*position.afterID)
	}
	rows, err := queries.ListFollowedEvents(ctx, params)
	if err != nil {
		return changeFeedPage{}, err
	}
	page := changeFeedPage{Items: []changeFeedEntry{}, Phase: string(generated.ChangeFeedPagePhaseSnapshot), HasMore: len(rows) > limit}
	rows = rows[:min(len(rows), limit)]
	events := make([]store.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, row.Event)
	}
	rendered, err := h.feedEvents(ctx, queries, events)
	if err != nil {
		return changeFeedPage{}, err
	}
	for _, row := range events {
		entry := rendered[row.ID.Bytes]
		entry.Actors, entry.ChangedAt = []string{}, row.UpdatedAt.Time.UTC()
		page.Items = append(page.Items, entry)
	}
	if page.HasMore {
		last := googleuuid.UUID(events[len(events)-1].ID.Bytes)
		page.NextCursor = encodeFeedCursor(feedPosition{snapshot: true, head: position.head, afterID: &last})
	} else {
		// The snapshot is complete: what follows are the changes made since
		// it started.
		page.NextCursor = encodeFeedCursor(feedPosition{seq: position.head})
	}
	return page, nil
}

// changesPage lists the changes after the entry after, several changes of
// one event as one entry with its current state.
func (h *Handler) changesPage(ctx context.Context, queries *store.Queries, after int64, integration *string, limit int) (changeFeedPage, error) {
	rows, err := queries.ListEventChanges(ctx, store.ListEventChangesParams{After: after, PageLimit: int32(limit + 1)})
	if err != nil {
		return changeFeedPage{}, err
	}
	page := changeFeedPage{Items: []changeFeedEntry{}, Phase: string(generated.ChangeFeedPagePhaseChanges), HasMore: len(rows) > limit}
	rows = rows[:min(len(rows), limit)]
	next := after
	if len(rows) > 0 {
		next = rows[len(rows)-1].Seq
	}
	page.NextCursor = encodeFeedCursor(feedPosition{seq: next})

	// The latest change of each event, in the order of those, with every
	// actor of its changes.
	type change struct {
		latest store.ListEventChangesRow
		actors []string
	}
	changes := map[[16]byte]*change{}
	var order [][16]byte
	sourceIDs := map[[16]byte]pgtype.UUID{}
	for _, row := range rows {
		found := changes[row.EventID.Bytes]
		if found == nil {
			found = &change{}
			changes[row.EventID.Bytes] = found
		} else {
			for index, id := range order {
				if id == row.EventID.Bytes {
					order = append(order[:index], order[index+1:]...)
					break
				}
			}
		}
		order = append(order, row.EventID.Bytes)
		found.latest = row
		known := false
		for _, actor := range found.actors {
			known = known || actor == row.Actor
		}
		if !known {
			found.actors = append(found.actors, row.Actor)
		}
		if row.SourceID.Valid {
			sourceIDs[row.SourceID.Bytes] = row.SourceID
		}
	}
	identities, err := h.sourceIdentities(ctx, queries, sourceIDs)
	if err != nil {
		return changeFeedPage{}, err
	}
	ids := make([]pgtype.UUID, 0, len(order))
	for _, id := range order {
		ids = append(ids, pgtype.UUID{Bytes: id, Valid: true})
	}
	stored, err := queries.ListEventsByID(ctx, ids)
	if err != nil {
		return changeFeedPage{}, err
	}
	rendered, err := h.feedEvents(ctx, queries, stored)
	if err != nil {
		return changeFeedPage{}, err
	}
	for _, id := range order {
		found := changes[id]
		entry, exists := rendered[id]
		if !exists {
			// Deleted: a tombstone, with the source identity it had.
			entry = changeFeedEntry{EventID: types.UUID(id), Deleted: true, Exceptions: []occurrenceExceptionView{}}
			if found.latest.SourceID.Valid {
				identity, ok := identities[found.latest.SourceID.Bytes]
				if !ok {
					continue // its source is gone too: nothing to write anywhere
				}
				entry.Source = &generated.ChangeFeedSource{
					SourceId: uuid(found.latest.SourceID), Integration: identity.Integration, ExternalId: identity.ExternalID,
					ExternalUid: deref(found.latest.ExternalUid),
				}
			}
		}
		if integration != nil && entry.Source != nil && entry.Source.Integration != *integration {
			continue // another integration's event
		}
		actor := found.latest.Actor
		sequence := found.latest.Seq
		entry.Actor, entry.Actors, entry.Sequence = &actor, found.actors, &sequence
		entry.ChangedAt = found.latest.ChangedAt.Time.UTC()
		page.Items = append(page.Items, entry)
	}
	return page, nil
}

// sourceIdentities reads the integration identity of the given sources.
func (h *Handler) sourceIdentities(ctx context.Context, queries *store.Queries, ids map[[16]byte]pgtype.UUID) (map[[16]byte]store.ListSourceIdentitiesRow, error) {
	out := make(map[[16]byte]store.ListSourceIdentitiesRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	list := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		list = append(list, id)
	}
	rows, err := queries.ListSourceIdentities(ctx, list)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID.Bytes] = row
	}
	return out, nil
}

// feedEvents renders stored events as feed entries (without their change
// details): each as GET /v1/events/{id} shows it, with its occurrence
// exceptions and, for an integration's event, its source identity.
func (h *Handler) feedEvents(ctx context.Context, queries *store.Queries, rows []store.Event) (map[[16]byte]changeFeedEntry, error) {
	out := make(map[[16]byte]changeFeedEntry, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	items := make([]generated.CalendarEvent, 0, len(rows))
	var series []pgtype.UUID
	sourceIDs := map[[16]byte]pgtype.UUID{}
	for _, row := range rows {
		item, err := event(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if row.RecurrenceFrequency != nil {
			series = append(series, row.ID)
		}
		if row.SourceID.Valid {
			sourceIDs[row.SourceID.Bytes] = row.SourceID
		}
	}
	if err := h.resolveSources(ctx, items); err != nil {
		return nil, err
	}
	exceptions := map[[16]byte][]occurrenceExceptionView{}
	if len(series) > 0 {
		stored, err := queries.ListEventExceptions(ctx, series)
		if err != nil {
			return nil, err
		}
		for _, row := range stored {
			exceptions[row.EventID.Bytes] = append(exceptions[row.EventID.Bytes], occurrenceExceptionView{
				RecurrenceID: row.RecurrenceID, Cancelled: row.CancelledAt.Valid, CancelledAt: nullableTime(row.CancelledAt),
				Changes: json.RawMessage(row.Changes), UpdatedAt: row.UpdatedAt.Time.UTC(),
			})
		}
	}
	identities, err := h.sourceIdentities(ctx, queries, sourceIDs)
	if err != nil {
		return nil, err
	}
	for index, row := range rows {
		item := items[index]
		entry := changeFeedEntry{EventID: uuid(row.ID), Event: &item, Exceptions: exceptions[row.ID.Bytes]}
		if entry.Exceptions == nil {
			entry.Exceptions = []occurrenceExceptionView{}
		}
		if row.SourceID.Valid {
			identity := identities[row.SourceID.Bytes]
			entry.Source = &generated.ChangeFeedSource{
				SourceId: uuid(row.SourceID), Integration: identity.Integration, ExternalId: identity.ExternalID,
				ExternalUid: deref(row.ExternalUid),
			}
		}
		out[row.ID.Bytes] = entry
	}
	return out, nil
}
