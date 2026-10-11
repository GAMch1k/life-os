package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// putAwayItem is one checked item of a put-away request.
type putAwayItem struct {
	id        pgtype.UUID
	location  *string
	expiresOn pgtype.Date
	// amount replaces the shopping item's when set (amountGiven); nil then
	// means none.
	amount      *string
	amountGiven bool
}

func checkPutAwayItem(item generated.PutAwayItem) (putAwayItem, error) {
	result := putAwayItem{id: pgUUID(item.Id), expiresOn: optionalDate(item.ExpiresOn), amountGiven: item.Amount != nil}
	var err error
	if result.location, err = locationFilter(item.Location); err != nil {
		return result, err
	}
	result.amount, err = cleanText("amount", item.Amount, amountLimit)
	return result, err
}

func notBought(name string) error {
	return apiError(fiber.StatusConflict, "not_bought", "“"+name+"” is still needed: only bought items can be put away")
}

// PutAwayShoppingItems moves bought items from the shopping list into the
// pantry, all in one transaction: each becomes an in-stock pantry item (in
// the location given, or the usual place of its aisle) and leaves the list.
func (h *Handler) PutAwayShoppingItems(c fiber.Ctx, _ generated.PutAwayShoppingItemsParams) error {
	var body generated.PutAwayRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	if len(body.Items) == 0 || len(body.Items) > batchLimit {
		return invalid(fmt.Sprintf("items must hold 1 to %d items", batchLimit))
	}
	inputs := make([]putAwayItem, len(body.Items))
	seen := make(map[[16]byte]bool, len(body.Items))
	for index, item := range body.Items {
		input, err := checkPutAwayItem(item)
		if err != nil {
			return itemError(index, err)
		}
		if seen[input.id.Bytes] {
			return itemError(index, invalid("the item is named twice"))
		}
		seen[input.id.Bytes] = true
		inputs[index] = input
	}
	// Items are locked in id order, so two requests naming the same items
	// lock them in the same order; the response keeps the request's order.
	order := make([]int, len(inputs))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool {
		return bytes.Compare(inputs[order[i]].id.Bytes[:], inputs[order[j]].id.Bytes[:]) < 0
	})
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		names, err := storeNames(ctx, q)
		if err != nil {
			return result{}, err
		}
		pantry := make([]store.PantryItem, len(inputs))
		events := make([][]activity.Event, len(inputs))
		for _, index := range order {
			input := inputs[index]
			item, err := q.GetShoppingItemForUpdate(ctx, input.id)
			if errors.Is(err, pgx.ErrNoRows) {
				return result{}, itemError(index, notFound("Shopping item"))
			}
			if err != nil {
				return result{}, err
			}
			if item.State != "bought" {
				return result{}, itemError(index, notBought(item.Name))
			}
			params := store.InsertPantryItemParams{Name: item.Name, NormalizedName: item.NormalizedName, Amount: item.Amount,
				Location: domain.PantryLocation(item.Aisle), ExpiresOn: input.expiresOn, Source: item.Source, SourceRef: item.SourceRef,
				FromShoppingItemID: item.ID, Actor: actor}
			if input.location != nil {
				params.Location = *input.location
			}
			if input.amountGiven {
				params.Amount = input.amount
			}
			added, err := q.InsertPantryItem(ctx, params)
			if err != nil {
				return result{}, err
			}
			if _, err := q.DeleteShoppingItem(ctx, item.ID); err != nil {
				return result{}, err
			}
			pantry[index] = added
			data := map[string]any{"pantryItemId": uuid(added.ID).String(), "location": added.Location}
			events[index] = []activity.Event{
				shoppingEvent(shoppingItemPutAway, item, nameOf(names, item.StoreID), nil, data),
				pantryEvent(pantryItemAdded, added, nil),
			}
		}
		response := generated.PutAwayResponse{Items: make([]generated.PantryItem, len(pantry))}
		var all []activity.Event
		for index, item := range pantry {
			response.Items[index] = pantryView(item)
			all = append(all, events[index]...)
		}
		return result{status: fiber.StatusOK, body: response, events: all}, nil
	})
}
