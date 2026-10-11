package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// expiringLimit bounds expiringWithinDays.
const expiringLimit = 366

func pantryView(item store.PantryItem) generated.PantryItem {
	return generated.PantryItem{
		Id: uuid(item.ID), Name: item.Name, NormalizedName: item.NormalizedName, Amount: item.Amount,
		Location: generated.PantryLocation(item.Location), ExpiresOn: nullableDate(item.ExpiresOn), OpenedOn: nullableDate(item.OpenedOn),
		Note: item.Note, State: generated.PantryItemState(item.State), EndedAt: nullableTime(item.EndedAt),
		Source: generated.ItemSource(item.Source), SourceRef: item.SourceRef, FromShoppingItemId: nullableUUID(item.FromShoppingItemID),
		CreatedAt: item.CreatedAt.Time.UTC(), UpdatedAt: item.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(item.CreatedBy), UpdatedBy: actorOf(item.UpdatedBy),
	}
}

func locationFilter(value *generated.PantryLocation) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if !value.Valid() {
		return nil, invalid("location must be fridge, freezer, pantry, or other")
	}
	return pointer(string(*value)), nil
}

func (h *Handler) ListPantryItems(c fiber.Ctx, params generated.ListPantryItemsParams) error {
	limit, err := pageLimit(params.Limit)
	if err != nil {
		return err
	}
	// One row beyond the page tells whether another page exists.
	query := store.ListPantryItemsParams{PageLimit: int32(limit + 1)}
	if params.State != nil {
		if !params.State.Valid() {
			return invalid("state must be in_stock, used_up, or thrown_away")
		}
		query.State = pointer(string(*params.State))
	}
	if query.Location, err = locationFilter(params.Location); err != nil {
		return err
	}
	if days := params.ExpiringWithinDays; days != nil {
		if *days < 0 || *days > expiringLimit {
			return invalid(fmt.Sprintf("expiringWithinDays must be between 0 and %d", expiringLimit))
		}
		if query.State != nil && *query.State != "in_stock" {
			return invalid("expiringWithinDays lists in-stock items only")
		}
		query.ExpiresBy = pgtype.Date{Time: h.today().AddDate(0, 0, *days), Valid: true}
	}
	if params.Cursor != nil {
		var position pantryCursor
		id, err := decodeCursor(*params.Cursor, &position)
		if err != nil {
			return err
		}
		if err := position.after(&query, id); err != nil {
			return err
		}
	}
	items, err := h.queries.ListPantryItems(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	result := generated.PantryItemList{Items: make([]generated.PantryItem, 0, min(len(items), limit))}
	for index, item := range items {
		if index == limit {
			result.NextCursor = pointer(encodeCursor(pantryPosition(items[index-1])))
			break
		}
		result.Items = append(result.Items, pantryView(item))
	}
	return c.JSON(result)
}

func (h *Handler) GetPantryItem(c fiber.Ctx, itemID generated.ItemId) error {
	item, err := h.queries.GetPantryItem(c.Context(), pgUUID(itemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Pantry item")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(pantryView(item))
}

func checkNewPantryItem(item generated.NewPantryItem, actor string) (store.InsertPantryItemParams, error) {
	params := store.InsertPantryItemParams{ExpiresOn: optionalDate(item.ExpiresOn), OpenedOn: optionalDate(item.OpenedOn), Actor: actor}
	var err error
	if params.Name, params.NormalizedName, err = cleanName("name", item.Name, nameLimit); err != nil {
		return params, err
	}
	if params.Amount, err = cleanText("amount", item.Amount, amountLimit); err != nil {
		return params, err
	}
	if params.Note, err = cleanText("note", item.Note, noteLimit); err != nil {
		return params, err
	}
	location, err := locationFilter(item.Location)
	if err != nil {
		return params, err
	}
	if location != nil {
		params.Location = *location
	}
	if item.FromShoppingItemId != nil {
		params.FromShoppingItemID = pgUUID(*item.FromShoppingItemId)
	}
	params.Source, params.SourceRef, err = itemSource(item.Source, item.SourceRef)
	return params, err
}

// AddPantryItems adds a batch of pantry items. They never merge.
func (h *Handler) AddPantryItems(c fiber.Ctx, _ generated.AddPantryItemsParams) error {
	var body generated.AddPantryItemsRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	if len(body.Items) == 0 || len(body.Items) > batchLimit {
		return invalid(fmt.Sprintf("items must hold 1 to %d items", batchLimit))
	}
	actor := requestActor(c)
	inputs := make([]store.InsertPantryItemParams, len(body.Items))
	for index, item := range body.Items {
		params, err := checkNewPantryItem(item, actor)
		if err != nil {
			return itemError(index, err)
		}
		inputs[index] = params
	}
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		var rules []domain.Rule
		response := generated.AddPantryItemsResponse{Items: make([]generated.PantryItem, 0, len(inputs))}
		var events []activity.Event
		for index, params := range inputs {
			if params.Location == "" {
				// No location: the usual place of the item's aisle.
				if rules == nil {
					var err error
					if rules, err = aisleRules(ctx, q); err != nil {
						return result{}, err
					}
				}
				aisle, _, _ := domain.ChooseAisle(params.NormalizedName, rules)
				params.Location = domain.PantryLocation(aisle)
			}
			if params.FromShoppingItemID.Valid {
				if _, err := q.GetShoppingItem(ctx, params.FromShoppingItemID); errors.Is(err, pgx.ErrNoRows) {
					return result{}, apiError(fiber.StatusBadRequest, "unknown_shopping_item", fmt.Sprintf("items[%d]: no shopping item has id %s", index, uuid(params.FromShoppingItemID)))
				} else if err != nil {
					return result{}, err
				}
			}
			item, err := q.InsertPantryItem(ctx, params)
			if err != nil {
				return result{}, err
			}
			response.Items = append(response.Items, pantryView(item))
			events = append(events, pantryEvent(pantryItemAdded, item, nil))
		}
		return result{status: fiber.StatusCreated, body: response, events: events}, nil
	})
}

// pantryFields are the UpdatePantryItemRequest properties; nullable ones may
// be null.
var (
	pantryFields   = []string{"name", "amount", "location", "expiresOn", "openedOn", "note"}
	pantryNullable = map[string]bool{"amount": true, "expiresOn": true, "openedOn": true, "note": true}
)

func (h *Handler) UpdatePantryItem(c fiber.Ctx, itemID generated.ItemId, _ generated.UpdatePantryItemParams) error {
	var body generated.UpdatePantryItemRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	present := map[string]bool{}
	for _, field := range pantryFields {
		if _, ok := properties[field]; ok {
			if !pantryNullable[field] && isNull(properties, field) {
				return invalid(field + " cannot be null")
			}
			present[field] = true
		}
	}
	if len(present) == 0 {
		return invalid("the update names no pantry item field")
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
	location, err := locationFilter(body.Location)
	if err != nil {
		return err
	}
	expiresOn, openedOn := optionalDate(body.ExpiresOn), optionalDate(body.OpenedOn)
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetPantryItemForUpdate(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Pantry item")
		}
		if err != nil {
			return result{}, err
		}
		next := store.UpdatePantryItemParams{ID: current.ID, Name: current.Name, NormalizedName: current.NormalizedName, Amount: current.Amount,
			Location: current.Location, ExpiresOn: current.ExpiresOn, OpenedOn: current.OpenedOn, Note: current.Note, Actor: actor}
		var changes []string
		if present["name"] && name != current.Name {
			next.Name, next.NormalizedName = name, normalized
			changes = append(changes, "name")
		}
		if present["amount"] && !sameText(amount, current.Amount) {
			next.Amount = amount
			changes = append(changes, "amount")
		}
		if location != nil && *location != current.Location {
			next.Location = *location
			changes = append(changes, "location")
		}
		if present["expiresOn"] && expiresOn != current.ExpiresOn {
			next.ExpiresOn = expiresOn
			changes = append(changes, "expiresOn")
		}
		if present["openedOn"] && openedOn != current.OpenedOn {
			next.OpenedOn = openedOn
			changes = append(changes, "openedOn")
		}
		if present["note"] && !sameText(note, current.Note) {
			next.Note = note
			changes = append(changes, "note")
		}
		if len(changes) == 0 {
			return result{status: fiber.StatusOK, body: pantryView(current)}, nil
		}
		item, err := q.UpdatePantryItem(ctx, next)
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusOK, body: pantryView(item), events: []activity.Event{pantryEvent(pantryItemUpdated, item, changes)}}, nil
	})
}

