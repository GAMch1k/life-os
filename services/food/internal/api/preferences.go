package api

import (
	"context"
	"fmt"
	"slices"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Preference limits (the contract's).
const (
	dislikesLimit     = 50
	dietaryNotesLimit = 1000
)

func preferencesView(preferences store.FoodPreference) generated.Preferences {
	view := generated.Preferences{Methods: cookingMethods(preferences.Methods), Dislikes: preferences.Dislikes, DietaryNotes: preferences.DietaryNotes,
		UpdatedAt: preferences.UpdatedAt.Time.UTC(), UpdatedBy: actorOf(preferences.UpdatedBy)}
	if view.Dislikes == nil {
		view.Dislikes = []string{}
	}
	return view
}

func (h *Handler) GetPreferences(c fiber.Ctx) error {
	preferences, err := h.queries.GetPreferences(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(preferencesView(preferences))
}

// preferredMethods keeps known methods once each, in the order given (the
// most preferred first).
func preferredMethods(values []generated.CookingMethod) ([]string, error) {
	if len(values) > len(domain.Methods) {
		return nil, invalid(fmt.Sprintf("methods must hold at most %d methods", len(domain.Methods)))
	}
	out := []string{}
	for index, value := range values {
		if !domain.ValidMethod(string(value)) {
			return nil, invalid(fmt.Sprintf("methods[%d] must be a cooking method", index))
		}
		if !slices.Contains(out, string(value)) {
			out = append(out, string(value))
		}
	}
	return out, nil
}

// dislikedNames keeps disliked ingredients as written, once per normalised
// name.
func dislikedNames(values []string) ([]string, error) {
	if len(values) > dislikesLimit {
		return nil, invalid(fmt.Sprintf("dislikes must hold at most %d names", dislikesLimit))
	}
	out := []string{}
	seen := map[string]bool{}
	for index, value := range values {
		name, normalized, err := cleanName(fmt.Sprintf("dislikes[%d]", index), value, nameLimit)
		if err != nil {
			return nil, err
		}
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func (h *Handler) UpdatePreferences(c fiber.Ctx, _ generated.UpdatePreferencesParams) error {
	var body generated.PreferencesRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	for _, field := range []string{"methods", "dislikes"} {
		if _, ok := properties[field]; !ok || isNull(properties, field) {
			return invalid(field + " is required")
		}
	}
	methods, err := preferredMethods(body.Methods)
	if err != nil {
		return err
	}
	dislikes, err := dislikedNames(body.Dislikes)
	if err != nil {
		return err
	}
	notes, err := cleanText("dietaryNotes", body.DietaryNotes, dietaryNotesLimit)
	if err != nil {
		return err
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetPreferences(ctx)
		if err != nil {
			return result{}, err
		}
		var changes []string
		if !slices.Equal(methods, current.Methods) {
			changes = append(changes, "methods")
		}
		if !slices.Equal(dislikes, current.Dislikes) {
			changes = append(changes, "dislikes")
		}
		if !sameText(notes, current.DietaryNotes) {
			changes = append(changes, "dietaryNotes")
		}
		if len(changes) == 0 {
			return result{status: fiber.StatusOK, body: preferencesView(current)}, nil
		}
		updated, err := q.UpdatePreferences(ctx, store.UpdatePreferencesParams{Methods: methods, Dislikes: dislikes, DietaryNotes: notes, Actor: actor})
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusOK, body: preferencesView(updated), events: []activity.Event{preferencesEvent(updated, changes)}}, nil
	})
}
