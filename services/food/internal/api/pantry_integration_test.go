package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

func TestPantryExpiringWithinDaysUsesTheOwnersToday(t *testing.T) {
	// fixedNow is 00:30 on 11 October in Berlin, 22:30 on 10 October in UTC.
	c := newAPIClient(t)
	expect[generated.AddPantryItemsResponse](c, http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{
		map[string]any{"name": "Yesterday's yogurt", "expiresOn": "2026-10-10"},
		map[string]any{"name": "Today's milk", "expiresOn": "2026-10-11"},
		map[string]any{"name": "Tomorrow's cream", "expiresOn": "2026-10-12"},
		map[string]any{"name": "Next week's cheese", "expiresOn": "2026-10-18"},
		map[string]any{"name": "Rice"},
	}}, http.StatusCreated)
	cases := map[string]string{
		// Today in Berlin is the 11th: within 0 days means the 11th or earlier.
		"expiringWithinDays=0": "Yesterday's yogurt, Today's milk",
		// Within 1 day reaches the 12th; counted from UTC's 10th it would stop
		// at the 11th.
		"expiringWithinDays=1": "Yesterday's yogurt, Today's milk, Tomorrow's cream",
		"expiringWithinDays=7": "Yesterday's yogurt, Today's milk, Tomorrow's cream, Next week's cheese",
		// Without the filter: soonest expiry first, no expiry last.
		"": "Yesterday's yogurt, Today's milk, Tomorrow's cream, Next week's cheese, Rice",
	}
	for query, want := range cases {
		if got := pantryNames(c.pantry(query).Items); got != want {
			t.Errorf("%s: %s, want %s", query, got, want)
		}
	}
	// The same items in UTC: within 1 day of the 10th is the 11th.
	c.handler.location = time.UTC
	if got := pantryNames(c.pantry("expiringWithinDays=1").Items); got != "Yesterday's yogurt, Today's milk" {
		t.Errorf("in UTC: %s", got)
	}
	c.expectError(http.MethodGet, "/v1/pantry-items?expiringWithinDays=-1", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/pantry-items?expiringWithinDays=400", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/pantry-items?expiringWithinDays=3&state=used_up", nil, http.StatusBadRequest, "invalid_request")
}

