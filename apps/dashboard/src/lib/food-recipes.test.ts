import assert from "node:assert/strict";
import test from "node:test";

import {
  type IngredientResult,
  type MealPlanEntry,
  type Recipe,
  allTags,
  byTitle,
  dayHeading,
  entriesByDay,
  ingredientStatusLabel,
  missingCounts,
  missingNote,
  moveRow,
  newRecipeBody,
  normalizeText,
  parseWeek,
  recipeDraft,
  recipeMatches,
  recipeMeta,
  recipePatch,
  stepLines,
  todayMeals,
  weekDays,
  weekLabel,
  weekStart,
} from "./food-recipes";
import { dayKey } from "./time-zone";

const STAMP = "2026-10-01T00:00:00Z";
const actor = { kind: "user" as const };

function recipe(overrides: Partial<Recipe> = {}): Recipe {
  const title = overrides.title ?? "Pasta Bolognese";
  return {
    id: title.toLowerCase(),
    title,
    normalizedTitle: title.toLowerCase(),
    servings: null,
    ingredients: [],
    steps: null,
    tags: [],
    sourceUrl: null,
    note: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    createdBy: actor,
    updatedBy: actor,
    ...overrides,
  };
}

const ingredient = (name: string, amount: string | null = null) => ({
  name,
  normalizedName: name.toLowerCase(),
  amount,
  note: null,
});

function entry(
  date: string,
  slot: MealPlanEntry["slot"],
  title: string,
): MealPlanEntry {
  return {
    id: `${date}-${slot}`,
    date,
    slot,
    recipeId: null,
    title,
    servings: null,
    note: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    createdBy: actor,
    updatedBy: actor,
  };
}

test("weeks start on Monday, also across daylight saving changes", () => {
  // 25 October 2026 is a Sunday: the clocks go back in Berlin.
  assert.equal(weekStart("2026-10-25"), "2026-10-19");
  assert.equal(weekStart("2026-10-26"), "2026-10-26");
  assert.equal(weekStart("2026-10-21"), "2026-10-19");
  // 29 March 2026, a Sunday: the clocks go forward.
  assert.equal(weekStart("2026-03-29"), "2026-03-23");
  // Across a month and a year.
  assert.equal(weekStart("2026-01-01"), "2025-12-29");
  assert.deepEqual(weekDays("2026-10-19"), [
    "2026-10-19",
    "2026-10-20",
    "2026-10-21",
    "2026-10-22",
    "2026-10-23",
    "2026-10-24",
    "2026-10-25",
  ]);
  assert.deepEqual(weekDays("2026-03-23").slice(-2), [
    "2026-03-28",
    "2026-03-29",
  ]);
  // The owner's today decides the week: 23:30 UTC on Sunday 25 October is
  // already Monday in Berlin, still Sunday in New York.
  const late = "2026-10-25T23:30:00Z";
  assert.equal(weekStart(dayKey(late, "Europe/Berlin")), "2026-10-26");
  assert.equal(weekStart(dayKey(late, "America/New_York")), "2026-10-19");
});

test("a week in the URL is any day of it; other values are not weeks", () => {
  assert.equal(parseWeek("2026-10-21"), "2026-10-19");
  assert.equal(parseWeek("2026-10-19"), "2026-10-19");
  assert.equal(parseWeek("2026-02-30"), undefined);
  assert.equal(parseWeek("next"), undefined);
  assert.equal(parseWeek(undefined), undefined);
});

test("weeks and days read short", () => {
  assert.equal(dayHeading("2026-10-12"), "Mon 12 Oct");
  assert.equal(weekLabel("2026-10-12", "2026-10-11"), "12 – 18 Oct");
  assert.equal(weekLabel("2026-09-28", "2026-10-11"), "28 Sept – 4 Oct");
  assert.equal(
    weekLabel("2025-12-29", "2026-10-11"),
    "29 Dec 2025 – 4 Jan 2026",
  );
  assert.equal(weekLabel("2027-01-11", "2026-10-11"), "11 – 17 Jan 2027");
});

test("recipes are found by title words, ingredients, and tags", () => {
  const pasta = recipe({
    tags: ["italian", "quick"],
    ingredients: [ingredient("Hackfleisch"), ingredient("Spaghetti")],
  });
  assert.equal(recipeMatches(pasta, ""), true);
  assert.equal(recipeMatches(pasta, "bolo"), true);
  assert.equal(recipeMatches(pasta, "  PASTA  bolognese "), true);
  assert.equal(recipeMatches(pasta, "hack"), true);
  assert.equal(recipeMatches(pasta, "pasta chili"), false);
  assert.equal(recipeMatches(pasta, "", "italian"), true);
  assert.equal(recipeMatches(pasta, "", "vegan"), false);
  assert.equal(normalizeText(" Ёжик, "), "ежик");
  assert.deepEqual(
    allTags([pasta, recipe({ title: "Chili", tags: ["vegan", "quick"] })]),
    ["italian", "quick", "vegan"],
  );
  assert.deepEqual(
    byTitle([recipe({ title: "Soup" }), recipe({ title: "Chili" })]).map(
      (item) => item.title,
    ),
    ["Chili", "Soup"],
  );
  assert.equal(
    recipeMeta(recipe({ servings: 4, ingredients: [ingredient("A")] })),
    "4 servings · 1 ingredient",
  );
});

