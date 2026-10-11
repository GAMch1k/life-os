package api

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// openNameKey is the partial unique index that allows one needed item per
// normalised name.
const openNameKey = "shopping_items_open_name_key"

func shoppingView(item store.ShoppingItem, names map[[16]byte]string) generated.ShoppingItem {
	return generated.ShoppingItem{
		Id: uuid(item.ID), Name: item.Name, NormalizedName: item.NormalizedName, Amount: item.Amount, Note: item.Note,
		Aisle: generated.Aisle(item.Aisle), AisleSetBy: generated.AisleSetBy(item.AisleSetBy),
		StoreId: nullableUUID(item.StoreID), StoreName: nameOf(names, item.StoreID),
		State: generated.ShoppingItemState(item.State), BoughtAt: nullableTime(item.BoughtAt),
		Source: generated.ItemSource(item.Source), SourceRef: item.SourceRef,
		CreatedAt: item.CreatedAt.Time.UTC(), UpdatedAt: item.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(item.CreatedBy), UpdatedBy: actorOf(item.UpdatedBy),
	}
}

func alreadyOnList(name string) error {
	return apiError(fiber.StatusConflict, "already_on_list", "“"+name+"” is already on the shopping list")
}

func sameText(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func (h *Handler) ListShoppingItems(c fiber.Ctx, params generated.ListShoppingItemsParams) error {
	limit, err := pageLimit(params.Limit)
	if err != nil {
		return err
	}
	// One row beyond the page tells whether another page exists.
	query := store.ListShoppingItemsParams{PageLimit: int32(limit + 1)}
	if params.State != nil {
		if !params.State.Valid() {
			return invalid("state must be needed or bought")
		}
		query.State = pointer(string(*params.State))
	}
	if params.StoreId != nil {
		query.StoreID = pgUUID(*params.StoreId)
	}
	if query.Aisle, err = aisleFilter(params.Aisle); err != nil {
		return err
	}
	if params.Cursor != nil {
		var position shoppingCursor
		id, err := decodeCursor(*params.Cursor, &position)
		if err != nil {
			return err
		}
		if err := position.after(&query, id); err != nil {
			return err
		}
	}
	ctx := c.Context()
	items, err := h.queries.ListShoppingItems(ctx, query)
	if err != nil {
		return internal(err)
	}
	names, err := storeNames(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	result := generated.ShoppingItemList{Items: make([]generated.ShoppingItem, 0, min(len(items), limit))}
	for index, item := range items {
		if index == limit {
			result.NextCursor = pointer(encodeCursor(shoppingPosition(items[index-1])))
			break
		}
		result.Items = append(result.Items, shoppingView(item, names))
	}
	return c.JSON(result)
}

func (h *Handler) GetShoppingItem(c fiber.Ctx, itemID generated.ItemId) error {
	ctx := c.Context()
	item, err := h.queries.GetShoppingItem(ctx, pgUUID(itemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Shopping item")
	}
	if err != nil {
		return internal(err)
	}
	names, err := storeNames(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	return c.JSON(shoppingView(item, names))
}

// newShoppingItem is one checked item of an add request.
type newShoppingItem struct {
	name, normalized string
	amount, note     *string
	aisle            *string
	storeID          *types.UUID
	storeName        *string
	source           string
	sourceRef        *string
}

func checkNewShoppingItem(item generated.NewShoppingItem) (newShoppingItem, error) {
	var result newShoppingItem
	var err error
	if result.name, result.normalized, err = cleanName("name", item.Name, nameLimit); err != nil {
		return result, err
	}
	if result.amount, err = cleanText("amount", item.Amount, amountLimit); err != nil {
		return result, err
	}
	if result.note, err = cleanText("note", item.Note, noteLimit); err != nil {
		return result, err
	}
	if result.aisle, err = aisleFilter(item.Aisle); err != nil {
		return result, err
	}
	if item.StoreId != nil && item.StoreName != nil {
		return result, invalid("give the store by storeId or by storeName, not both")
	}
	result.storeID, result.storeName = item.StoreId, item.StoreName
	result.source, result.sourceRef, err = itemSource(item.Source, item.SourceRef)
	return result, err
}

// AddShoppingItems adds a batch of items, merging each into the needed item
// with its normalised name if there is one (an upsert on the partial unique
// index, so concurrent adds of one name leave one item).
func (h *Handler) AddShoppingItems(c fiber.Ctx, _ generated.AddShoppingItemsParams) error {
	var body generated.AddShoppingItemsRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	if len(body.Items) == 0 || len(body.Items) > batchLimit {
		return invalid(fmt.Sprintf("items must hold 1 to %d items", batchLimit))
	}
	inputs := make([]newShoppingItem, len(body.Items))
	for index, item := range body.Items {
		input, err := checkNewShoppingItem(item)
		if err != nil {
			return itemError(index, err)
		}
		inputs[index] = input
	}
	// Items are written in normalised-name order, so two requests adding the
	// same names lock them in the same order; the response keeps the
	// request's order.
	order := make([]int, len(inputs))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool { return inputs[order[i]].normalized < inputs[order[j]].normalized })
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		rules, err := aisleRules(ctx, q)
		if err != nil {
			return result{}, err
		}
		// Stores first, in the request's order, so a new store is named as
		// the first item that names it writes it.
		storeIDs := make([]pgtype.UUID, len(inputs))
		var storeEvents []activity.Event
		for index, input := range inputs {
			if input.storeID == nil && input.storeName == nil {
				continue
			}
			item, events, err := h.resolveStore(ctx, q, actor, input.storeID, input.storeName)
			if err != nil {
				return result{}, itemError(index, err)
			}
			storeIDs[index] = item.ID
			storeEvents = append(storeEvents, events...)
		}
		rows := make([]store.ShoppingItem, len(inputs))
		merged := make([]bool, len(inputs))
		changes := make([][]string, len(inputs))
		for _, index := range order {
			input := inputs[index]
			params := store.AddShoppingItemParams{Name: input.name, NormalizedName: input.normalized, Amount: input.amount, Note: input.note,
				StoreID: storeIDs[index], Source: input.source, SourceRef: input.sourceRef, Actor: actor}
			params.AisleSetBy = "manual"
			if input.aisle != nil {
				params.Aisle = *input.aisle
			} else {
				params.Aisle, _, _ = domain.ChooseAisle(input.normalized, rules)
				params.AisleSetBy = "rule"
			}
			// The open item it may merge into, to tell what the merge changed.
			before, err := q.GetOpenShoppingItemByName(ctx, input.normalized)
			found := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return result{}, err
			}
			id := googleuuid.New()
			params.ID = pgtype.UUID{Bytes: id, Valid: true}
			row, err := q.AddShoppingItem(ctx, params)
			if err != nil {
				return result{}, err
			}
			rows[index], merged[index] = row, row.ID.Bytes != id
			if merged[index] {
				changes[index] = mergeChanges(before, found, row, params)
			}
		}
		names, err := storeNames(ctx, q)
		if err != nil {
			return result{}, err
		}
		response := generated.AddShoppingItemsResponse{Items: make([]generated.ShoppingItemResult, len(rows))}
		events := storeEvents
		for index, row := range rows {
			response.Items[index] = generated.ShoppingItemResult{Item: shoppingView(row, names), Merged: merged[index]}
			eventType := shoppingItemAdded
			if merged[index] {
				eventType = shoppingItemMerged
			}
			events = append(events, shoppingEvent(eventType, row, nameOf(names, row.StoreID), changes[index], nil))
		}
		return result{status: fiber.StatusOK, body: response, events: events}, nil
	})
}

