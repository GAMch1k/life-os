import type { components } from "@/lib/api/generated/recipe-import";
import {
  type RecipeDraft,
  type RecipeErrors,
  ingredientRow,
  newRecipeBody,
} from "@/lib/food-recipes";

// Importing recipes from links through the recipe-import integration: what
// a preview says, a pasted caption merged into it, and the edited draft as
// the integration takes it. Everything here is pure and unit-tested.

type Schemas = components["schemas"];

export type ImportDraft = Schemas["RecipeDraft"];
export type ImportPreview = Schemas["ImportPreview"];
export type ImportMethod = Schemas["ImportMethod"];

/** What an import keeps besides the form's fields: they go in the note. */
export type ImportExtras = {
  url: string;
  imageUrl?: string | null;
  description?: string | null;
  siteName?: string | null;
};

/** The tag a preview's stub carries until the owner reviewed it. */
export const REVIEW_TAG = "to-review";

const SOCIAL: Record<string, string> = {
  "instagram.com": "Instagram",
  "tiktok.com": "TikTok",
  "youtube.com": "YouTube",
  "youtu.be": "YouTube",
  "facebook.com": "Facebook",
  "fb.watch": "Facebook",
  "threads.net": "Threads",
  "x.com": "X",
  "twitter.com": "X",
  "pinterest.com": "Pinterest",
};

/**
 * A link's site for people: "Instagram" for a social site, else its host
 * without "www." ("chefkoch.de"); null for something that is no link.
 */
export function siteName(link: string | null | undefined) {
  if (!link) return null;
  let host: string;
  try {
    host = new URL(link).hostname.toLowerCase().replace(/^www\./, "");
  } catch {
    return null;
  }
  const social = Object.entries(SOCIAL).find(
    ([domain]) => host === domain || host.endsWith(`.${domain}`),
  );
  return social ? social[1] : host || null;
}

/** Whether a link is a social post whose recipe sits in its caption. */
export function isSocialLink(link: string) {
  const name = siteName(link);
  return name !== null && Object.values(SOCIAL).includes(name);
}

/** The line that says what a preview found. */
export function previewSummary(preview: ImportPreview) {
  const draft = preview.draft;
  const site = draft.siteName ?? siteName(draft.sourceUrl) ?? "the page";
  if (preview.method === "structured") {
    const parts = [
      `${draft.ingredients.length} ingredient${draft.ingredients.length === 1 ? "" : "s"}`,
      `${draft.steps.length} step${draft.steps.length === 1 ? "" : "s"}`,
    ];
    return `Found the recipe on ${site}: ${parts.join(", ")}.`;
  }
  return preview.reason ?? `Only a preview could be read from ${site}.`;
}

/** The form's draft from an import draft. */
export function formDraft(draft: ImportDraft): RecipeDraft {
  return {
    title: draft.title,
    servings: draft.servings ? String(draft.servings) : "",
    ingredients: draft.ingredients.length
      ? draft.ingredients.map((item) =>
          ingredientRow({ name: item.name, amount: item.amount ?? "" }),
        )
      : [ingredientRow()],
    steps: draft.steps.join("\n"),
    tags: draft.tags.join(", "),
    sourceUrl: draft.sourceUrl ?? "",
    // Food detects how it is cooked once it is saved.
    methods: null,
  };
}

/**
 * A page's draft with a pasted caption read into it: the caption's
 * ingredients, steps, and servings where it found them, its title when the
 * page had none or was a social post (whose page title is the account's),
 * and the tags of both.
 */
export function withCaption(
  page: ImportDraft,
  caption: ImportDraft,
  social: boolean,
): ImportDraft {
  return {
    ...page,
    title:
      caption.title && (social || !page.title) ? caption.title : page.title,
    servings: caption.servings ?? page.servings,
    ingredients: caption.ingredients.length
      ? caption.ingredients
      : page.ingredients,
    steps: caption.steps.length ? caption.steps : page.steps,
    tags: [...new Set([...page.tags, ...caption.tags])],
  };
}

/**
 * The edited draft as the integration saves it (`overrides`), checked like
 * a new recipe; the description and image link of the page come along for
 * the note.
 */
export function importOverrides(
  draft: RecipeDraft,
  extras: ImportExtras,
): { overrides: ImportDraft | null; errors: RecipeErrors } {
  const { body, errors } = newRecipeBody(draft);
  if (!body) return { overrides: null, errors };
  return {
    overrides: {
      title: body.title,
      servings: body.servings ?? null,
      ingredients: (body.ingredients ?? []).map((item) => ({
        name: item.name,
        ...(item.amount ? { amount: item.amount } : {}),
      })),
      steps: (body.steps ?? "").split("\n").filter((line) => line.trim()),
      tags: body.tags ?? [],
      sourceUrl: extras.url,
      imageUrl: extras.imageUrl ?? null,
      description: extras.description ?? null,
      siteName: extras.siteName ?? null,
    },
    errors,
  };
}
