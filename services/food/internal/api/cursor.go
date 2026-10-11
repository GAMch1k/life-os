package api

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Cursors are opaque to clients: base64url of a small JSON object holding the
// last row's place in its list's order (see the List queries' after_*
// arguments).
type shoppingCursor struct {
	State    string     `json:"s"`
	Rank     int16      `json:"r,omitempty"`
	Name     string     `json:"n,omitempty"`
	BoughtAt *time.Time `json:"b,omitempty"`
	ID       string     `json:"i"`
}

type pantryCursor struct {
	InStock bool `json:"s"`
	// ExpiresOn is empty for an item without an expiry date.
	ExpiresOn string     `json:"e,omitempty"`
	Name      string     `json:"n,omitempty"`
	EndedAt   *time.Time `json:"x,omitempty"`
	ID        string     `json:"i"`
}

type recipeCursor struct {
	Title string `json:"t"`
	ID    string `json:"i"`
}

func invalidCursor() error {
	return apiError(fiber.StatusBadRequest, "invalid_cursor", "The cursor is not one this list returned")
}

func encodeCursor(position any) string {
	raw, _ := json.Marshal(position)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(cursor string, target any) (pgtype.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(raw, target) != nil {
		return pgtype.UUID{}, invalidCursor()
	}
	var id string
	switch position := target.(type) {
	case *shoppingCursor:
		id = position.ID
	case *pantryCursor:
		id = position.ID
	case *recipeCursor:
		id = position.ID
	}
	parsed, err := googleuuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, invalidCursor()
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func shoppingPosition(item store.ShoppingItem) shoppingCursor {
	position := shoppingCursor{State: item.State, ID: uuid(item.ID).String()}
	if item.State == "needed" {
		position.Rank, position.Name = item.AisleRank, item.NormalizedName
	} else {
		position.BoughtAt = nullableTime(item.BoughtAt)
	}
	return position
}

// after applies a shopping list cursor to the list query.
func (position shoppingCursor) after(params *store.ListShoppingItemsParams, id pgtype.UUID) error {
	params.AfterID = id
	switch {
	case position.State == "needed" && position.Name != "":
		params.AfterState, params.AfterRank, params.AfterName = pointer("needed"), pointer(position.Rank), pointer(position.Name)
	case position.State == "bought" && position.BoughtAt != nil:
		params.AfterState, params.AfterBoughtAt = pointer("bought"), timestamptz(*position.BoughtAt)
	default:
		return invalidCursor()
	}
	return nil
}

func pantryPosition(item store.PantryItem) pantryCursor {
	position := pantryCursor{InStock: item.State == "in_stock", ID: uuid(item.ID).String()}
	if position.InStock {
		position.Name = item.NormalizedName
		if item.ExpiresOn.Valid {
			position.ExpiresOn = item.ExpiresOn.Time.Format(time.DateOnly)
		}
	} else {
		position.EndedAt = nullableTime(item.EndedAt)
	}
	return position
}

// after applies a pantry cursor to the list query. An in-stock item without
// an expiry date sorts as infinity.
func (position pantryCursor) after(params *store.ListPantryItemsParams, id pgtype.UUID) error {
	params.AfterID, params.AfterInStock = id, pointer(position.InStock)
	switch {
	case position.InStock && position.Name != "":
		params.AfterName = pointer(position.Name)
		params.AfterExpiresOn = pgtype.Date{InfinityModifier: pgtype.Infinity, Valid: true}
		if position.ExpiresOn != "" {
			date, err := time.Parse(time.DateOnly, position.ExpiresOn)
			if err != nil {
				return invalidCursor()
			}
			params.AfterExpiresOn = pgtype.Date{Time: date, Valid: true}
		}
	case !position.InStock && position.EndedAt != nil:
		params.AfterEndedAt = timestamptz(*position.EndedAt)
	default:
		return invalidCursor()
	}
	return nil
}

// pageLimit checks a list's limit: 1 to 200, 100 by default.
func pageLimit(value *int) (int, error) {
	if value == nil {
		return 100, nil
	}
	if *value < 1 || *value > 200 {
		return 0, invalid("limit must be between 1 and 200")
	}
	return *value, nil
}