test("steps are the non-empty lines, without typed numbers", () => {
  assert.deepEqual(stepLines("1. Boil water\n\n2) Add pasta \nServe."), [
    "Boil water",
    "Add pasta",
    "Serve.",
  ]);
  assert.deepEqual(stepLines(null), []);
  // A number that is part of the step stays.
  assert.deepEqual(stepLines("200 g flour in"), ["200 g flour in"]);
});

test("the recipe form sends what was filled in, and patches what changed", () => {
  const draft = recipeDraft();
  assert.equal(draft.ingredients.length, 1);
  assert.deepEqual(newRecipeBody({ ...draft, title: " " }).errors, {
    title: "Give the recipe a title.",
  });
  assert.deepEqual(
    newRecipeBody({
      ...draft,
      title: "Toast",
      servings: "0",
      sourceUrl: "example.com",
    }).errors,
    {
      servings: "Servings are a whole number from 1 to 100.",
      sourceUrl: "Use a web address starting with https://.",
    },
  );
  assert.deepEqual(
    newRecipeBody({
      title: " Pasta ",
      servings: "4",
      ingredients: [
        { key: "a", name: "Spaghetti ", amount: " 500 g", note: null },
        { key: "b", name: " ", amount: "1", note: null },
      ],
      steps: "Boil.\n",
      tags: "italian, , quick",
      sourceUrl: "",
    }).body,
    {
      title: "Pasta",
      servings: 4,
      ingredients: [{ name: "Spaghetti", amount: "500 g" }],
      steps: "Boil.",
      tags: ["italian", "quick"],
    },
  );
  const stored = recipe({
    title: "Pasta",
    servings: 4,
    ingredients: [{ ...ingredient("Spaghetti", "500 g"), note: "dry" }],
    steps: "Boil.",
    tags: ["italian"],
  });
  const same = recipeDraft(stored);
  assert.equal(same.tags, "italian");
  assert.deepEqual(recipePatch(stored, same), { body: null, errors: {} });
  // Tags compare normalised.
  assert.equal(recipePatch(stored, { ...same, tags: "Italian" }).body, null);
  const moved = {
    ...same,
    servings: "",
    ingredients: [
      ...same.ingredients,
      { key: "c", name: "Salz", amount: "", note: null },
    ],
    sourceUrl: "https://example.com/pasta",
  };
  assert.deepEqual(recipePatch(stored, moved).body, {
    servings: null,
    // The ingredient note the form does not show is kept.
    ingredients: [
      { name: "Spaghetti", amount: "500 g", note: "dry" },
      { name: "Salz" },
    ],
    sourceUrl: "https://example.com/pasta",
  });
  assert.deepEqual(moveRow(["a", "b", "c"], 2, -1), ["a", "c", "b"]);
  assert.deepEqual(moveRow(["a", "b"], 0, -1), ["a", "b"]);
});

test("the plan groups by day and names today's meals in slot order", () => {
  const entries = [
    entry("2026-10-12", "dinner", "Pasta"),
    entry("2026-10-12", "breakfast", "Porridge"),
    entry("2026-10-13", "lunch", "Soup"),
  ];
  const byDay = entriesByDay(entries);
  assert.equal(byDay.get("2026-10-12")?.dinner?.title, "Pasta");
  assert.equal(byDay.get("2026-10-12")?.lunch, undefined);
  assert.equal(
    todayMeals(entries, "2026-10-12"),
    "Today: Porridge (breakfast) · Pasta (dinner)",
  );
  assert.equal(todayMeals(entries, "2026-10-14"), null);
});

test("the missing-ingredients preview says what will happen", () => {
  const result = (status: IngredientResult["status"]): IngredientResult => ({
    name: "x",
    normalizedName: "x",
    amount: null,
    status,
    recipes: [],
    shoppingItem: null,
    pantryItem: null,
  });
  assert.equal(ingredientStatusLabel("added", true), "Will add");
  assert.equal(ingredientStatusLabel("added", false), "Added");
  assert.equal(ingredientStatusLabel("in_pantry", true), "At home");
  assert.equal(ingredientStatusLabel("on_list", true), "On the list");
  const items = [
    result("added"),
    result("merged"),
    result("in_pantry"),
    result("on_list"),
  ];
  assert.deepEqual(missingCounts(items), { toAdd: 2, skipped: 2 });
  assert.equal(
    missingNote({ dryRun: false, items }),
    "Added 2 items to the shopping list.",
  );
  assert.equal(
    missingNote({ dryRun: false, items: [result("on_list")] }),
    "Nothing to add: everything is at home or on the list.",
  );
});
