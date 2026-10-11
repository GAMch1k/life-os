import { foodClient } from "@/lib/api/food-client";
import type { components } from "@/lib/api/generated/food";
import {
  FOOD_MAX_PAGES,
  FOOD_PAGE_SIZE,
  type FoodList,
  type Loaded,
  load,
} from "@/lib/food";
import { addDays } from "@/lib/time-zone";

// Recipes and the weekly meal plan: search, drafts as request bodies, the
// week's days, and what the missing-ingredients preview says. Everything but
// the fetch helpers is pure and unit-tested.

type Schemas = components["schemas"];

export type Recipe = Schemas["Recipe"];
export type Ingredient = Schemas["Ingredient"];
export type MealSlot = Schemas["MealSlot"];
export type MealPlanEntry = Schemas["MealPlanEntry"];
export type IngredientResult = Schemas["IngredientResult"];
export type FromRecipesResult = Schemas["FromRecipesResult"];
export type FromRecipesRequest = Schemas["FromRecipesRequest"];
export type CreateRecipeBody = Schemas["CreateRecipeRequest"];
export type CookingMethod = Schemas["CookingMethod"];
export type UpdateRecipeBody = Schemas["UpdateRecipeRequest"];

/** A day's meals, in the order the plan lists them. */
export const MEAL_SLOTS: { value: MealSlot; label: string }[] = [
  { value: "breakfast", label: "Breakfast" },
  { value: "lunch", label: "Lunch" },
  { value: "dinner", label: "Dinner" },
];

export function slotLabel(slot: MealSlot) {
  return MEAL_SLOTS.find((entry) => entry.value === slot)?.label ?? slot;
}

// ---- Search -----------------------------------------------------------------

/**
 * Text as Food compares names, near enough for searching in the browser:
 * NFKC, lower case, "ё" as "е", runs of whitespace as one space, and
 * surrounding punctuation stripped.
 */
export function normalizeText(text: string) {
  return text
    .normalize("NFKC")
    .toLowerCase()
    .replace(/ё/g, "е")
    .replace(/\s+/g, " ")
    .replace(/^[\p{P}\p{S}\s]+|[\p{P}\p{S}\s]+$/gu, "");
}

/**
 * Whether a recipe matches a search: every word of the query is in its
 * title or in one of its ingredients (also inside a word), and it has the
 * tag, when one is chosen.
 */
export function recipeMatches(recipe: Recipe, query: string, tag?: string) {
  if (tag && !recipe.tags.includes(tag)) return false;
  const words = normalizeText(query).split(" ").filter(Boolean);
  return words.every(
    (word) =>
      recipe.normalizedTitle.includes(word) ||
      recipe.ingredients.some((item) => item.normalizedName.includes(word)),
  );
}

// A fixed locale, so the server and every browser sort alike.
const collator = new Intl.Collator("en");

/** Every tag of the recipes, once, alphabetically. */
export function allTags(recipes: Recipe[]) {
  return [...new Set(recipes.flatMap((recipe) => recipe.tags))].sort(
    collator.compare,
  );
}

/** Recipes by title, as Food lists them. */
export function byTitle(recipes: Recipe[]) {
  return [...recipes].sort((a, b) =>
    collator.compare(a.normalizedTitle, b.normalizedTitle),
  );
}

/**
 * The steps of a recipe, one per non-empty line, without numbers the owner
 * typed ("1. Boil water" is "Boil water"): the page numbers them.
 */
export function stepLines(steps: string | null) {
  return (steps ?? "")
    .split("\n")
    .map((line) => line.trim().replace(/^\d+\s*[.)]\s+/, ""))
    .filter(Boolean);
}

/** The cooking methods, in Food's order, with their labels. */
export const COOKING_METHODS: { value: CookingMethod; label: string }[] = [
  { value: "air_fryer", label: "Air fryer" },
  { value: "oven", label: "Oven" },
  { value: "stovetop", label: "Stovetop" },
  { value: "slow_cooker", label: "Slow cooker" },
  { value: "grill", label: "Grill" },
  { value: "microwave", label: "Microwave" },
  { value: "no_cook", label: "No cooking" },
];