func (h *Handler) MarkPantryItemUsedUp(c fiber.Ctx, itemID generated.ItemId, _ generated.MarkPantryItemUsedUpParams) error {
	return h.setPantryState(c, itemID, "used_up", pantryItemUsedUp)
}

func (h *Handler) MarkPantryItemThrownAway(c fiber.Ctx, itemID generated.ItemId, _ generated.MarkPantryItemThrownAwayParams) error {
	return h.setPantryState(c, itemID, "thrown_away", pantryItemThrownAway)
}

func (h *Handler) RestockPantryItem(c fiber.Ctx, itemID generated.ItemId, _ generated.RestockPantryItemParams) error {
	return h.setPantryState(c, itemID, "in_stock", pantryItemRestocked)
}

// setPantryState moves an item to state; an item already there is returned
// unchanged and records nothing.
func (h *Handler) setPantryState(c fiber.Ctx, itemID generated.ItemId, state, eventType string) error {
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetPantryItemForUpdate(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Pantry item")
		}
		if err != nil {
			return result{}, err
		}
		if current.State == state {
			return result{status: fiber.StatusOK, body: pantryView(current)}, nil
		}
		item, err := q.SetPantryItemState(ctx, store.SetPantryItemStateParams{ID: current.ID, State: state, Actor: actor})
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusOK, body: pantryView(item), events: []activity.Event{pantryEvent(eventType, item, nil)}}, nil
	})
}

func (h *Handler) DeletePantryItem(c fiber.Ctx, itemID generated.ItemId, _ generated.DeletePantryItemParams) error {
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		item, err := q.DeletePantryItem(ctx, pgUUID(itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Pantry item")
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusNoContent, events: []activity.Event{pantryEvent(pantryItemDeleted, item, nil)}}, nil
	})
}
