"use client";

import { ArrowDown, ArrowUp, Plus, Trash2, X } from "lucide-react";
import { type FormEvent, useId, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { foodClient } from "@/lib/api/food-client";
import { recipeImportClient } from "@/lib/api/recipe-import-client";
import { load, upsert, writeHeader } from "@/lib/food";
import { type ImportExtras, importOverrides } from "@/lib/food-import";
import {
  COOKING_METHODS,
  type CookingMethod,
  type IngredientRow,
  type Recipe,
  type RecipeDraft,
  type RecipeErrors,
  ingredientRow,
  moveRow,
  newRecipeBody,
  recipeDraft,
  recipePatch,
} from "@/lib/food-recipes";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";

/**
 * The form's subject: a recipe to edit, or a new one, possibly from an
 * import (its draft, saved through the recipe-import integration).
 */
export type RecipeFormState = {
  recipe?: Recipe;
  draft?: RecipeDraft;
  importFrom?: ImportExtras;
} | null;

/**
 * Creates or edits a recipe: title, servings, ingredient rows (added,
 * removed, and moved), steps one per line, tags, and a source link; or
 * deletes it. Meals planned with a deleted recipe stay as plain text. An
 * imported draft is saved through the recipe-import integration, which
 * keeps the page's description and image link in the note.
 */
export function RecipeDialog({
  state,
  recipes,
  onClose,
  onSaved,
  onDeleted,
  onExists,
}: {
  state: RecipeFormState;
  recipes: SyncedList<Recipe>;
  onClose: () => void;
  onSaved: (recipe: Recipe) => void;
  onDeleted: (recipe: Recipe) => void;
  /** An imported recipe's title is already saved (its id, when known). */
  onExists?: (recipeId: string | null, title: string) => void;
}) {
  const current = useClosingState(state);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={state !== null}
    >
      <DialogContent className="sm:max-w-lg">
        {current ? (
          <RecipeForm
            key={
              current.recipe
                ? `${current.recipe.id}-${current.recipe.updatedAt}`
                : current.importFrom
                  ? `import-${current.importFrom.url}`
                  : "new"
            }
            importFrom={current.importFrom}
            initialDraft={current.draft}
            onCancel={onClose}
            onDeleted={onDeleted}
            onExists={onExists}
            onSaved={onSaved}
            recipe={current.recipe}
            recipes={recipes}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function RecipeForm({
  recipe,
  initialDraft,
  importFrom,
  recipes,
  onCancel,
  onSaved,
  onDeleted,
  onExists,
}: {
  recipe?: Recipe;
  initialDraft?: RecipeDraft;
  importFrom?: ImportExtras;
  recipes: SyncedList<Recipe>;
  onCancel: () => void;
  onSaved: (recipe: Recipe) => void;
  onDeleted: (recipe: Recipe) => void;
  onExists?: (recipeId: string | null, title: string) => void;
}) {
  const id = useId();
  const [draft, setDraft] = useState<RecipeDraft>(
    () => initialDraft ?? recipeDraft(recipe),
  );
  const [errors, setErrors] = useState<RecipeErrors & { form?: string }>({});
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pending, setPending] = useState(false);
  const update = (patch: Partial<RecipeDraft>) =>
    setDraft((value) => ({ ...value, ...patch }));
  const updateRow = (key: string, patch: Partial<IngredientRow>) =>
    update({
      ingredients: draft.ingredients.map((row) =>
        row.key === key ? { ...row, ...patch } : row,
      ),
    });

  /** Sends one save; on success applies the recipe and closes. */
  const send = (request: () => Promise<{ data?: Recipe; error?: unknown }>) => {
    setPending(true);
    setErrors({});
    void recipes.write(async () => {
      const result = await load(request, "Could not save this recipe.");
      setPending(false);
      if (result.error !== undefined) return setErrors({ form: result.error });
      recipes.change((items) => upsert(items, result.data));
      onSaved(result.data);
    });
  };

  /** Saves an imported draft through the integration. */
  const saveImport = (from: ImportExtras) => {
    const { overrides, errors: found } = importOverrides(draft, from);
    if (!overrides) return setErrors(found);
    setPending(true);
    setErrors({});
    void recipes.write(async () => {
      let answer: Awaited<ReturnType<typeof recipeImportClient.POST>>;
      try {
        answer = await recipeImportClient.POST("/v1/imports", {
          params: { header: writeHeader() },
          body: { url: from.url, overrides },
        });
      } catch {
        setPending(false);
        return setErrors({ form: "Could not save this recipe." });
      }
      setPending(false);
      const { data, error } = answer;
      if (
        error &&
        "recipeId" in error &&
        error.error.code === "recipe_exists"
      ) {
        return onExists?.(error.recipeId ?? null, overrides.title);
      }
      if (!data) {
        return setErrors({
          form: error?.error.message ?? "Could not save this recipe.",
        });
      }
      const saved = data.recipe as Recipe;
      recipes.change((items) => upsert(items, saved));
      onSaved(saved);
    });
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (importFrom) return saveImport(importFrom);
    if (!recipe) {
      const { body, errors: found } = newRecipeBody(draft);
      if (!body) return setErrors(found);
      return send(() =>
        foodClient.POST("/v1/recipes", {
          params: { header: writeHeader() },
          body,
        }),
      );
    }
    const { body, errors: found } = recipePatch(recipe, draft);
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onCancel();
    send(() =>
      foodClient.PATCH("/v1/recipes/{recipeId}", {
        params: { path: { recipeId: recipe.id }, header: writeHeader() },
        body,
      }),
    );
  };

  const remove = () => {
    if (!recipe) return;
    setPending(true);
    setErrors({});
    void recipes.write(async () => {
      const result = await load(
        () =>
          foodClient
            .DELETE("/v1/recipes/{recipeId}", {
              params: { path: { recipeId: recipe.id }, header: writeHeader() },
            })
            .then(({ response, error }) => ({
              data: response.ok ? true : undefined,
              error,
            })),
        "Could not delete this recipe.",
      );
      setPending(false);
      if (result.error !== undefined) {
        setConfirmDelete(false);
        return setErrors({ form: result.error });
      }
      recipes.change((items) => items.filter((item) => item.id !== recipe.id));
      onDeleted(recipe);
    });
  };

  const invalid = (field: keyof RecipeErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {recipe
            ? "Edit recipe"
            : importFrom
              ? "Save imported recipe"
              : "New recipe"}
        </DialogTitle>
        <DialogDescription>
          {importFrom
            ? `From ${importFrom.siteName ?? "the link"}. Check it before saving; steps are one per line.`
            : "Steps are plain text, one step per line."}
        </DialogDescription>
      </DialogHeader>

      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_7rem]">
        <Field data-invalid={Boolean(errors.title)}>
          <FieldLabel htmlFor={`${id}-title`}>Title</FieldLabel>
          <Input
            className="h-10 text-base"
            id={`${id}-title`}
            maxLength={200}
            onChange={(event) => update({ title: event.target.value })}
            value={draft.title}
            {...invalid("title")}
          />
          <FieldError id={`${id}-title-error`}>{errors.title}</FieldError>
        </Field>
        <Field data-invalid={Boolean(errors.servings)}>
          <FieldLabel htmlFor={`${id}-servings`}>Servings</FieldLabel>
          <Input
            className="h-10 text-base"
            id={`${id}-servings`}
            inputMode="numeric"
            onChange={(event) => update({ servings: event.target.value })}
            value={draft.servings}
            {...invalid("servings")}
          />
          <FieldError id={`${id}-servings-error`}>{errors.servings}</FieldError>
        </Field>
      </div>

      <FieldSet className="gap-2">
        <FieldLegend variant="label">Ingredients</FieldLegend>
        <ol className="grid gap-2" data-testid="food-ingredient-rows">
          {draft.ingredients.map((row, index) => (
            <li className="flex items-center gap-1.5" key={row.key}>
              <Input
                aria-label={`Ingredient ${index + 1}`}
                className="h-10 min-w-0 flex-1 text-base"
                maxLength={200}
                onChange={(event) =>
                  updateRow(row.key, { name: event.target.value })
                }
                placeholder="Spaghetti"
                value={row.name}
              />
              <Input
                aria-label={`Amount of ingredient ${index + 1}`}
                className="h-10 w-20 shrink-0 text-base sm:w-28"
                maxLength={50}
                onChange={(event) =>
                  updateRow(row.key, { amount: event.target.value })
                }
                placeholder="500 g"
                value={row.amount}
              />
              <Button
                aria-label={`Move ingredient ${index + 1} up`}
                disabled={index === 0}
                onClick={() =>
                  update({ ingredients: moveRow(draft.ingredients, index, -1) })
                }
                size="icon"
                type="button"
                variant="ghost"
              >
                <ArrowUp aria-hidden="true" />
              </Button>
              <Button
                aria-label={`Move ingredient ${index + 1} down`}
                disabled={index === draft.ingredients.length - 1}
                onClick={() =>
                  update({ ingredients: moveRow(draft.ingredients, index, 1) })
                }
                size="icon"
                type="button"
                variant="ghost"
              >
                <ArrowDown aria-hidden="true" />
              </Button>
              <Button
                aria-label={`Remove ingredient ${index + 1}`}
                onClick={() =>
                  update({
                    ingredients: draft.ingredients.filter(
                      (entry) => entry.key !== row.key,
                    ),
                  })
                }
                size="icon"
                type="button"
                variant="ghost"
              >
                <X aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ol>
        <Button
          className="w-fit"
          onClick={() =>
            update({ ingredients: [...draft.ingredients, ingredientRow()] })
          }
          size="lg"
          type="button"
          variant="outline"
        >
          <Plus aria-hidden="true" />
          Add ingredient
        </Button>
      </FieldSet>

      <Field>
        <FieldLabel htmlFor={`${id}-steps`}>Steps</FieldLabel>
        <Textarea
          className="min-h-32 text-base"
          id={`${id}-steps`}
          maxLength={20000}
          onChange={(event) => update({ steps: event.target.value })}
          placeholder={"Boil the pasta.\nFry the onions."}
          value={draft.steps}
        />
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-tags`}>Tags</FieldLabel>
          <Input
            className="h-10 text-base"
            id={`${id}-tags`}
            onChange={(event) => update({ tags: event.target.value })}
            placeholder="quick, vegetarian"
            value={draft.tags}
          />
          <FieldDescription>Separated by commas.</FieldDescription>
        </Field>
        <Field data-invalid={Boolean(errors.sourceUrl)}>
          <FieldLabel htmlFor={`${id}-source`}>Source link</FieldLabel>
          <Input
            className="h-10 text-base"
            id={`${id}-source`}
            inputMode="url"
            maxLength={2000}
            onChange={(event) => update({ sourceUrl: event.target.value })}
            placeholder="https://"
            value={draft.sourceUrl}
            {...invalid("sourceUrl")}
          />
          <FieldError id={`${id}-sourceUrl-error`}>
            {errors.sourceUrl}
          </FieldError>
        </Field>
      </div>

      {importFrom ? null : (
        <MethodsField
          detected={recipe?.methodsInferred ? recipe.methods : null}
          methods={draft.methods}
          onChange={(methods) => update({ methods })}
        />
      )}

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter className={recipe ? "sm:justify-between" : undefined}>
        {confirmDelete ? (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <p className="text-sm font-medium">
              Delete it? Meals planned with it stay as plain text.
            </p>
            <div className="flex gap-2">
              <Button
                disabled={pending}
                onClick={remove}
                size="lg"
                type="button"
                variant="destructive"
              >
                {pending ? "Deleting…" : "Delete"}
              </Button>
              <Button
                disabled={pending}
                onClick={() => setConfirmDelete(false)}
                size="lg"
                type="button"
                variant="outline"
              >
                Keep
              </Button>
            </div>
          </div>
        ) : (
          <>
            {recipe ? (
              <Button
                disabled={pending}
                onClick={() => setConfirmDelete(true)}
                size="lg"
                type="button"
                variant="ghost"
              >
                <Trash2 aria-hidden="true" />
                Delete
              </Button>
            ) : null}
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              <Button
                disabled={pending}
                onClick={onCancel}
                size="lg"
                type="button"
                variant="outline"
              >
                Cancel
              </Button>
              <Button disabled={pending} size="lg" type="submit">
                {pending
                  ? "Saving…"
                  : recipe
                    ? "Save"
                    : importFrom
                      ? "Save recipe"
                      : "Add recipe"}
              </Button>
            </div>
          </>
        )}
      </DialogFooter>
    </form>
  );
}

/**
 * How the recipe is cooked: Food detects it from the title, tags, and
 * steps ("air fryer", "Backofen", "сковорода") until the owner picks the
 * methods here; "Detect again" hands it back to Food.
 */
function MethodsField({
  methods,
  detected,
  onChange,
}: {
  /** As set; null while Food detects them. */
  methods: CookingMethod[] | null;
  /** What Food detected, for a saved recipe whose methods are detected. */
  detected: CookingMethod[] | null;
  onChange: (methods: CookingMethod[] | null) => void;
}) {
  const shown = methods ?? detected ?? [];
  return (
    <FieldSet>
      <FieldLegend variant="label">Cooked in</FieldLegend>
      <ToggleGroup
        aria-label="Cooked in"
        className="flex-wrap"
        multiple
        onValueChange={(value) => onChange(value as CookingMethod[])}
        size="lg"
        spacing={1}
        value={shown}
        variant="outline"
      >
        {COOKING_METHODS.map((method) => (
          <ToggleGroupItem key={method.value} value={method.value}>
            {method.label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
      <FieldDescription>
        {methods === null ? (
          "Detected from the title, tags, and steps."
        ) : (
          <>
            Set by you.{" "}
            <Button
              className="h-auto p-0"
              onClick={() => onChange(null)}
              size="sm"
              type="button"
              variant="link"
            >
              Detect again
            </Button>
          </>
        )}
      </FieldDescription>
    </FieldSet>
  );
}
