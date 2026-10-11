import assert from "node:assert/strict";
import test from "node:test";

import {
  type ImportDraft,
  formDraft,
  importOverrides,
  isSocialLink,
  previewSummary,
  siteName,
  withCaption,
} from "./food-import";

const draft = (overrides: Partial<ImportDraft> = {}): ImportDraft => ({
  title: "",
  ingredients: [],
  steps: [],
  tags: [],
  ...overrides,
});

test("a link's site reads as people say it", () => {
  assert.equal(
    siteName("https://www.chefkoch.de/rezepte/1/x.html"),
    "chefkoch.de",
  );
  assert.equal(siteName("https://www.instagram.com/p/ABC/"), "Instagram");
  assert.equal(siteName("https://m.youtube.com/watch?v=1"), "YouTube");
  assert.equal(siteName("https://youtu.be/1"), "YouTube");
  assert.equal(siteName("not a link"), null);
  assert.equal(siteName(null), null);
  assert.equal(isSocialLink("https://www.tiktok.com/@a/video/1"), true);
  assert.equal(isSocialLink("https://www.bbc.co.uk/food"), false);
});

test("a preview says what it found", () => {
  assert.equal(
    previewSummary({
      method: "structured",
      draft: draft({
        title: "Pasta",
        siteName: "Chefkoch.de",
        ingredients: [{ name: "Pasta" }],
        steps: ["Kochen.", "Essen."],
      }),
    }),
    "Found the recipe on Chefkoch.de: 1 ingredient, 2 steps.",
  );
  assert.equal(
    previewSummary({
      method: "preview",
      reason: "Instagram posts can't be read directly.",
      draft: draft(),
    }),
    "Instagram posts can't be read directly.",
  );
});

test("a caption fills in what the page lacked", () => {
  const page = draft({
    title: "Anna on Instagram: “Creamy pasta…”",
    tags: ["to-review"],
    sourceUrl: "https://www.instagram.com/p/A/",
    imageUrl: "https://cdn.example/1.jpg",
  });
  const caption = draft({
    title: "Creamy pasta",
    servings: 2,
    ingredients: [{ name: "Pasta", amount: "250 g" }],
    steps: ["Kochen."],
    tags: ["pasta", "to-review"],
  });
  assert.deepEqual(withCaption(page, caption, true), {
    ...page,
    title: "Creamy pasta",
    servings: 2,
    ingredients: [{ name: "Pasta", amount: "250 g" }],
    steps: ["Kochen."],
    tags: ["to-review", "pasta"],
  });
  // A real page title stays; a caption without lists changes no lists.
  const blog = draft({ title: "Greek salad", ingredients: [{ name: "Feta" }] });
  assert.deepEqual(withCaption(blog, draft({ title: "Yum" }), false), {
    ...blog,
    servings: undefined,
  });
});

test("the edited draft goes back as the integration's overrides", () => {
  const form = formDraft(
    draft({
      title: "Pasta",
      servings: 2,
      ingredients: [{ name: "Pasta", amount: "250 g" }, { name: "Salz" }],
      steps: ["Kochen.", "Essen."],
      tags: ["to-review", "pasta"],
      sourceUrl: "https://www.instagram.com/p/A/",
    }),
  );
  assert.equal(form.steps, "Kochen.\nEssen.");
  assert.equal(form.tags, "to-review, pasta");
  assert.equal(form.ingredients[1].amount, "");
  const extras = {
    url: "https://www.instagram.com/p/A/",
    imageUrl: "https://cdn.example/1.jpg",
    description: "Creamy.",
    siteName: "Instagram",
  };
  assert.deepEqual(importOverrides(form, extras), {
    overrides: {
      title: "Pasta",
      servings: 2,
      ingredients: [{ name: "Pasta", amount: "250 g" }, { name: "Salz" }],
      steps: ["Kochen.", "Essen."],
      tags: ["to-review", "pasta"],
      sourceUrl: "https://www.instagram.com/p/A/",
      imageUrl: "https://cdn.example/1.jpg",
      description: "Creamy.",
      siteName: "Instagram",
    },
    errors: {},
  });
  assert.deepEqual(importOverrides({ ...form, title: " " }, extras), {
    overrides: null,
    errors: { title: "Give the recipe a title." },
  });
  // An empty form still has one ingredient row to fill in.
  assert.equal(formDraft(draft()).ingredients.length, 1);
});
