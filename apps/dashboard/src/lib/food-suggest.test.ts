import assert from "node:assert/strict";
import test from "node:test";

import type { PantryItem } from "./food";
import type { Recipe } from "./food-recipes";
import {
  type Suggestion,
  addDislike,
  cookTonightLine,
  discoverChoices,
  expiresWhen,
  ideaSource,
  missingItems,
  missingLine,
  preferencesBody,
  preferencesDraft,
  reasonPoints,
} from "./food-suggest";

const STAMP = "2026-10-01T00:00:00Z";
const actor = { kind: "user" as const };

const recipe: Recipe = {
  id: "r1",
  title: "Hähnchen-Reis-Pfanne",
  normalizedTitle: "hähnchen-reis-pfanne",
  servings: null,
  ingredients: [],
  steps: null,
  tags: [],
  methods: ["air_fryer"],
  methodsInferred: true,
  sourceUrl: null,
  note: null,
  createdAt: STAMP,
  updatedAt: STAMP,
  createdBy: actor,
  updatedBy: actor,
};

function suggestion(overrides: Partial<Suggestion> = {}): Suggestion {
  return {
    recipe,
    score: 0,
    reasons: [],
    have: [],
    missing: [],
    basics: [],
    expiring: [],
    ...overrides,
  };
}

function pantry(
  name: string,
  expiresOn: string | null,
  state: PantryItem["state"] = "in_stock",
): PantryItem {
  return {
    id: name,
    name,
    normalizedName: name.toLowerCase(),
    amount: null,
    location: "fridge",
    expiresOn,
    openedOn: null,
    note: null,
    state,
    endedAt: null,
    runningLow: false,
    runningLowAt: null,
    source: "manual",
    sourceRef: null,
    fromShoppingItemId: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    createdBy: actor,
    updatedBy: actor,
  };
}

test("a suggestion says its points and what to buy", () => {
  assert.equal(reasonPoints({ points: 12 }), "+12");
  assert.equal(reasonPoints({ points: -10 }), "−10");
  assert.equal(missingLine(suggestion()), "You have everything for it.");
  const short = suggestion({
    missing: [
      { name: "Zwiebel", amount: "1" },
      { name: "Käse", amount: null },
    ],
  });
  assert.equal(missingLine(short), "To buy: Zwiebel, Käse");
  // The missing ingredients go on the list as the recipe's.
  assert.deepEqual(missingItems(short), [
    { name: "Zwiebel", amount: "1", source: "recipe", sourceRef: "r1" },
    { name: "Käse", source: "recipe", sourceRef: "r1" },
  ]);
});

test("Home suggests tonight's meal only when expiring food drives it", () => {
  assert.equal(cookTonightLine({ items: [] }), null);
  assert.equal(cookTonightLine({ items: [suggestion()] }), null);
  const use = {
    ingredient: "Hähnchen",
    pantryItemId: "p1",
    pantryItemName: "Hähnchenbrust",
    expiresOn: "2026-10-12",
    expiresInDays: 1,
  };
  assert.equal(
    cookTonightLine({ items: [suggestion({ expiring: [use] })] }),
    "Cook tonight: Hähnchen-Reis-Pfanne (uses Hähnchenbrust, expiring tomorrow)",
  );
  assert.equal(expiresWhen(0), "today");
  assert.equal(expiresWhen(3), "in 3 days");
});

test("Discover offers what is at home, what expires soonest first", () => {
  const choices = discoverChoices([
    pantry("Reis", null),
    pantry("Sahne", "2026-10-14"),
    pantry("Hähnchen", "2026-10-12"),
    pantry("hähnchen", "2026-10-20"),
    pantry("Alte Milch", "2026-10-01", "used_up"),
    pantry("Apfel", null),
  ]);
  assert.deepEqual(
    choices.map((choice) => choice.name),
    ["Hähnchen", "Sahne", "Apfel", "Reis"],
  );
  assert.equal(
    discoverChoices([pantry("A", null), pantry("B", null)], 1).length,
    1,
  );
});

test("an idea says where it came from", () => {
  assert.equal(ideaSource({ source: "ai", sourceUrl: null }), "AI idea");
  assert.equal(
    ideaSource({
      source: "web",
      sourceUrl: "https://www.example.com/shakshuka",
    }),
    "Found on example.com",
  );
  assert.equal(
    ideaSource({ source: "web", sourceUrl: "nonsense" }),
    "Found on the web",
  );
});

test("preferences keep the air fryer first and each dislike once", () => {
  const draft = preferencesDraft({
    methods: ["air_fryer"],
    dislikes: ["Koriander"],
    dietaryNotes: null,
    updatedAt: STAMP,
    updatedBy: actor,
  });
  assert.deepEqual(draft, {
    methods: ["air_fryer"],
    dislikes: ["Koriander"],
    dietaryNotes: "",
  });
  assert.deepEqual(addDislike(draft.dislikes, " koriander "), ["Koriander"]);
  assert.deepEqual(addDislike(draft.dislikes, "  "), ["Koriander"]);
  assert.deepEqual(addDislike(draft.dislikes, "Rosinen"), [
    "Koriander",
    "Rosinen",
  ]);
  assert.deepEqual(
    preferencesBody({
      methods: ["oven", "air_fryer"],
      dislikes: ["Rosinen"],
      dietaryNotes: "  ",
    }),
    { methods: ["air_fryer", "oven"], dislikes: ["Rosinen"] },
  );
  assert.deepEqual(
    preferencesBody({
      methods: [],
      dislikes: [],
      dietaryNotes: " Wenig Fleisch ",
    }),
    { methods: [], dislikes: [], dietaryNotes: "Wenig Fleisch" },
  );
});
