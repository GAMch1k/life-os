package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Activity event types Food records: one per change made through the API,
// attributed to the request's actor. A change that changes nothing (marking
// a bought item bought) records nothing; clearing bought items is one event
// with the count. Notes are never recorded.
const (
	shoppingItemAdded    = "food.shopping_item.added"
	shoppingItemMerged   = "food.shopping_item.merged"
	shoppingItemUpdated  = "food.shopping_item.updated"
	shoppingItemBought   = "food.shopping_item.bought"
	shoppingItemReopened = "food.shopping_item.reopened"
	shoppingItemDeleted  = "food.shopping_item.deleted"
	shoppingItemPutAway  = "food.shopping_item.put_away"
	shoppingItemsCleared = "food.shopping_items.cleared"
	storeCreated         = "food.store.created"
	storeUpdated         = "food.store.updated"
	storeArchived        = "food.store.archived"
	storeRestored        = "food.store.restored"
	aisleRuleCreated     = "food.aisle_rule.created"
	aisleRuleDeleted     = "food.aisle_rule.deleted"
	pantryItemAdded      = "food.pantry_item.added"
	pantryItemUpdated    = "food.pantry_item.updated"
	pantryItemUsedUp     = "food.pantry_item.used_up"
	pantryItemThrownAway = "food.pantry_item.thrown_away"
	pantryItemRestocked  = "food.pantry_item.restocked"
	pantryItemDeleted    = "food.pantry_item.deleted"
)

// emit records one event with the request's actor, correlation, and
// causation.
func (h *Handler) emit(c fiber.Ctx, event activity.Event) {
	if h.activity == nil || h.activity.URL == "" {
		return
	}
	trace := activity.FromRequest(c)
	event.CorrelationID, event.CausationID, event.Actor = trace.CorrelationID, trace.CausationID, trace.Actor
	event.Outcome = activity.Succeeded
	event.Summary = activity.Preview(event.Summary, 500)
	h.activity.Emit(c.Context(), event)
}

// newEvent starts an event about the entity id. A creation or a deletion
// happens once per entity, so its event id is derived from the entity (see
// activity.EventID); any other change can repeat, even within one
// transaction (two merges of one item in one batch), and gets a random id.
func newEvent(eventType, id string, once bool) activity.Event {
	if once {
		return activity.Event{ID: activity.EventID(eventType, id), Type: eventType}
	}
	return activity.Event{ID: googleuuid.New(), Type: eventType}
}

func quoted(text string) string { return "“" + activity.Preview(text, 80) + "”" }

func labels(changes []string) string {
	names := map[string]string{"storeId": "store", "storeName": "store", "sortOrder": "sort order", "expiresOn": "expiry date", "openedOn": "opened date"}
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		if label, ok := names[change]; ok {
			change = label
		}
		if len(out) == 0 || out[len(out)-1] != change {
			out = append(out, change)
		}
	}
	return strings.Join(out, ", ")
}

// shoppingEvent describes a shopping item change. storeName names the
// item's store, if any; changes lists the fields a merge or an update
// changed; data adds details such as the merged item.
func shoppingEvent(eventType string, item store.ShoppingItem, storeName *string, changes []string, data map[string]any) activity.Event {
	id := uuid(item.ID).String()
	// An item is added, put away, and deleted once.
	event := newEvent(eventType, id, eventType == shoppingItemAdded || eventType == shoppingItemDeleted || eventType == shoppingItemPutAway)
	event.Subject = activity.Subject{Type: "food_shopping_item", ID: id}
	if data == nil {
		data = map[string]any{}
	}
	data["name"], data["aisle"], data["state"], data["source"] = activity.Preview(item.Name, 200), item.Aisle, item.State, item.Source
	if item.Amount != nil {
		data["amount"] = *item.Amount
	}
	if item.StoreID.Valid {
		data["storeId"] = uuid(item.StoreID).String()
	}
	if storeName != nil {
		data["storeName"] = activity.Preview(*storeName, 200)
	}
	if item.SourceRef != nil {
		data["sourceRef"] = *item.SourceRef
	}
	if changes != nil {
		data["changes"] = changes
	}
	event.Data = data
	name := quoted(item.Name)
	switch eventType {
	case shoppingItemAdded:
		details := []string{}
		if item.Amount != nil {
			details = append(details, activity.Preview(*item.Amount, 40))
		}
		if storeName != nil {
			details = append(details, activity.Preview(*storeName, 40))
		}
		event.Summary = "Added " + name + " to the shopping list"
		if len(details) > 0 {
			event.Summary = "Added " + name + " (" + strings.Join(details, ", ") + ") to the shopping list"
		}
	case shoppingItemMerged:
		event.Summary = name + " was already on the shopping list"
		if len(changes) > 0 {
			event.Summary += ": " + labels(changes) + " updated"
		}
	case shoppingItemUpdated:
		event.Summary = "Shopping item " + name + " updated: " + labels(changes)
	case shoppingItemBought:
		event.Summary = "Bought " + name
	case shoppingItemReopened:
		event.Summary = name + " is needed again"
		if data["mergedFromId"] != nil {
			event.Summary += ", merged into the item already on the list"
		}
	case shoppingItemDeleted:
		event.Summary = "Removed " + name + " from the shopping list"
	case shoppingItemPutAway:
		event.Summary = "Put away " + name
		if place, ok := map[any]string{"fridge": "the fridge", "freezer": "the freezer", "pantry": "the pantry"}[data["location"]]; ok {
			event.Summary += " in " + place
		}
	}
	return event
}

