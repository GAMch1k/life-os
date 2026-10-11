"use client";

import {
  ExternalLink,
  Link2,
  Pencil,
  Plus,
  ShoppingBasket,
} from "lucide-react";
import { useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { ImportDialog } from "@/components/food-import-dialog";
import {
  MissingIngredientsDialog,
  type MissingRequest,
} from "@/components/food-missing-dialog";
import {
  RecipeDialog,
  type RecipeFormState,
} from "@/components/food-recipe-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import type { ShoppingItem } from "@/lib/food";
import { formDraft, siteName } from "@/lib/food-import";
import {
  type Recipe,
  allTags,
  byTitle,
  recipeMatches,
  recipeMeta,
  stepLines,
} from "@/lib/food-recipes";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";
import { cn } from "@/lib/utils";

const EMPTY =
  "text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm";
// Not a tag: tags are kept without surrounding punctuation.
const ALL_TAGS = "*";

/**
 * The recipes: a search over titles and ingredients, tag chips, and the
 * list; a recipe opens with its ingredients and numbered steps, from where
 * it is edited or its missing ingredients go on the shopping list.
 */
export function FoodRecipes({
  recipes,
  shopping,
  error,
}: {
  recipes: SyncedList<Recipe>;
  shopping: SyncedList<ShoppingItem>;
  /** Why the recipes could not be read on the server, if they could not. */
  error?: string;
}) {
  const [query, setQuery] = useState("");
  const [tag, setTag] = useState(ALL_TAGS);
  const [viewing, setViewing] = useState<Recipe | null>(null);
  const [form, setForm] = useState<RecipeFormState>(null);
  const [importing, setImporting] = useState(false);
  const [missing, setMissing] = useState<MissingRequest | null>(null);
  const [note, setNote] = useState<string>();
  const tags = allTags(recipes.items);
  // A tag no recipe has any more shows everything again.
  const chosenTag = tags.includes(tag) ? tag : ALL_TAGS;
  const shown = byTitle(recipes.items).filter((recipe) =>
    recipeMatches(
      recipe,
      query,
      chosenTag === ALL_TAGS ? undefined : chosenTag,
    ),
  );

  return (
    <div className="space-y-4" data-testid="food-recipes">
      <div className="flex flex-wrap gap-2">
        <Input
          aria-label="Search recipes"
          className="h-11 min-w-0 flex-[1_1_14rem] text-base"
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search by title or ingredient"
          type="search"
          value={query}
        />
        <div className="flex flex-1 gap-2 sm:flex-none">
          <Button
            className="h-11 flex-1 sm:flex-none"
            onClick={() => {
              setNote(undefined);
              setImporting(true);
            }}
            size="lg"
            variant="outline"
          >
            <Link2 aria-hidden="true" />
            Import from link
          </Button>
          <Button
            className="h-11 flex-1 sm:flex-none"
            onClick={() => {
              setNote(undefined);
              setForm({});
            }}
            size="lg"
          >
            <Plus aria-hidden="true" />
            New recipe
          </Button>
        </div>
      </div>

      {tags.length ? (
        <ToggleGroup
          aria-label="Tag"
          className="flex-wrap"
          onValueChange={(chosen) => {
            // Pressing the current tag again would leave none pressed.
            if (chosen[0]) setTag(chosen[0]);
          }}
          size="lg"
          spacing={1}
          value={[chosenTag]}
          variant="outline"
        >
          <ToggleGroupItem value={ALL_TAGS}>All</ToggleGroupItem>
          {tags.map((entry) => (
            <ToggleGroupItem key={entry} value={entry}>
              {entry}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      ) : null}

      <p
        aria-live="polite"
        className={cn("text-muted-foreground text-sm", !note && "sr-only")}
        data-testid="food-recipes-note"
        role="status"
      >
        {note}
      </p>

      {error && !recipes.items.length ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : shown.length ? (
        <ul className="bg-card divide-y rounded-lg border">
          {shown.map((recipe) => (
            <li data-testid="food-recipe" key={recipe.id}>
              <button
                className="focus-visible:ring-ring flex min-h-12 w-full flex-col items-start gap-1 rounded-lg px-3 py-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset"
                onClick={() => {
                  setNote(undefined);
                  setViewing(recipe);
                }}
                type="button"
              >
                <span className="font-medium wrap-anywhere">
                  {recipe.title}
                </span>
                <span className="text-muted-foreground flex flex-wrap items-center gap-1.5 text-xs">
                  {recipeMeta(recipe)}
                  {recipe.tags.map((entry) => (
                    <Badge key={entry} variant="secondary">
                      {entry}
                    </Badge>
                  ))}
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className={EMPTY}>
          {recipes.items.length ? "No recipe matches." : "No recipes yet."}
        </p>
      )}

      <RecipeView
        onAddMissing={(recipe) => {
          setViewing(null);
          setMissing({ label: recipe.title, body: { recipeIds: [recipe.id] } });
        }}
        onClose={() => setViewing(null)}
        onEdit={(recipe) => {
          setViewing(null);
          setForm({ recipe });
        }}
        recipe={viewing}
      />
      <ImportDialog
        onClose={() => setImporting(false)}
        onContinue={(draft, extras) => {
          setImporting(false);
          setForm({ draft: formDraft(draft), importFrom: extras });
        }}
        open={importing}
      />
      <RecipeDialog
        onClose={() => setForm(null)}
        onDeleted={(recipe) => {
          setForm(null);
          setNote(`Deleted “${recipe.title}”.`);
        }}
        onExists={(recipeId, title) => {
          setForm(null);
          setNote(`“${title}” is already saved.`);
          const existing = recipes.items.find((item) => item.id === recipeId);
          if (existing) setViewing(existing);
          else recipes.reload();
        }}
        onSaved={(recipe) => {
          setForm(null);
          setViewing(recipe);
        }}
        recipes={recipes}
        state={form}
      />
      <MissingIngredientsDialog
        onClose={() => setMissing(null)}
        onDone={setNote}
        request={missing}
        shopping={shopping}
      />
    </div>
  );
}

/** A recipe: its ingredients with amounts and its steps, numbered. */
function RecipeView({
  recipe,
  onClose,
  onEdit,
  onAddMissing,
}: {
  recipe: Recipe | null;
  onClose: () => void;
  onEdit: (recipe: Recipe) => void;
  onAddMissing: (recipe: Recipe) => void;
}) {
  const current = useClosingState(recipe);
  const steps = stepLines(current?.steps ?? null);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={recipe !== null}
    >
      <DialogContent className="sm:max-w-lg">
        {current ? (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{current.title}</DialogTitle>
              <DialogDescription>{recipeMeta(current)}</DialogDescription>
            </DialogHeader>
            {current.tags.length || current.sourceUrl ? (
              <div className="flex flex-wrap items-center gap-1.5 text-sm">
                {current.tags.map((entry) => (
                  <Badge key={entry} variant="secondary">
                    {entry}
                  </Badge>
                ))}
                {current.sourceUrl ? (
                  <span
                    className="text-muted-foreground inline-flex items-center gap-1"
                    data-testid="food-recipe-source"
                  >
                    From
                    <a
                      className="text-primary inline-flex items-center gap-1 underline-offset-4 hover:underline"
                      href={current.sourceUrl}
                      rel="noreferrer"
                      target="_blank"
                    >
                      {siteName(current.sourceUrl) ?? "the source"}
                      <ExternalLink aria-hidden="true" className="size-3.5" />
                    </a>
                  </span>
                ) : null}
              </div>
            ) : null}

            <section aria-label="Ingredients">
              <h3 className="mb-1.5 text-sm font-semibold">Ingredients</h3>
              {current.ingredients.length ? (
                <ul className="divide-y rounded-lg border text-sm">
                  {current.ingredients.map((item, index) => (
                    <li
                      className="flex items-baseline justify-between gap-3 px-3 py-2"
                      key={`${index}-${item.normalizedName}`}
                    >
                      <span className="min-w-0 wrap-anywhere">
                        {item.name}
                        {item.note ? (
                          <span className="text-muted-foreground">
                            , {item.note}
                          </span>
                        ) : null}
                      </span>
                      {item.amount ? (
                        <span className="text-muted-foreground shrink-0">
                          {item.amount}
                        </span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-muted-foreground text-sm">
                  No ingredients listed.
                </p>
              )}
            </section>

            <section aria-label="Steps">
              <h3 className="mb-1.5 text-sm font-semibold">Steps</h3>
              {steps.length ? (
                <ol className="list-decimal space-y-1.5 pl-5 text-sm">
                  {steps.map((step, index) => (
                    <li className="wrap-anywhere" key={index}>
                      {step}
                    </li>
                  ))}
                </ol>
              ) : (
                <p className="text-muted-foreground text-sm">
                  No steps written.
                </p>
              )}
            </section>
            {current.note ? (
              <p className="text-muted-foreground text-sm whitespace-pre-line">
                {current.note}
              </p>
            ) : null}

            <DialogFooter>
              <Button
                onClick={() => onEdit(current)}
                size="lg"
                type="button"
                variant="outline"
              >
                <Pencil aria-hidden="true" />
                Edit
              </Button>
              <Button
                disabled={!current.ingredients.length}
                onClick={() => onAddMissing(current)}
                size="lg"
                type="button"
              >
                <ShoppingBasket aria-hidden="true" />
                Add missing ingredients
              </Button>
            </DialogFooter>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