func TestPantryLifecycle(t *testing.T) {
	c := newAPIClient(t)
	milk := c.add(map[string]any{"name": "Milk"})[0].Item
	added := expect[generated.AddPantryItemsResponse](c, http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{
		map[string]any{"name": "Milk", "amount": "1 l", "expiresOn": "2026-10-15", "fromShoppingItemId": milk.Id, "source": "capture", "sourceRef": "inbox-7"},
		map[string]any{"name": "Milk", "expiresOn": "2026-10-20"},
		map[string]any{"name": "Fischstäbchen"},
		map[string]any{"name": "Nudeln"},
		map[string]any{"name": "Batteries", "location": "other"},
		map[string]any{"name": "Kerzen"},
	}}, http.StatusCreated).Items
	if len(added) != 6 {
		t.Fatalf("added = %+v", added)
	}
	first := added[0]
	if first.Location != generated.PantryLocationFridge || *first.Amount != "1 l" || first.State != generated.PantryItemStateInStock || first.EndedAt != nil ||
		*first.FromShoppingItemId != milk.Id || first.Source != generated.ItemSourceCapture || *first.SourceRef != "inbox-7" ||
		first.ExpiresOn.Format("2006-01-02") != "2026-10-15" {
		t.Fatalf("first = %+v", first)
	}
	// Pantry items never merge; a missing location is the aisle's usual place.
	if added[1].Id == first.Id {
		t.Fatal("pantry items merged")
	}
	locations := map[string]generated.PantryLocation{"Fischstäbchen": generated.PantryLocationFreezer, "Nudeln": generated.PantryLocationPantry,
		"Batteries": generated.PantryLocationOther, "Kerzen": generated.PantryLocationOther}
	for _, item := range added[2:] {
		if item.Location != locations[item.Name] {
			t.Errorf("%s is in %s", item.Name, item.Location)
		}
	}
	if got := pantryNames(c.pantry("location=fridge").Items); got != "Milk, Milk" {
		t.Fatalf("fridge = %s", got)
	}

	// Edit, use up, throw away, restock.
	path := "/v1/pantry-items/" + first.Id.String()
	opened := expect[generated.PantryItem](c, http.MethodPatch, path, map[string]any{"openedOn": "2026-10-11", "expiresOn": nil, "note": "opened", "location": "fridge"}, http.StatusOK)
	if opened.OpenedOn == nil || opened.ExpiresOn != nil || *opened.Note != "opened" {
		t.Fatalf("opened = %+v", opened)
	}
	used := expect[generated.PantryItem](c, http.MethodPost, path+"/used-up", nil, http.StatusOK)
	if used.State != generated.PantryItemStateUsedUp || used.EndedAt == nil {
		t.Fatalf("used = %+v", used)
	}
	if again := expect[generated.PantryItem](c, http.MethodPost, path+"/used-up", nil, http.StatusOK); !again.EndedAt.Equal(*used.EndedAt) {
		t.Fatalf("used again = %+v", again)
	}
	thrown := expect[generated.PantryItem](c, http.MethodPost, path+"/thrown-away", nil, http.StatusOK)
	if thrown.State != generated.PantryItemStateThrownAway || !thrown.EndedAt.Equal(*used.EndedAt) {
		t.Fatalf("thrown = %+v", thrown)
	}
	// Ended items are kept, after the in-stock ones (no expiry: by name).
	if got := pantryNames(c.pantry("").Items); got != "Milk, Batteries, Fischstäbchen, Kerzen, Nudeln, Milk" {
		t.Fatalf("all = %s", got)
	}
	if got := c.pantry("state=thrown_away").Items; len(got) != 1 || got[0].Id != first.Id {
		t.Fatalf("thrown away = %+v", got)
	}
	if got := pantryNames(c.pantry("state=in_stock&limit=2").Items); got != "Milk, Batteries" {
		t.Fatalf("in stock = %s", got)
	}
	restocked := expect[generated.PantryItem](c, http.MethodPost, path+"/restock", nil, http.StatusOK)
	if restocked.State != generated.PantryItemStateInStock || restocked.EndedAt != nil {
		t.Fatalf("restocked = %+v", restocked)
	}

	// Paging walks in-stock then ended items without gaps or repeats.
	expect[generated.PantryItem](c, http.MethodPost, "/v1/pantry-items/"+added[3].Id.String()+"/used-up", nil, http.StatusOK)
	want := pantryNames(c.pantry("").Items)
	var walked []generated.PantryItem
	query := "limit=2"
	for pages := 0; ; pages++ {
		page := c.pantry(query)
		walked = append(walked, page.Items...)
		if page.NextCursor == nil {
			break
		}
		if pages > 5 {
			t.Fatal("too many pages")
		}
		query = "limit=2&cursor=" + *page.NextCursor
	}
	if got := pantryNames(walked); got != want {
		t.Fatalf("paged = %s, want %s", got, want)
	}

	for _, body := range []any{map[string]any{}, map[string]any{"name": nil}, map[string]any{"location": nil}, map[string]any{"location": "cellar"}, map[string]any{"expiresOn": "soon"}} {
		c.expectError(http.MethodPatch, path, body, http.StatusBadRequest, "invalid_request")
	}
	c.expectError(http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{map[string]any{"name": "x", "fromShoppingItemId": first.Id}}}, http.StatusBadRequest, "unknown_shopping_item")
	c.expectError(http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{map[string]any{"name": "x", "location": "cellar"}}}, http.StatusBadRequest, "invalid_request")
	if status, _ := c.do(http.MethodDelete, path, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	c.expectError(http.MethodGet, path, nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodPost, path+"/restock", nil, http.StatusNotFound, "not_found")
}