export function methodLabel(method: string) {
  return (
    COOKING_METHODS.find((entry) => entry.value === method)?.label ?? method
  );
}

/** Methods in Food's order, each once. */
export function orderedMethods(methods: readonly string[]): CookingMethod[] {
  return COOKING_METHODS.map((entry) => entry.value).filter((value) =>
    methods.includes(value),
  );
}

/** "4 servings · 3 ingredients" */
export function recipeMeta(recipe: Recipe) {
  const parts = [];
  if (recipe.servings)
    parts.push(`${recipe.servings} serving${recipe.servings === 1 ? "" : "s"}`);
  const count = recipe.ingredients.length;
  parts.push(`${count} ingredient${count === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

// ---- Drafts -----------------------------------------------------------------

/** An ingredient row of the recipe form; `key` keeps React rows stable. */
export type IngredientRow = {
  key: string;
  name: string;
  amount: string;
  /** Kept as it was: the form does not edit ingredient notes. */
  note: string | null;
};

export type RecipeDraft = {
  title: string;
  servings: string;
  ingredients: IngredientRow[];
  steps: string;
  /** Comma-separated. */
  tags: string;
  sourceUrl: string;
  /** The cooking methods as set; null lets Food detect them. */
  methods: CookingMethod[] | null;
};

export type RecipeErrors = {
  title?: string;
  servings?: string;
  sourceUrl?: string;
};

let rowKeys = 0;

/** A new, empty ingredient row. */
export function ingredientRow(
  values: Partial<Omit<IngredientRow, "key">> = {},
): IngredientRow {
  rowKeys += 1;
  return {
    key: `row-${rowKeys}`,
    name: "",
    amount: "",
    note: null,
    ...values,
  };
}

export function recipeDraft(recipe?: Recipe): RecipeDraft {
  return {
    title: recipe?.title ?? "",
    servings: recipe?.servings ? String(recipe.servings) : "",
    ingredients: recipe?.ingredients.length
      ? recipe.ingredients.map((item) =>
          ingredientRow({
            name: item.name,
            amount: item.amount ?? "",
            note: item.note,
          }),
        )
      : [ingredientRow()],
    steps: recipe?.steps ?? "",
    tags: recipe?.tags.join(", ") ?? "",
    sourceUrl: recipe?.sourceUrl ?? "",
    methods:
      recipe && recipe.methodsInferred === false
        ? orderedMethods(recipe.methods)
        : null,
  };
}

/** The rows with `index` moved up (-1) or down (+1). */
export function moveRow<T>(rows: T[], index: number, delta: -1 | 1) {
  const target = index + delta;
  if (target < 0 || target >= rows.length) return rows;
  const next = [...rows];
  [next[index], next[target]] = [next[target], next[index]];
  return next;
}

type CheckedDraft = {
  title: string;
  servings: number | null;
  ingredients: { name: string; amount?: string; note?: string }[];
  steps: string | null;
  tags: string[];
  sourceUrl: string | null;
};

function checkDraft(draft: RecipeDraft): {
  checked?: CheckedDraft;
  errors: RecipeErrors;
} {
  const errors: RecipeErrors = {};
  const title = draft.title.trim();
  if (!title) errors.title = "Give the recipe a title.";
  const servingsText = draft.servings.trim();
  const servings = servingsText ? Number(servingsText) : null;
  if (
    servings !== null &&
    (!Number.isInteger(servings) || servings < 1 || servings > 100)
  )
    errors.servings = "Servings are a whole number from 1 to 100.";
  const sourceUrl = draft.sourceUrl.trim() || null;
  if (sourceUrl && !/^https?:\/\/\S+$/i.test(sourceUrl))
    errors.sourceUrl = "Use a web address starting with https://.";
  if (Object.keys(errors).length) return { errors };
  const ingredients = draft.ingredients
    .filter((row) => row.name.trim())
    .map((row) => ({
      name: row.name.trim(),
      ...(row.amount.trim() ? { amount: row.amount.trim() } : {}),
      ...(row.note ? { note: row.note } : {}),
    }));
  const tags = draft.tags
    .split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
  return {
    checked: {
      title,
      servings,
      ingredients,
      steps: draft.steps.trim() || null,
      tags,
      sourceUrl,
    },
    errors,
  };
}

/** A new recipe from the form; what is left empty is not sent. */
export function newRecipeBody(draft: RecipeDraft): {
  body: CreateRecipeBody | null;
  errors: RecipeErrors;
} {
  const { checked, errors } = checkDraft(draft);
  if (!checked) return { body: null, errors };
  const body: CreateRecipeBody = { title: checked.title };
  if (checked.servings !== null) body.servings = checked.servings;
  if (checked.ingredients.length) body.ingredients = checked.ingredients;
  if (checked.steps) body.steps = checked.steps;
  if (checked.tags.length) body.tags = checked.tags;
  if (checked.sourceUrl) body.sourceUrl = checked.sourceUrl;
  if (draft.methods !== null) body.methods = orderedMethods(draft.methods);
  return { body, errors };
}

function sameIngredients(
  current: Ingredient[],
  next: CheckedDraft["ingredients"],
) {
  return (
    current.length === next.length &&
    current.every(
      (item, index) =>
        item.name === next[index].name &&
        (item.amount ?? undefined) === next[index].amount &&
        (item.note ?? undefined) === next[index].note,
    )
  );
}

/** The PATCH body for what the form changed, or null when nothing did. */
export function recipePatch(
  recipe: Recipe,
  draft: RecipeDraft,
): { body: UpdateRecipeBody | null; errors: RecipeErrors } {
  const { checked, errors } = checkDraft(draft);
  if (!checked) return { body: null, errors };
  const body: UpdateRecipeBody = {};
  if (checked.title !== recipe.title) body.title = checked.title;
  if (checked.servings !== recipe.servings) body.servings = checked.servings;
  if (!sameIngredients(recipe.ingredients, checked.ingredients))
    body.ingredients = checked.ingredients;
  if (checked.steps !== (recipe.steps?.trim() || null))
    body.steps = checked.steps;
  // Tags are kept normalised: compare them so.
  const tags = [...new Set(checked.tags.map(normalizeText))];
  if (tags.join("\n") !== recipe.tags.join("\n")) body.tags = checked.tags;
  if (checked.sourceUrl !== recipe.sourceUrl)
    body.sourceUrl = checked.sourceUrl;
  // Null detects the methods again; a list sets them.
  const setBefore = recipe.methodsInferred === false;
  if (draft.methods === null) {
    if (setBefore) body.methods = null;
  } else {
    const methods = orderedMethods(draft.methods);
    if (!setBefore || methods.join() !== orderedMethods(recipe.methods).join())
      body.methods = methods;
  }
  return { body: Object.keys(body).length ? body : null, errors };
}

// ---- Weeks ------------------------------------------------------------------

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/**
 * The Monday of the week a day (YYYY-MM-DD) is in. Days are plain dates,
 * so a daylight saving change inside the week changes nothing; "today" is
 * the owner's (see dayKey).
 */
export function weekStart(day: string) {
  const weekday = new Date(`${day}T00:00:00Z`).getUTCDay();
  return addDays(day, -((weekday + 6) % 7));
}

/** The seven days of the week that starts on `monday`. */
export function weekDays(monday: string) {
  return Array.from({ length: 7 }, (_, index) => addDays(monday, index));
}

/** A week named in the URL (any day of it), as its Monday. */
export function parseWeek(value: unknown) {
  if (typeof value !== "string" || !DAY.test(value)) return undefined;
  const date = new Date(`${value}T00:00:00Z`);
  if (Number.isNaN(date.getTime()) || date.toISOString().slice(0, 10) !== value)
    return undefined;
  return weekStart(value);
}

const dayFormats = new Map<string, Intl.DateTimeFormat>();

function format(day: string, options: Intl.DateTimeFormatOptions) {
  const key = JSON.stringify(options);
  let formatter = dayFormats.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("en-GB", {
      ...options,
      timeZone: "UTC",
    });
    dayFormats.set(key, formatter);
  }
  return formatter.format(new Date(`${day}T00:00:00Z`));
}

/** "Mon 12 Oct" */
export function dayHeading(day: string) {
  return format(day, { weekday: "short", day: "numeric", month: "short" });
}

/**
 * The week's dates: "12 – 18 Oct", "28 Sep – 4 Oct", or across a year "29
 * Dec 2025 – 4 Jan 2026"; with the year when it is not today's.
 */
export function weekLabel(monday: string, today: string) {
  const sunday = addDays(monday, 6);
  const year = sunday.slice(0, 4);
  const crossesYear = monday.slice(0, 4) !== year;
  const withYear = crossesYear || year !== today.slice(0, 4);
  const end = format(sunday, {
    day: "numeric",
    month: "short",
    ...(withYear ? { year: "numeric" } : {}),
  });
  const start =
    monday.slice(5, 7) === sunday.slice(5, 7) && !crossesYear
      ? format(monday, { day: "numeric" })
      : format(monday, {
          day: "numeric",
          month: "short",
          ...(crossesYear ? { year: "numeric" } : {}),
        });
  return `${start} – ${end}`;
}

/** The plan's entries by day, then slot. */
export function entriesByDay(entries: MealPlanEntry[]) {
  const out = new Map<string, Partial<Record<MealSlot, MealPlanEntry>>>();
  for (const entry of entries) {
    out.set(entry.date, { ...out.get(entry.date), [entry.slot]: entry });
  }
  return out;
}

/** "Today: Porridge (breakfast) · Pasta Bolognese (dinner)", or null. */
export function todayMeals(entries: MealPlanEntry[], today: string) {
  const meals = MEAL_SLOTS.flatMap(({ value }) =>
    entries
      .filter((entry) => entry.date === today && entry.slot === value)
      .map((entry) => `${entry.title} (${value})`),
  );
  return meals.length ? `Today: ${meals.join(" · ")}` : null;
}

// ---- Missing ingredients ----------------------------------------------------

/** What happened, or in a preview would happen, to one ingredient. */
export function ingredientStatusLabel(
  status: IngredientResult["status"],
  dryRun: boolean,
) {
  switch (status) {
    case "added":
      return dryRun ? "Will add" : "Added";
    case "merged":
      return "Added";
    case "in_pantry":
      return "At home";
    default:
      return "On the list";
  }
}

/** The ingredients to add, and those skipped. */
export function missingCounts(items: IngredientResult[]) {
  const toAdd = items.filter(
    (item) => item.status === "added" || item.status === "merged",
  ).length;
  return { toAdd, skipped: items.length - toAdd };
}

/** The line after adding: "Added 3 items to the shopping list." */
export function missingNote(result: FromRecipesResult) {
  const { toAdd } = missingCounts(result.items);
  if (!toAdd) return "Nothing to add: everything is at home or on the list.";
  return `Added ${toAdd} item${toAdd === 1 ? "" : "s"} to the shopping list.`;
}

// ---- Loading ----------------------------------------------------------------

/** Every recipe, by title, read page by page. */
export function fetchRecipes(): Promise<Loaded<FoodList<Recipe>>> {
  return load<FoodList<Recipe>>(async () => {
    const items: Recipe[] = [];
    let cursor: string | undefined;
    for (let page = 0; page < FOOD_MAX_PAGES; page++) {
      const { data, error } = await foodClient.GET("/v1/recipes", {
        params: { query: { limit: FOOD_PAGE_SIZE, cursor } },
      });
      if (!data) return { error: error ?? {} };
      items.push(...data.items);
      cursor = data.nextCursor ?? undefined;
      if (!cursor) return { data: { items, capped: false } };
    }
    return { data: { items, capped: true } };
  }, "Recipes could not be loaded.");
}

/** The meal plan of the week that starts on `monday`. */
export function fetchWeek(monday: string) {
  return load(
    () =>
      foodClient
        .GET("/v1/meal-plan", {
          params: { query: { from: monday, to: addDays(monday, 6) } },
        })
        .then(({ data, error }) => ({ data: data?.items, error })),
    "The meal plan could not be loaded.",
  );
}