// mergeChanges lists what merging params into an open item changed. Without
// the item as it was (another request added it meanwhile), it lists what
// the request gave.
func mergeChanges(before store.ShoppingItem, found bool, after store.ShoppingItem, params store.AddShoppingItemParams) []string {
	changes := []string{}
	if !found {
		before = store.ShoppingItem{Aisle: "", AisleSetBy: "rule"}
	}
	if !sameText(before.Amount, after.Amount) {
		changes = append(changes, "amount")
	}
	if !sameText(before.Note, after.Note) {
		changes = append(changes, "note")
	}
	if before.StoreID != after.StoreID {
		changes = append(changes, "storeId")
	}
	if params.AisleSetBy == "manual" && (before.Aisle != after.Aisle || before.AisleSetBy != "manual") {
		changes = append(changes, "aisle")
	}
	return changes
}

// shoppingFields are the UpdateShoppingItemRequest properties; nullable ones
// may be null.
var (
	shoppingFields   = []string{"name", "amount", "note", "aisle", "storeId", "storeName"}
	shoppingNullable = map[string]bool{"amount": true, "note": true, "storeId": true}
)

func (h *Handler) UpdateShoppingItem(c fiber.Ctx, itemID generated.ItemId, _ generated.UpdateShoppingItemParams) error {
	var body generated.UpdateShoppingItemRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	present := map[string]bool{}
	for _, field := range shoppingFields {
		if _, ok := properties[field]; ok {
			if !shoppingNullable[field] && isNull(properties, field) {
				return invalid(field + " cannot be null")
			}
			present[field] = true
		}
	}
	if len(present) == 0 {
		return invalid("the update names no shopping item field")
	}
	if present["storeId"] && present["storeName"] {
		return invalid("give the store by storeId or by storeName, not both")
	}
	var name, normalized string
	if present["name"] {
		if name, normalized, err = cleanName("name", *body.Name, nameLimit); err != nil {
			return err
		}
	}
	amount, err := cleanText("amount", body.Amount, amountLimit)
	if err != nil {
		return err
	}
	note, err := cleanText("note", body.Note, noteLimit)
	if err != nil {
		return err
	}
	aisle, err := aisleFilter(body.Aisle)
	if err != nil {
		return err
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetShoppingItemForUpdate(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Shopping item")
		}
		if err != nil {
			return result{}, err
		}
		next := store.UpdateShoppingItemParams{ID: current.ID, Name: current.Name, NormalizedName: current.NormalizedName, Amount: current.Amount,
			Note: current.Note, Aisle: current.Aisle, AisleSetBy: current.AisleSetBy, StoreID: current.StoreID, Actor: actor}
		var changes []string
		var events []activity.Event
		if present["name"] && name != current.Name {
			next.Name, next.NormalizedName = name, normalized
			changes = append(changes, "name")
		}
		if present["amount"] && !sameText(amount, current.Amount) {
			next.Amount = amount
			changes = append(changes, "amount")
		}
		if present["note"] && !sameText(note, current.Note) {
			next.Note = note
			changes = append(changes, "note")
		}
		switch {
		case present["storeId"] && body.StoreId == nil:
			next.StoreID = pgtype.UUID{}
		case present["storeId"] || present["storeName"]:
			item, storeEvents, err := h.resolveStore(ctx, q, actor, body.StoreId, body.StoreName)
			if err != nil {
				return result{}, err
			}
			next.StoreID, events = item.ID, storeEvents
		}
		if next.StoreID != current.StoreID {
			changes = append(changes, "storeId")
		}
		switch {
		case aisle != nil:
			next.Aisle, next.AisleSetBy = *aisle, "manual"
		case next.NormalizedName != current.NormalizedName && current.AisleSetBy == "rule":
			// A rename lets the rules choose the aisle again.
			rules, err := aisleRules(ctx, q)
			if err != nil {
				return result{}, err
			}
			next.Aisle, _, _ = domain.ChooseAisle(next.NormalizedName, rules)
		}
		if next.Aisle != current.Aisle || next.AisleSetBy != current.AisleSetBy {
			changes = append(changes, "aisle")
		}
		names, err := storeNames(ctx, q)
		if err != nil {
			return result{}, err
		}
		if len(changes) == 0 {
			return result{status: fiber.StatusOK, body: shoppingView(current, names), events: events}, nil
		}
		item, err := q.UpdateShoppingItem(ctx, next)
		if uniqueViolation(err, openNameKey) {
			return result{}, alreadyOnList(name)
		}
		if err != nil {
			return result{}, err
		}
		events = append(events, shoppingEvent(shoppingItemUpdated, item, nameOf(names, item.StoreID), changes, nil))
		return result{status: fiber.StatusOK, body: shoppingView(item, names), events: events}, nil
	})
}

