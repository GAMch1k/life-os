import assert from "node:assert/strict";
import test from "node:test";

import {
  EVENT_TYPES,
  eventLabel,
  sourceDescription,
  sourceLabel,
  subjectLabel,
} from "./activity-log";

test("Food events read with the item, store, or rule they are about", () => {
  assert.equal(
    eventLabel({
      type: "food.shopping_item.added",
      data: { name: "Vollmilch", aisle: "dairy", amount: "2 l" },
    }),
    "Added to the shopping list: Vollmilch",
  );
  assert.equal(
    eventLabel({ type: "food.shopping_item.bought", data: { name: "Milch" } }),
    "Bought: Milch",
  );
  assert.equal(
    eventLabel({
      type: "food.shopping_item.reopened",
      data: { name: "Milch", mergedFromId: "x" },
    }),
    "Needed again: Milch",
  );
  assert.equal(
    eventLabel({ type: "food.shopping_items.cleared", data: { deleted: 3 } }),
    "Bought items cleared: 3",
  );
  assert.equal(
    eventLabel({ type: "food.store.archived", data: { name: "Lidl" } }),
    "Store archived: Lidl",
  );
  assert.equal(
    eventLabel({
      type: "food.aisle_rule.created",
      data: { keyword: "kerze", aisle: "household", match: "substring" },
    }),
    "Aisle rule created: kerze → Household",
  );
  assert.equal(
    eventLabel({ type: "food.pantry_item.used_up", data: { name: "Joghurt" } }),
    "Used up: Joghurt",
  );
  assert.equal(
    eventLabel({ type: "food.pantry_item.restocked", data: {} }),
    "Back in stock",
  );
  assert.equal(
    eventLabel({
      type: "food.pantry_item.running_low",
      data: { name: "Milch" },
    }),
    "Running low: Milch",
  );
  assert.equal(
    eventLabel({ type: "food.staple.created", data: { name: "Milch" } }),
    "Staple added: Milch",
  );
  assert.equal(
    eventLabel({ type: "food.staple.deleted", data: { name: "Milch" } }),
    "Staple removed: Milch",
  );
  // An item a staple put on the list says why.
  assert.equal(
    eventLabel({
      type: "food.shopping_item.added",
      data: { name: "Milch", reason: "used_up", stapleId: "s1" },
    }),
    "Added to the shopping list: Milch — used up (staple)",
  );
  assert.equal(
    eventLabel({
      type: "food.shopping_item.added",
      data: { name: "Käse", reason: "running_low" },
    }),
    "Added to the shopping list: Käse — running low",
  );
  assert.equal(
    eventLabel({
      type: "food.shopping_item.put_away",
      data: { name: "Milch", location: "fridge" },
    }),
    "Put away: Milch",
  );
  assert.equal(
    eventLabel({
      type: "food.notification.missed",
      data: { kind: "expiry", day: "2026-10-11" },
    }),
    "Expiry reminder missed",
  );
  assert.equal(subjectLabel("food_notification"), "Food reminder");
  assert.equal(
    eventLabel({
      type: "food.recipe.updated",
      data: { title: "Pasta Bolognese", changes: ["ingredients"] },
    }),
    "Recipe updated: Pasta Bolognese",
  );
  assert.equal(
    eventLabel({
      type: "food.meal_plan.set",
      data: { date: "2026-10-12", slot: "dinner", title: "Pasta" },
    }),
    "Meal planned: Pasta",
  );
  assert.equal(subjectLabel("food_recipe"), "Recipe");
  assert.equal(
    eventLabel({
      type: "food.recipe.imported",
      data: { title: "Spaghetti Bolognese", site: "chefkoch.de" },
    }),
    "Recipe imported: Spaghetti Bolognese",
  );
  assert.equal(sourceLabel("recipe-import"), "Recipe import");
  assert.equal(subjectLabel("food_meal_plan"), "Meal plan slot");
  assert.equal(sourceLabel("food"), "Food");
  assert.ok(sourceDescription("food"));
  assert.equal(subjectLabel("food_shopping_item"), "Shopping item");
  assert.equal(subjectLabel("food_shopping_list"), "Shopping list");
  assert.equal(subjectLabel("food_store"), "Store");
  assert.equal(subjectLabel("food_aisle_rule"), "Aisle rule");
  assert.equal(subjectLabel("food_pantry_item"), "Pantry item");
  assert.equal(subjectLabel("food_staple"), "Staple");
  assert.equal(subjectLabel("food_idea"), "Recipe idea");
  assert.equal(subjectLabel("food_preferences"), "Cooking preferences");
  assert.equal(
    eventLabel({
      type: "food.idea.created",
      data: { title: "Air fryer wings" },
    }),
    "Recipe idea: Air fryer wings",
  );
  assert.equal(
    eventLabel({
      type: "food.preferences.updated",
      data: { methods: ["air_fryer"] },
    }),
    "Cooking preferences updated",
  );
  for (const type of [
    "food.shopping_item.added",
    "food.shopping_item.merged",
    "food.shopping_item.updated",
    "food.shopping_item.bought",
    "food.shopping_item.reopened",
    "food.shopping_item.deleted",
    "food.shopping_item.put_away",
    "food.shopping_items.cleared",
    "food.store.created",
    "food.store.updated",
    "food.store.archived",
    "food.store.restored",
    "food.aisle_rule.created",
    "food.aisle_rule.deleted",
    "food.pantry_item.added",
    "food.pantry_item.updated",
    "food.pantry_item.used_up",
    "food.pantry_item.thrown_away",
    "food.pantry_item.restocked",
    "food.pantry_item.deleted",
    "food.notification.missed",
    "food.recipe.created",
    "food.recipe.updated",
    "food.recipe.deleted",
    "food.meal_plan.set",
    "food.meal_plan.cleared",
    "food.recipe.imported",
    "food.pantry_item.running_low",
    "food.pantry_item.running_low_cleared",
    "food.staple.created",
    "food.staple.updated",
    "food.staple.deleted",
    "food.preferences.updated",
    "food.idea.created",
    "food.idea.saved",
    "food.idea.dismissed",
  ]) {
    assert.ok(EVENT_TYPES.includes(type), type);
  }
});
