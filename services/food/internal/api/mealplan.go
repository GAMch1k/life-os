package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// planDays is the longest date range the meal plan is read or used for.
const planDays = 31

// mealTextLimit bounds a free-text meal.
const mealTextLimit = 200

func mealPlanView(entry store.MealPlanEntry) generated.MealPlanEntry {
	view := generated.MealPlanEntry{
		Id: uuid(entry.ID), Date: *nullableDate(entry.Day), Slot: generated.MealSlot(entry.Slot), RecipeId: nullableUUID(entry.RecipeID),
		Title: entry.Title, Note: entry.Note,
		CreatedAt: entry.CreatedAt.Time.UTC(), UpdatedAt: entry.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(entry.CreatedBy), UpdatedBy: actorOf(entry.UpdatedBy),
	}
	if entry.Servings != nil {
		view.Servings = pointer(int(*entry.Servings))
	}
	return view
}

// planRange checks a from/to date range: from no later than to, at most
// planDays days.
func planRange(from, to time.Time) (pgtype.Date, pgtype.Date, error) {
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	if to.Before(from) {
		return pgtype.Date{}, pgtype.Date{}, invalid("to must not be before from")
	}
	if to.Sub(from) >= planDays*24*time.Hour {
		return pgtype.Date{}, pgtype.Date{}, invalid(fmt.Sprintf("the range must be at most %d days", planDays))
	}
	return pgtype.Date{Time: from, Valid: true}, pgtype.Date{Time: to, Valid: true}, nil
}

func (h *Handler) ListMealPlan(c fiber.Ctx, params generated.ListMealPlanParams) error {
	from, to, err := planRange(params.From.Time, params.To.Time)
	if err != nil {
		return err
	}
	entries, err := h.queries.ListMealPlan(c.Context(), store.ListMealPlanParams{FromDay: from, ThroughDay: to})
	if err != nil {
		return internal(err)
	}
	result := generated.MealPlanList{Items: make([]generated.MealPlanEntry, 0, len(entries))}
	for _, entry := range entries {
		result.Items = append(result.Items, mealPlanView(entry))
	}
	return c.JSON(result)
}

func sameMeal(entry store.MealPlanEntry, next store.UpsertMealPlanEntryParams) bool {
	return entry.RecipeID == next.RecipeID && entry.Title == next.Title && sameServings(entry.Servings, next.Servings) && sameText(entry.Note, next.Note)
}

// SetMealPlanEntry plans a recipe or a free-text meal in a day's slot,
// replacing what the slot held.
func (h *Handler) SetMealPlanEntry(c fiber.Ctx, date generated.PlanDate, slot generated.PlanSlot, _ generated.SetMealPlanEntryParams) error {
	if !slot.Valid() {
		return invalid("slot must be breakfast, lunch, or dinner")
	}
	var body generated.SetMealPlanEntryRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	if (body.RecipeId == nil) == (body.Text == nil) {
		return invalid("give recipeId or text, exactly one")
	}
	var text *string
	var err error
	if body.Text != nil {
		if text, err = cleanText("text", body.Text, mealTextLimit); err != nil {
			return err
		}
		if text == nil {
			return invalid("text must not be empty")
		}
	}
	servings, err := checkServings(body.Servings)
	if err != nil {
		return err
	}
	note, err := cleanText("note", body.Note, noteLimit)
	if err != nil {
		return err
	}
	day := pgDate(date)
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		next := store.UpsertMealPlanEntryParams{Day: day, Slot: string(slot), Servings: servings, Note: note, Actor: actor}
		if body.RecipeId != nil {
			recipe, err := q.GetRecipe(ctx, pgUUID(*body.RecipeId))
			if errors.Is(err, pgx.ErrNoRows) {
				return result{}, apiError(fiber.StatusBadRequest, "unknown_recipe", "No recipe has id "+body.RecipeId.String())
			}
			if err != nil {
				return result{}, err
			}
			next.RecipeID, next.Title = recipe.ID, recipe.Title
		} else {
			next.Title = *text
		}
		current, err := q.GetMealPlanEntryForUpdate(ctx, store.GetMealPlanEntryForUpdateParams{Day: day, Slot: string(slot)})
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result{}, err
		}
		if found && sameMeal(current, next) {
			return result{status: fiber.StatusOK, body: mealPlanView(current)}, nil
		}
		entry, err := q.UpsertMealPlanEntry(ctx, next)
		if err != nil {
			return result{}, err
		}
		var previous *store.MealPlanEntry
		if found {
			previous = &current
		}
		return result{status: fiber.StatusOK, body: mealPlanView(entry), events: []activity.Event{mealPlanEvent(mealPlanned, entry, previous)}}, nil
	})
}

func (h *Handler) ClearMealPlanEntry(c fiber.Ctx, date generated.PlanDate, slot generated.PlanSlot, _ generated.ClearMealPlanEntryParams) error {
	if !slot.Valid() {
		return invalid("slot must be breakfast, lunch, or dinner")
	}
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		entry, err := q.DeleteMealPlanEntry(ctx, store.DeleteMealPlanEntryParams{Day: pgDate(date), Slot: string(slot)})
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, apiError(fiber.StatusNotFound, "not_found", "Nothing is planned for "+string(slot)+" on "+date.Format(time.DateOnly))
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusNoContent, events: []activity.Event{mealPlanEvent(mealCleared, entry, nil)}}, nil
	})
}
