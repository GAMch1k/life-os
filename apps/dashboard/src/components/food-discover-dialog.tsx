"use client";

import { ExternalLink, Search } from "lucide-react";
import { type FormEvent, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { PantryItem } from "@/lib/food";
import {
  type DiscoverResult,
  type DiscoveredDraft,
  discover,
  discoverChoices,
} from "@/lib/food-suggest";

/**
 * Discover: recipes from TheMealDB for a main ingredient at home (those
 * that expire soonest first) or one typed. A result opens in the recipe
 * form to check and save, through the import like a link; nothing is saved
 * by itself. TheMealDB is in English.
 */
export function DiscoverDialog({
  open,
  pantry,
  onClose,
  onChoose,
}: {
  open: boolean;
  pantry: PantryItem[];
  onClose: () => void;
  /** A draft to check and save. */
  onChoose: (draft: DiscoveredDraft) => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Discover recipes</DialogTitle>
          <DialogDescription>
            Recipes from TheMealDB for something at home. They are in English;
            nothing is saved until you save one.
          </DialogDescription>
        </DialogHeader>
        {open ? <DiscoverPanel onChoose={onChoose} pantry={pantry} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function DiscoverPanel({
  pantry,
  onChoose,
}: {
  pantry: PantryItem[];
  onChoose: (draft: DiscoveredDraft) => void;
}) {
  const choices = discoverChoices(pantry);
  const [typed, setTyped] = useState("");
  const [pending, setPending] = useState(false);
  const [found, setFound] = useState<DiscoverResult>();
  const [error, setError] = useState<string>();

  const search = (ingredient: string) => {
    if (!ingredient.trim()) return;
    setPending(true);
    setError(undefined);
    void discover(ingredient.trim()).then((result) => {
      setPending(false);
      if (result.error !== undefined) {
        setFound(undefined);
        return setError(result.error);
      }
      setFound(result.data);
    });
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    search(typed);
  };

  return (
    <div className="grid gap-4">
      {choices.length ? (
        <div className="space-y-1.5">
          <p className="text-sm font-medium">At home</p>
          <div
            className="flex flex-wrap gap-1.5"
            data-testid="food-discover-choices"
          >
            {choices.map((choice) => (
              <Button
                disabled={pending}
                key={choice.name}
                onClick={() => {
                  setTyped(choice.name);
                  search(choice.name);
                }}
                size="sm"
                variant="outline"
              >
                {choice.name}
              </Button>
            ))}
          </div>
        </div>
      ) : null}
      <form className="flex items-end gap-2" onSubmit={submit}>
        <Field className="flex-1">
          <FieldLabel htmlFor="food-discover-ingredient">
            Main ingredient
          </FieldLabel>
          <Input
            className="h-10 text-base"
            id="food-discover-ingredient"
            maxLength={200}
            onChange={(event) => setTyped(event.target.value)}
            placeholder="Hähnchen, salmon, Kartoffeln"
            value={typed}
          />
        </Field>
        <Button disabled={pending || !typed.trim()} size="lg" type="submit">
          <Search aria-hidden="true" />
          Search
        </Button>
      </form>

      <p
        aria-live="polite"
        className="text-muted-foreground text-sm"
        role="status"
      >
        {pending
          ? "Asking TheMealDB…"
          : found
            ? found.items.length
              ? `TheMealDB has these for “${found.query}”.`
              : `TheMealDB has no recipes for “${found.query}”.`
            : ""}
      </p>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      {found?.items.length ? (
        <ul
          className="divide-y rounded-lg border"
          data-testid="food-discovered"
        >
          {found.items.map((draft) => (
            <li
              className="space-y-1.5 px-3 py-2.5"
              key={draft.sourceUrl ?? draft.title}
            >
              {draft.sourceUrl ? (
                <a
                  className="text-primary inline-flex items-center gap-1 font-medium underline-offset-4 hover:underline"
                  href={draft.sourceUrl}
                  rel="noreferrer"
                  target="_blank"
                >
                  {draft.title}
                  <ExternalLink aria-hidden="true" className="size-3.5" />
                </a>
              ) : (
                <p className="font-medium">{draft.title}</p>
              )}
              <p className="text-muted-foreground flex flex-wrap items-center gap-1.5 text-xs">
                {draft.ingredients.length} ingredients
                {draft.tags.map((tag) => (
                  <Badge key={tag} variant="secondary">
                    {tag}
                  </Badge>
                ))}
              </p>
              <Button
                aria-label={`Check and save: ${draft.title}`}
                onClick={() => onChoose(draft)}
                size="lg"
                variant="outline"
              >
                Check and save
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      {found ? (
        <p className="text-muted-foreground text-xs">
          Recipe data and imagery:{" "}
          <a
            className="underline-offset-4 hover:underline"
            href="https://www.themealdb.com/"
            rel="noreferrer"
            target="_blank"
          >
            TheMealDB
          </a>
        </p>
      ) : null}
    </div>
  );
}
