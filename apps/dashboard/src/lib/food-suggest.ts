import { OWNER_HEADERS } from "@/lib/api/actor";
import { foodClient } from "@/lib/api/food-client";
import type { components } from "@/lib/api/generated/food";
import type { components as importComponents } from "@/lib/api/generated/recipe-import";
import { recipeImportClient } from "@/lib/api/recipe-import-client";
import { type PantryItem, load } from "@/lib/food";
import {
  type CookingMethod,
  type MealSlot,
  orderedMethods,
} from "@/lib/food-recipes";

// "What can I cook?": Food's ranked suggestions and recipe ideas, the
// cooking preferences, and recipes discovered in TheMealDB through the
// recipe-import integration. Everything but the fetch helpers is pure and
// unit-tested.

type Schemas = components["schemas"];

export type Suggestion = Schemas["Suggestion"];
export type SuggestionList = Schemas["SuggestionList"];
export type SuggestionReason = Schemas["SuggestionReason"];
export type Idea = Schemas["Idea"];
export type Preferences = Schemas["Preferences"];
export type PreferencesBody = Schemas["PreferencesRequest"];
export type DiscoverResult = importComponents["schemas"]["DiscoverResult"];
export type DiscoveredDraft = importComponents["schemas"]["RecipeDraft"];

/** How many suggestions the panel shows. */
export const SUGGESTIONS_SHOWN = 5;

/** "+12" or "−10": a reason's points, for the eye. */
export function reasonPoints(reason: Pick<SuggestionReason, "points">) {
  return reason.points < 0 ? `−${-reason.points}` : `+${reason.points}`;
}

/**
 * What to buy for a suggestion, in one line: "To buy: Zwiebel, Käse", or
 * that everything is at home.
 */
export function missingLine(suggestion: Pick<Suggestion, "missing">) {
  if (!suggestion.missing.length) return "You have everything for it.";
  return `To buy: ${suggestion.missing.map((item) => item.name).join(", ")}`;
}

/** The shopping items for a suggestion's missing ingredients. */
export function missingItems(suggestion: Suggestion) {
  return suggestion.missing.map((item) => ({
    name: item.name,
    ...(item.amount ? { amount: item.amount } : {}),
    source: "recipe" as const,
    sourceRef: suggestion.recipe.id,
  }));
}

/** When something expires, from days: "today", "tomorrow", "in 3 days". */
export function expiresWhen(days: number) {
  if (days <= 0) return "today";
  if (days === 1) return "tomorrow";
  return `in ${days} days`;
}

/**
 * Home's line for tonight: the top dinner suggestion, only when food that
 * is about to expire drives it ("Cook tonight: Hähnchen-Reis-Pfanne (uses
 * Hähnchenbrust, expiring tomorrow)"); null otherwise.
 */
export function cookTonightLine(list: Pick<SuggestionList, "items">) {
  const top = list.items[0];
  const first = top?.expiring[0];
  if (!top || !first || first.expiresInDays === null) return null;
  return `Cook tonight: ${top.recipe.title} (uses ${first.pantryItemName}, expiring ${expiresWhen(first.expiresInDays)})`;
}

/**
 * The main ingredients to discover recipes for: the in-stock pantry items,
 * those that expire soonest first (items without a date last, by name),
 * each name once, at most `max`.
 */
export function discoverChoices(items: PantryItem[], max = 12) {
  const stocked = items
    .filter((item) => item.state === "in_stock")
    .sort((a, b) => {
      if (a.expiresOn !== b.expiresOn) {
        if (a.expiresOn === null) return 1;
        if (b.expiresOn === null) return -1;
        return a.expiresOn < b.expiresOn ? -1 : 1;
      }
      return a.name.localeCompare(b.name);
    });
  const seen = new Set<string>();
  const out: { name: string; expiresOn: string | null }[] = [];
  for (const item of stocked) {
    if (seen.has(item.normalizedName)) continue;
    seen.add(item.normalizedName);
    out.push({ name: item.name, expiresOn: item.expiresOn });
    if (out.length === max) break;
  }
  return out;
}

/** Where an idea came from: "AI idea", or the site it was found on. */
export function ideaSource(idea: Pick<Idea, "source" | "sourceUrl">) {
  if (idea.source === "web" && idea.sourceUrl) {
    try {
      return `Found on ${new URL(idea.sourceUrl).hostname.replace(/^www\./, "")}`;
    } catch {
      return "Found on the web";
    }
  }
  return idea.source === "web" ? "Found on the web" : "AI idea";
}

// ---- Preferences ------------------------------------------------------------

export type PreferencesDraft = {
  methods: CookingMethod[];
  dislikes: string[];
  dietaryNotes: string;
};

export function preferencesDraft(preferences: Preferences): PreferencesDraft {
  return {
    methods: [...preferences.methods],
    dislikes: [...preferences.dislikes],
    dietaryNotes: preferences.dietaryNotes ?? "",
  };
}

/**
 * The methods picked, the air fryer first among them (the owner's
 * preferred way), then in the usual order.
 */
export function preferredOrder(methods: readonly string[]): CookingMethod[] {
  return orderedMethods(methods);
}