func (h *Handler) DeleteShoppingItem(c fiber.Ctx, itemID generated.ItemId, _ generated.DeleteShoppingItemParams) error {
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		item, err := q.DeleteShoppingItem(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Shopping item")
		}
		if err != nil {
			return result{}, err
		}
		names, err := storeNames(ctx, q)
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusNoContent, events: []activity.Event{shoppingEvent(shoppingItemDeleted, item, nameOf(names, item.StoreID), nil, nil)}}, nil
	})
}

func (h *Handler) MarkShoppingItemBought(c fiber.Ctx, itemID generated.ItemId, _ generated.MarkShoppingItemBoughtParams) error {
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetShoppingItemForUpdate(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Shopping item")
		}
		if err != nil {
			return result{}, err
		}
		names, err := storeNames(ctx, q)
		if err != nil {
			return result{}, err
		}
		if current.State == "bought" {
			return result{status: fiber.StatusOK, body: shoppingView(current, names)}, nil
		}
		item, err := q.MarkShoppingItemBought(ctx, store.MarkShoppingItemBoughtParams{ID: current.ID, Actor: actor})
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusOK, body: shoppingView(item, names),
			events: []activity.Event{shoppingEvent(shoppingItemBought, item, nameOf(names, item.StoreID), nil, nil)}}, nil
	})
}

