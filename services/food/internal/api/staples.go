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
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// stapleNameKey is the unique index that keeps one staple per normalised
// name.
const stapleNameKey = "staples_normalized_name_key"

func stapleExists(name string) error {
	return apiError(fiber.StatusConflict, "staple_exists", "“"+name+"” is already a staple")
}

// stapleState is what the staple list says besides the staple: whether an
// item of its name is needed, in stock, and running low.
type stapleState struct{ onList, inStock, runningLow bool }

func stapleView(staple store.Staple, names map[[16]byte]string, state stapleState) generated.Staple {
	return generated.Staple{
		Id: uuid(staple.ID), Name: staple.Name, NormalizedName: staple.NormalizedName, Amount: staple.Amount,
		StoreId: nullableUUID(staple.StoreID), StoreName: nameOf(names, staple.StoreID), Aisle: staple.Aisle,
		OnList: state.onList, InStock: state.inStock, RunningLow: state.runningLow,
		CreatedAt: staple.CreatedAt.Time.UTC(), UpdatedAt: staple.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(staple.CreatedBy), UpdatedBy: actorOf(staple.UpdatedBy),
	}
}

// stapleWithState reads a staple's view with its current state.
func stapleWithState(ctx context.Context, q *store.Queries, staple store.Staple) (generated.Staple, error) {
	names, err := storeNames(ctx, q)
	if err != nil {
		return generated.Staple{}, err
	}
	rows, err := q.ListStaples(ctx)
	if err != nil {
		return generated.Staple{}, err
	}
	for _, row := range rows {
		if row.ID == staple.ID {
			return stapleView(staple, names, stapleState{onList: row.OnList, inStock: row.InStock, runningLow: row.RunningLow}), nil
		}
	}
	return stapleView(staple, names, stapleState{}), nil
}