/** Adds a disliked ingredient unless it is empty or listed already. */
export function addDislike(dislikes: string[], name: string) {
  const typed = name.trim();
  if (!typed) return dislikes;
  const key = typed.toLocaleLowerCase();
  if (dislikes.some((entry) => entry.toLocaleLowerCase() === key))
    return dislikes;
  return [...dislikes, typed];
}

export function preferencesBody(draft: PreferencesDraft): PreferencesBody {
  const body: PreferencesBody = {
    methods: preferredOrder(draft.methods),
    dislikes: draft.dislikes,
  };
  if (draft.dietaryNotes.trim()) body.dietaryNotes = draft.dietaryNotes.trim();
  return body;
}

// ---- Loading ----------------------------------------------------------------

export function fetchSuggestions(
  mealSlot?: MealSlot,
  limit = SUGGESTIONS_SHOWN,
) {
  return load(
    () =>
      foodClient.GET("/v1/suggestions", {
        params: { query: { limit, ...(mealSlot ? { mealSlot } : {}) } },
      }),
    "Suggestions could not be loaded.",
  );
}

export function fetchIdeas() {
  return load(
    () =>
      foodClient.GET("/v1/ideas", {
        params: { query: { state: "new", limit: 50 } },
      }),
    "Recipe ideas could not be loaded.",
  );
}

export function fetchPreferences() {
  return load(
    () => foodClient.GET("/v1/preferences"),
    "Preferences could not be loaded.",
  );
}

export function discover(ingredient: string) {
  return load(
    () =>
      recipeImportClient.GET("/v1/discover", {
        params: { query: { ingredient, limit: 6 } },
      }),
    "TheMealDB could not be asked.",
  );
}

/** What "What can I cook?" shows first: suggestions for any meal, and the new ideas. */
export type Cooking = {
  suggestions: Awaited<ReturnType<typeof fetchSuggestions>>;
  ideas: Awaited<ReturnType<typeof fetchIdeas>>;
};

export async function fetchCooking(): Promise<Cooking> {
  const [suggestions, ideas] = await Promise.all([
    fetchSuggestions(),
    fetchIdeas(),
  ]);
  return { suggestions, ideas };
}

// ---- Getting ideas ------------------------------------------------------------

/**
 * The processor's recipe ideas job, through the Dashboard
 * (src/app/api/processor): it has no gateway route.
 */
export const IDEAS_JOB_PATH = "/api/processor/ideas";

/** How often, and how many times, "Get ideas" looks for the new ideas. */
export const IDEAS_POLL_MS = 5000;
export const IDEAS_POLL_LIMIT = 60;

/** What the processor says about its ideas job (GET /ideas). */
export type IdeasJob = {
  running: boolean;
  queued: boolean;
  lastRun: {
    startedAt: string;
    finishedAt: string;
    created: number;
    outcome: string;
  } | null;
};

/** Asking for ideas: queued, or why not. */
export type IdeasAsk = { queued: true } | { queued: false; message: string };

/** Asks the processor for recipe ideas now. */
export async function requestIdeas(): Promise<IdeasAsk> {
  try {
    const response = await fetch(`${IDEAS_JOB_PATH}/requests`, {
      method: "POST",
      headers: OWNER_HEADERS,
      cache: "no-store",
    });
    if (response.ok) return { queued: true };
    return {
      queued: false,
      message: ideasAskProblem(await response.json().catch(() => null)),
    };
  } catch {
    return { queued: false, message: ideasAskProblem(null) };
  }
}

/** Why ideas could not be asked for: the processor's words, or a plain one. */
export function ideasAskProblem(body: unknown) {
  const message = (body as { error?: { message?: unknown } } | null)?.error
    ?.message;
  return typeof message === "string" && message
    ? message
    : "Ideas could not be asked for right now. Try again later.";
}

/** The ideas job's state, or null when the processor cannot be reached. */
export async function fetchIdeasJob(): Promise<IdeasJob | null> {
  try {
    const response = await fetch(IDEAS_JOB_PATH, { cache: "no-store" });
    return response.ok ? ((await response.json()) as IdeasJob) : null;
  } catch {
    return null;
  }
}

/**
 * Whether the run asked for is over: nothing waits or runs, and the last run
 * is a newer one than `before` (the last run when it was asked for).
 */
export function ideasRunOver(job: IdeasJob, before: IdeasJob | null) {
  return (
    !job.running &&
    !job.queued &&
    job.lastRun !== null &&
    job.lastRun.startedAt !== before?.lastRun?.startedAt
  );
}

/** What a finished run says: how many new ideas, or why there are none. */
export function ideasRunNote(job: IdeasJob) {
  const run = job.lastRun;
  if (!run || run.outcome === "failed")
    return "Ideas could not be thought of right now. Try again later.";
  if (run.created === 0)
    return "No new ideas this time: the ones found are already saved or suggested.";
  return run.created === 1 ? "1 new idea." : `${run.created} new ideas.`;
}

/** Whether `after` has an idea that `before` did not. */
export function hasNewIdea(
  before: ReadonlySet<string>,
  after: Pick<Idea, "id">[],
) {
  return after.some((idea) => !before.has(idea.id));
}