// MarkShoppingItemNeeded puts a bought item back on the list, merging it into
// the needed item with its normalised name if there is one meanwhile.
func (h *Handler) MarkShoppingItemNeeded(c fiber.Ctx, itemID generated.ItemId, _ generated.MarkShoppingItemNeededParams) error {
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetShoppingItemForUpdate(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Shopping item")
		}
		if err != nil {
			return result{}, err
		}
		names, err := storeNames(ctx, q)
		if err != nil {
			return result{}, err
		}
		if current.State == "needed" {
			return result{status: fiber.StatusOK, body: generated.ShoppingItemResult{Item: shoppingView(current, names)}}, nil
		}
		open, err := q.GetOpenShoppingItemByName(ctx, current.NormalizedName)
		if errors.Is(err, pgx.ErrNoRows) {
			item, err := q.MarkShoppingItemNeeded(ctx, store.MarkShoppingItemNeededParams{ID: current.ID, Actor: actor})
			if uniqueViolation(err, openNameKey) {
				// Another request added the name meanwhile: merge into it.
				return result{}, errRetry
			}
			if err != nil {
				return result{}, err
			}
			return result{status: fiber.StatusOK, body: generated.ShoppingItemResult{Item: shoppingView(item, names)},
				events: []activity.Event{shoppingEvent(shoppingItemReopened, item, nameOf(names, item.StoreID), nil, nil)}}, nil
		}
		if err != nil {
			return result{}, err
		}
		// The needed item stays and takes what it lacks from this one.
		next := store.UpdateShoppingItemParams{ID: open.ID, Name: open.Name, NormalizedName: open.NormalizedName, Amount: open.Amount,
			Note: open.Note, Aisle: open.Aisle, AisleSetBy: open.AisleSetBy, StoreID: open.StoreID, Actor: actor}
		if next.Amount == nil {
			next.Amount = current.Amount
		}
		if next.Note == nil {
			next.Note = current.Note
		}
		if !next.StoreID.Valid {
			next.StoreID = current.StoreID
		}
		item, err := q.UpdateShoppingItem(ctx, next)
		if err != nil {
			return result{}, err
		}
		if _, err := q.DeleteShoppingItem(ctx, current.ID); err != nil {
			return result{}, err
		}
		data := map[string]any{"mergedFromId": uuid(current.ID).String()}
		return result{status: fiber.StatusOK, body: generated.ShoppingItemResult{Item: shoppingView(item, names), Merged: true},
			events: []activity.Event{shoppingEvent(shoppingItemReopened, item, nameOf(names, item.StoreID), nil, data)}}, nil
	})
}

func (h *Handler) ClearBoughtShoppingItems(c fiber.Ctx, _ generated.ClearBoughtShoppingItemsParams) error {
	var body generated.ClearBoughtRequest
	if len(c.Body()) > 0 {
		if _, err := decodeObject(c.Body(), &body); err != nil {
			return invalid(err.Error())
		}
	}
	before := pgtype.Timestamptz{}
	if body.BoughtBefore != nil {
		before = timestamptz(*body.BoughtBefore)
	}
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		deleted, err := q.ClearBoughtShoppingItems(ctx, before)
		if err != nil {
			return result{}, err
		}
		var events []activity.Event
		if deleted > 0 {
			events = append(events, clearedEvent(deleted, body.BoughtBefore))
		}
		return result{status: fiber.StatusOK, body: generated.ClearBoughtResult{Deleted: int(deleted)}, events: events}, nil
	})
}
