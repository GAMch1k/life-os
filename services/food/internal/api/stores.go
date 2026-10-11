package api

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

const storeNameKey = "stores_normalized_name_key"

func storeView(item store.Store) generated.Store {
	return generated.Store{Id: uuid(item.ID), Name: item.Name, SortOrder: int(item.SortOrder), Archived: item.ArchivedAt.Valid,
		ArchivedAt: nullableTime(item.ArchivedAt), CreatedAt: item.CreatedAt.Time.UTC(), UpdatedAt: item.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(item.CreatedBy), UpdatedBy: actorOf(item.UpdatedBy)}
}

func storeExists(name string) error {
	return apiError(fiber.StatusConflict, "store_exists", "A store is already called "+name)
}

// storeNames maps every store, archived or not, to its name, for the items
// that show it.
func storeNames(ctx context.Context, q *store.Queries) (map[[16]byte]string, error) {
	stores, err := q.ListStores(ctx, true)
	if err != nil {
		return nil, err
	}
	names := make(map[[16]byte]string, len(stores))
	for _, item := range stores {
		names[item.ID.Bytes] = item.Name
	}
	return names, nil
}

func nameOf(names map[[16]byte]string, id pgtype.UUID) *string {
	if !id.Valid {
		return nil
	}
	if name, ok := names[id.Bytes]; ok {
		return &name
	}
	return nil
}

func checkSortOrder(value *int) error {
	if value != nil && (*value < -sortOrderLimit || *value > sortOrderLimit) {
		return invalid("sortOrder must be between -1000000 and 1000000")
	}
	return nil
}

// resolveStore returns the store an item names, by id or by name. An
// archived store named by id is refused (409 store_archived); one named by
// name is restored, and an unknown name creates the store. The events
// describe what it created or restored.
func (h *Handler) resolveStore(ctx context.Context, q *store.Queries, actor string, id *types.UUID, name *string) (store.Store, []activity.Event, error) {
	if id != nil {
		item, err := q.GetStore(ctx, pgUUID(*id))
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Store{}, nil, apiError(fiber.StatusBadRequest, "unknown_store", "No store has id "+id.String())
		}
		if err != nil {
			return store.Store{}, nil, err
		}
		if item.ArchivedAt.Valid {
			return store.Store{}, nil, apiError(fiber.StatusConflict, "store_archived", "The store "+item.Name+" is archived")
		}
		return item, nil, nil
	}
	cleaned, normalized, err := cleanName("storeName", *name, storeNameLimit)
	if err != nil {
		return store.Store{}, nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		item, err := q.GetStoreByName(ctx, normalized)
		switch {
		case err == nil && !item.ArchivedAt.Valid:
			return item, nil, nil
		case err == nil:
			restored, err := q.UpdateStore(ctx, store.UpdateStoreParams{ID: item.ID, Name: item.Name, NormalizedName: item.NormalizedName,
				SortOrder: item.SortOrder, Actor: actor})
			if err != nil {
				return store.Store{}, nil, err
			}
			return restored, []activity.Event{storeEvent(storeRestored, restored, nil)}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return store.Store{}, nil, err
		}
		created, err := q.InsertStore(ctx, store.InsertStoreParams{Name: cleaned, NormalizedName: normalized, Actor: actor})
		if err == nil {
			return created, []activity.Event{storeEvent(storeCreated, created, nil)}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return store.Store{}, nil, err
		}
		// Another request created the store meanwhile: read it again.
	}
	return store.Store{}, nil, errRetry
}

func (h *Handler) ListStores(c fiber.Ctx, params generated.ListStoresParams) error {
	items, err := h.queries.ListStores(c.Context(), params.IncludeArchived != nil && *params.IncludeArchived)
	if err != nil {
		return internal(err)
	}
	result := generated.StoreList{Items: make([]generated.Store, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, storeView(item))
	}
	return c.JSON(result)
}

func (h *Handler) GetStore(c fiber.Ctx, storeID generated.StoreId) error {
	item, err := h.queries.GetStore(c.Context(), pgUUID(storeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Store")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(storeView(item))
}

func (h *Handler) CreateStore(c fiber.Ctx, _ generated.CreateStoreParams) error {
	var body generated.CreateStoreRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	name, normalized, err := cleanName("name", body.Name, storeNameLimit)
	if err != nil {
		return err
	}
	if err := checkSortOrder(body.SortOrder); err != nil {
		return err
	}
	params := store.InsertStoreParams{Name: name, NormalizedName: normalized, Actor: requestActor(c)}
	if body.SortOrder != nil {
		params.SortOrder = pointer(int32(*body.SortOrder))
	}
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		item, err := q.InsertStore(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, storeExists(name)
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusCreated, body: storeView(item), events: []activity.Event{storeEvent(storeCreated, item, nil)}}, nil
	})
}

func (h *Handler) UpdateStore(c fiber.Ctx, storeID generated.StoreId, _ generated.UpdateStoreParams) error {
	var body generated.UpdateStoreRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	known := 0
	for _, field := range []string{"name", "sortOrder", "archived"} {
		if _, ok := properties[field]; ok {
			if isNull(properties, field) {
				return invalid(field + " cannot be null")
			}
			known++
		}
	}
	if known == 0 {
		return invalid("the update names no store field")
	}
	var name, normalized string
	if body.Name != nil {
		if name, normalized, err = cleanName("name", *body.Name, storeNameLimit); err != nil {
			return err
		}
	}
	if err := checkSortOrder(body.SortOrder); err != nil {
		return err
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetStoreForUpdate(ctx, pgUUID(storeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Store")
		}
		if err != nil {
			return result{}, err
		}
		next := store.UpdateStoreParams{ID: current.ID, Name: current.Name, NormalizedName: current.NormalizedName,
			SortOrder: current.SortOrder, ArchivedAt: current.ArchivedAt, Actor: actor}
		var changes []string
		if body.Name != nil && name != current.Name {
			next.Name, next.NormalizedName = name, normalized
			changes = append(changes, "name")
		}
		if body.SortOrder != nil && int32(*body.SortOrder) != current.SortOrder {
			next.SortOrder = int32(*body.SortOrder)
			changes = append(changes, "sortOrder")
		}
		archive := ""
		if body.Archived != nil && *body.Archived != current.ArchivedAt.Valid {
			archive = storeRestored
			next.ArchivedAt = pgtype.Timestamptz{}
			if *body.Archived {
				archive, next.ArchivedAt = storeArchived, timestamptz(h.now())
			}
		}
		if len(changes) == 0 && archive == "" {
			return result{status: fiber.StatusOK, body: storeView(current)}, nil
		}
		item, err := q.UpdateStore(ctx, next)
		if uniqueViolation(err, storeNameKey) {
			return result{}, storeExists(name)
		}
		if err != nil {
			return result{}, err
		}
		var events []activity.Event
		if len(changes) > 0 {
			events = append(events, storeEvent(storeUpdated, item, changes))
		}
		if archive != "" {
			events = append(events, storeEvent(archive, item, nil))
		}
		return result{status: fiber.StatusOK, body: storeView(item), events: events}, nil
	})
}