func (h *Handler) ListStaples(c fiber.Ctx) error {
	ctx := c.Context()
	rows, err := h.queries.ListStaples(ctx)
	if err != nil {
		return internal(err)
	}
	names, err := storeNames(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	result := generated.StapleList{Items: make([]generated.Staple, 0, len(rows))}
	for _, row := range rows {
		staple := store.Staple{ID: row.ID, Name: row.Name, NormalizedName: row.NormalizedName, Amount: row.Amount, StoreID: row.StoreID,
			Aisle: row.Aisle, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}
		result.Items = append(result.Items, stapleView(staple, names, stapleState{onList: row.OnList, inStock: row.InStock, runningLow: row.RunningLow}))
	}
	return c.JSON(result)
}

func (h *Handler) GetStaple(c fiber.Ctx, stapleID generated.StapleId) error {
	ctx := c.Context()
	staple, err := h.queries.GetStaple(ctx, pgUUID(stapleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Staple")
	}
	if err != nil {
		return internal(err)
	}
	view, err := stapleWithState(ctx, h.queries, staple)
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

// checkAisle reads an optional aisle: an Aisle value, or nil for the rules.
func checkAisle(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if !domain.ValidAisle(*value) {
		return nil, invalid("aisle must be one of the aisles, such as dairy or produce")
	}
	return value, nil
}

func (h *Handler) CreateStaple(c fiber.Ctx, _ generated.CreateStapleParams) error {
	var body generated.CreateStapleRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	name, normalized, err := cleanName("name", body.Name, nameLimit)
	if err != nil {
		return err
	}
	amount, err := cleanText("amount", body.Amount, amountLimit)
	if err != nil {
		return err
	}
	aisle, err := aisleFilter(body.Aisle)
	if err != nil {
		return err
	}
	if body.StoreId != nil && body.StoreName != nil {
		return invalid("give the store by storeId or by storeName, not both")
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		params := store.InsertStapleParams{Name: name, NormalizedName: normalized, Amount: amount, Aisle: aisle, Actor: actor}
		var events []activity.Event
		if body.StoreId != nil || body.StoreName != nil {
			item, storeEvents, err := h.resolveStore(ctx, q, actor, body.StoreId, body.StoreName)
			if err != nil {
				return result{}, err
			}
			params.StoreID, events = item.ID, storeEvents
		}
		staple, err := q.InsertStaple(ctx, params)
		if uniqueViolation(err, stapleNameKey) {
			return result{}, stapleExists(name)
		}
		if err != nil {
			return result{}, err
		}
		view, err := stapleWithState(ctx, q, staple)
		if err != nil {
			return result{}, err
		}
		events = append(events, stapleEvent(stapleCreated, staple, view.StoreName, nil))
		return result{status: fiber.StatusCreated, body: view, events: events}, nil
	})
}

// stapleFields are the UpdateStapleRequest properties; nullable ones may be
// null.
var (
	stapleFields   = []string{"name", "amount", "storeId", "storeName", "aisle"}
	stapleNullable = map[string]bool{"amount": true, "storeId": true, "aisle": true}
)

func (h *Handler) UpdateStaple(c fiber.Ctx, stapleID generated.StapleId, _ generated.UpdateStapleParams) error {
	var body generated.UpdateStapleRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	present := map[string]bool{}
	for _, field := range stapleFields {
		if _, ok := properties[field]; ok {
			if !stapleNullable[field] && isNull(properties, field) {
				return invalid(field + " cannot be null")
			}
			present[field] = true
		}
	}
	if len(present) == 0 {
		return invalid("the update names no staple field")
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
	aisle, err := checkAisle(body.Aisle)
	if err != nil {
		return err
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetStapleForUpdate(ctx, pgUUID(stapleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Staple")
		}
		if err != nil {
			return result{}, err
		}
		next := store.UpdateStapleParams{ID: current.ID, Name: current.Name, NormalizedName: current.NormalizedName, Amount: current.Amount,
			StoreID: current.StoreID, Aisle: current.Aisle, Actor: actor}
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
		if present["aisle"] && !sameText(aisle, current.Aisle) {
			next.Aisle = aisle
			changes = append(changes, "aisle")
		}
		staple := current
		if len(changes) > 0 {
			if staple, err = q.UpdateStaple(ctx, next); uniqueViolation(err, stapleNameKey) {
				return result{}, stapleExists(name)
			} else if err != nil {
				return result{}, err
			}
		}
		view, err := stapleWithState(ctx, q, staple)
		if err != nil {
			return result{}, err
		}
		if len(changes) > 0 {
			events = append(events, stapleEvent(stapleUpdated, staple, view.StoreName, changes))
		}
		return result{status: fiber.StatusOK, body: view, events: events}, nil
	})
}

func (h *Handler) DeleteStaple(c fiber.Ctx, stapleID generated.StapleId, _ generated.DeleteStapleParams) error {
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		staple, err := q.DeleteStaple(ctx, pgUUID(stapleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Staple")
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusNoContent, events: []activity.Event{stapleEvent(stapleDeleted, staple, nil, nil)}}, nil
	})
}

// reasonText is why an item went on the list, in words.
func reasonText(reason generated.RestockReason) string {
	switch reason {
	case generated.RestockReasonUsedUp:
		return "used up"
	case generated.RestockReasonThrownAway:
		return "thrown away"
	}
	return "running low"
}

// restock puts the staple of a normalised name on the shopping list, or
// with force the name itself when it is no staple, unless an item of that
// name is already needed. It returns nil when the name is no staple and
// force is not set.
func (h *Handler) restock(ctx context.Context, q *store.Queries, actor, name, normalized string, reason generated.RestockReason, force bool) (*generated.Restock, []activity.Event, error) {
	staple, err := q.GetStapleByName(ctx, normalized)
	isStaple := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	if !isStaple && !force {
		return nil, nil, nil
	}
	restock := &generated.Restock{Reason: reason}
	if isStaple {
		restock.StapleId = pointer(uuid(staple.ID))
	}
	names, err := storeNames(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	open, err := q.GetOpenShoppingItemByName(ctx, normalized)
	if err == nil {
		restock.Status, restock.ShoppingItem = generated.RestockStatusOnList, shoppingView(open, names)
		restock.Message = quoted(open.Name) + " is already on the list"
		return restock, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	input := newShoppingItem{name: name, normalized: normalized, source: string(generated.ItemSourceManual)}
	if isStaple {
		input.name, input.amount, input.aisle = staple.Name, staple.Amount, staple.Aisle
		input.source, input.sourceRef = string(generated.ItemSourceStaple), pointer(uuid(staple.ID).String())
		if staple.StoreID.Valid {
			// An archived store is left out rather than refusing the change.
			if item, err := q.GetStore(ctx, staple.StoreID); err == nil && !item.ArchivedAt.Valid {
				input.storeID = pointer(types.UUID(item.ID.Bytes))
			} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, err
			}
		}
	}
	added, err := h.addShoppingItems(ctx, q, actor, []newShoppingItem{input})
	if err != nil {
		return nil, nil, err
	}
	row := added.rows[0]
	restock.ShoppingItem = shoppingView(row, added.names)
	restock.Status = generated.RestockStatusAdded
	restock.Message = "Added " + quoted(row.Name) + " to the list: " + reasonText(reason)
	if isStaple {
		restock.Message += " (staple)"
	}
	if added.merged[0] {
		// Another request put it on the list meanwhile.
		restock.Status = generated.RestockStatusMerged
		restock.Message = quoted(row.Name) + " is already on the list"
	}
	// The add's own event says why.
	for index := range added.events {
		event := &added.events[index]
		if event.Subject.ID != uuid(row.ID).String() {
			continue
		}
		if event.Type == shoppingItemAdded {
			event.Summary = restock.Message
		}
		event.Data["reason"] = string(reason)
		if isStaple {
			event.Data["stapleId"] = uuid(staple.ID).String()
		}
	}
	return restock, added.events, nil
}

// MarkRunningLowByName flags the in-stock pantry items of a name as running
// low and, for a staple or with addToList, puts the name on the list.
func (h *Handler) MarkRunningLowByName(c fiber.Ctx, _ generated.MarkRunningLowByNameParams) error {
	var body generated.RunningLowByNameRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	name, normalized, err := cleanName("name", body.Name, nameLimit)
	if err != nil {
		return err
	}
	force := body.AddToList != nil && *body.AddToList
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		response := generated.RunningLowResult{Name: name, NormalizedName: normalized, PantryItems: []generated.PantryItem{}}
		var events []activity.Event
		items, err := q.ListInStockPantryItemsByName(ctx, normalized)
		if err != nil {
			return result{}, err
		}
		for _, item := range items {
			if !item.RunningLowAt.Valid {
				if item, err = q.SetPantryItemRunningLow(ctx, store.SetPantryItemRunningLowParams{ID: item.ID, Running: true, Actor: actor}); err != nil {
					return result{}, err
				}
				events = append(events, pantryEvent(pantryItemRunningLow, item, nil))
				response.Changed = true
			}
			response.PantryItems = append(response.PantryItems, pantryView(item))
		}
		restock, restockEvents, err := h.restock(ctx, q, actor, name, normalized, generated.RestockReasonRunningLow, force)
		if err != nil {
			return result{}, err
		}
		response.Restock = restock
		events = append(events, restockEvents...)
		if restock != nil && restock.Status != generated.RestockStatusOnList {
			response.Changed = true
		}
		if staple, err := q.GetStapleByName(ctx, normalized); err == nil {
			view, err := stapleWithState(ctx, q, staple)
			if err != nil {
				return result{}, err
			}
			response.Staple = &view
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return result{}, err
		}
		return result{status: fiber.StatusOK, body: response, events: events}, nil
	})
}

// moreAtHome clears the running-low flags of a name when more of it comes
// home (added to the pantry or put away from the list).
func moreAtHome(ctx context.Context, q *store.Queries, actor, normalized string) ([]activity.Event, error) {
	items, err := q.ClearRunningLowByName(ctx, store.ClearRunningLowByNameParams{NormalizedName: normalized, Actor: actor})
	if err != nil {
		return nil, err
	}
	events := make([]activity.Event, 0, len(items))
	for _, item := range items {
		event := pantryEvent(pantryItemNotLow, item, nil)
		event.Summary += ": more came home"
		event.Data["reason"] = "more_at_home"
		events = append(events, event)
	}
	return events, nil
}