func clearedEvent(deleted int64, boughtBefore *time.Time) activity.Event {
	data := map[string]any{"deleted": deleted}
	summary := fmt.Sprintf("Cleared %d bought %s from the shopping list", deleted, plural(deleted, "item"))
	if boughtBefore != nil {
		data["boughtBefore"] = boughtBefore.UTC().Format(time.RFC3339)
	}
	return activity.Event{ID: googleuuid.New(), Type: shoppingItemsCleared,
		Subject: activity.Subject{Type: "food_shopping_list", ID: "bought"}, Summary: summary, Data: data}
}

func storeEvent(eventType string, item store.Store, changes []string) activity.Event {
	id := uuid(item.ID).String()
	event := newEvent(eventType, id, eventType == storeCreated)
	event.Subject = activity.Subject{Type: "food_store", ID: id}
	event.Data = map[string]any{"name": activity.Preview(item.Name, 200), "sortOrder": item.SortOrder, "archived": item.ArchivedAt.Valid}
	if changes != nil {
		event.Data["changes"] = changes
	}
	name := quoted(item.Name)
	event.Summary = map[string]string{
		storeCreated:  "Store " + name + " created",
		storeUpdated:  "Store " + name + " updated: " + labels(changes),
		storeArchived: "Store " + name + " archived",
		storeRestored: "Store " + name + " restored",
	}[eventType]
	return event
}

func ruleEvent(eventType string, rule store.AisleRule) activity.Event {
	id := uuid(rule.ID).String()
	event := newEvent(eventType, id, true)
	event.Subject = activity.Subject{Type: "food_aisle_rule", ID: id}
	event.Data = map[string]any{"keyword": rule.Keyword, "aisle": rule.Aisle, "match": rule.Match}
	verb := map[string]string{aisleRuleCreated: "created", aisleRuleDeleted: "deleted"}[eventType]
	event.Summary = fmt.Sprintf("Aisle rule %s: %s → %s", verb, quoted(rule.Keyword), rule.Aisle)
	if rule.Match == "substring" {
		event.Summary += " (also inside words)"
	}
	return event
}

func pantryEvent(eventType string, item store.PantryItem, changes []string) activity.Event {
	id := uuid(item.ID).String()
	event := newEvent(eventType, id, eventType == pantryItemAdded || eventType == pantryItemDeleted)
	event.Subject = activity.Subject{Type: "food_pantry_item", ID: id}
	data := map[string]any{"name": activity.Preview(item.Name, 200), "location": item.Location, "state": item.State, "source": item.Source}
	if item.Amount != nil {
		data["amount"] = *item.Amount
	}
	if item.ExpiresOn.Valid {
		data["expiresOn"] = item.ExpiresOn.Time.Format(time.DateOnly)
	}
	if item.SourceRef != nil {
		data["sourceRef"] = *item.SourceRef
	}
	if item.FromShoppingItemID.Valid {
		data["fromShoppingItemId"] = uuid(item.FromShoppingItemID).String()
	}
	if changes != nil {
		data["changes"] = changes
	}
	event.Data = data
	name := quoted(item.Name)
	switch eventType {
	case pantryItemAdded:
		details := item.Location
		if item.ExpiresOn.Valid {
			details += ", expires " + item.ExpiresOn.Time.Format(time.DateOnly)
		}
		event.Summary = "Added " + name + " to the pantry (" + details + ")"
	case pantryItemUpdated:
		event.Summary = "Pantry item " + name + " updated: " + labels(changes)
	case pantryItemUsedUp:
		event.Summary = "Used up " + name
	case pantryItemThrownAway:
		event.Summary = "Threw away " + name
	case pantryItemRestocked:
		event.Summary = name + " is back in stock"
	case pantryItemDeleted:
		event.Summary = "Deleted pantry item " + name
	}
	return event
}

func plural(count int64, word string) string {
	if count == 1 {
		return word
	}
	return word + "s"
}
