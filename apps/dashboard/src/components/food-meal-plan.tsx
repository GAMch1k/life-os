"use client";

import { ChevronLeft, ChevronRight, ShoppingBasket } from "lucide-react";
import { type FormEvent, useRef, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import {
  MissingIngredientsDialog,
  type MissingRequest,
} from "@/components/food-missing-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { foodClient } from "@/lib/api/food-client";
import { type ShoppingItem, load, writeHeader } from "@/lib/food";
import {
  MEAL_SLOTS,
  type MealPlanEntry,
  type MealSlot,
  type Recipe,
  byTitle,
  dayHeading,
  entriesByDay,
  fetchWeek,
  recipeMatches,
  slotLabel,
  weekDays,
  weekLabel,
  weekStart,
} from "@/lib/food-recipes";
import { addDays } from "@/lib/time-zone";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";
import { cn } from "@/lib/utils";

/** How many recipes the slot dialog lists for a search. */
const RECIPE_CHOICES = 8;

type Slot = { day: string; slot: MealSlot; entry?: MealPlanEntry };

/**
 * The week's meal plan, made for the phone: one card per day (Monday
 * first, in the owner's timezone) with breakfast, lunch, and dinner. A tap
 * on a meal picks a recipe, takes a typed meal, or clears it. The week's
 * missing ingredients go on the shopping list after a preview.
 */
export function FoodMealPlan({
  initialWeek,
  initialEntries,
  initialError,
  recipes,
  shopping,
  today,
  onWeekChange,
}: {
  /** The week shown first, as its Monday (YYYY-MM-DD). */
  initialWeek: string;
  initialEntries: MealPlanEntry[];
  initialError?: string;
  recipes: SyncedList<Recipe>;
  shopping: SyncedList<ShoppingItem>;
  /** The owner's today (YYYY-MM-DD). */
  today: string;
  /** The week shown changed: keep it in the URL. */
  onWeekChange: (monday: string) => void;
}) {
  const thisWeek = weekStart(today);
  const [week, setWeek] = useState(initialWeek);
  const [entries, setEntries] = useState(initialEntries);
  const [error, setError] = useState(initialError);
  const [loading, setLoading] = useState(false);
  const [editing, setEditing] = useState<Slot | null>(null);
  const [missing, setMissing] = useState<MissingRequest | null>(null);
  const [note, setNote] = useState<string>();
  // The latest week asked for: an older answer is dropped.
  const ticket = useRef(0);
  const byDay = entriesByDay(entries);
  const planned = entries.some((entry) => entry.recipeId);

  /** Reads a week; an answer for an older request is dropped. */
  const read = (monday: string) => {
    const mine = ++ticket.current;
    void fetchWeek(monday).then((result) => {
      if (mine !== ticket.current) return;
      setLoading(false);
      if (result.error !== undefined) return setError(result.error);
      setError(undefined);
      setEntries(result.data);
    });
  };

  const showWeek = (monday: string) => {
    setWeek(monday);
    setNote(undefined);
    setLoading(true);
    onWeekChange(monday);
    read(monday);
  };

  /** A slot changed: show the answer, then read the week again. */
  const changed = (day: string, slot: MealSlot, entry?: MealPlanEntry) => {
    setEntries((current) => [
      ...current.filter((item) => item.date !== day || item.slot !== slot),
      ...(entry ? [entry] : []),
    ]);
    read(week);
  };

  return (
    <div className="space-y-4" data-testid="food-meal-plan" data-week={week}>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          aria-label="Previous week"
          onClick={() => showWeek(addDays(week, -7))}
          size="icon-lg"
          variant="outline"
        >
          <ChevronLeft aria-hidden="true" />
        </Button>
        <Button
          disabled={week === thisWeek}
          onClick={() => showWeek(thisWeek)}
          size="lg"
          variant="outline"
        >
          This week
        </Button>
        <Button
          aria-label="Next week"
          onClick={() => showWeek(addDays(week, 7))}
          size="icon-lg"
          variant="outline"
        >
          <ChevronRight aria-hidden="true" />
        </Button>
        <h2
          aria-live="polite"
          className="ml-1 text-sm font-semibold"
          data-testid="food-week-label"
        >
          {weekLabel(week, today)}
        </h2>
      </div>

      <p
        aria-live="polite"
        className={cn("text-muted-foreground text-sm", !note && "sr-only")}
        data-testid="food-plan-note"
        role="status"
      >
        {note}
      </p>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      <div
        aria-busy={loading}
        className={cn("grid gap-3 sm:grid-cols-2", loading && "opacity-70")}
      >
        {weekDays(week).map((day) => (
          <section
            aria-label={dayHeading(day)}
            aria-current={day === today ? "date" : undefined}
            className={cn(
              "bg-card rounded-lg border",
              day === today && "border-primary/50",
            )}
            data-day={day}
            data-testid="food-plan-day"
            key={day}
          >
            <div className="flex items-center gap-2 px-3 pt-2.5 pb-1">
              <h3 className="text-sm font-semibold">{dayHeading(day)}</h3>
              {day === today ? <Badge variant="secondary">Today</Badge> : null}
            </div>
            <ul className="divide-y">
              {MEAL_SLOTS.map(({ value, label }) => {
                const entry = byDay.get(day)?.[value];
                return (
                  <li key={value}>
                    <button
                      aria-label={`${label}, ${dayHeading(day)}: ${entry?.title ?? "nothing planned"}`}
                      className="focus-visible:ring-ring flex min-h-12 w-full items-center gap-3 px-3 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset"
                      data-meal={value}
                      onClick={() => {
                        setNote(undefined);
                        setEditing({ day, slot: value, entry });
                      }}
                      type="button"
                    >
                      <span className="text-muted-foreground w-20 shrink-0 text-xs">
                        {label}
                      </span>
                      <span
                        className={cn(
                          "min-w-0 flex-1 wrap-anywhere",
                          entry ? "font-medium" : "text-muted-foreground",
                        )}
                      >
                        {entry?.title ?? "Add"}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        ))}
      </div>

      <Button
        disabled={!planned}
        onClick={() =>
          setMissing({
            label: `the week of ${weekLabel(week, today)}`,
            body: { from: week, to: addDays(week, 6) },
          })
        }
        size="lg"
        variant="outline"
      >
        <ShoppingBasket aria-hidden="true" />
        Add missing ingredients for this week
      </Button>

      <SlotDialog
        onClose={() => setEditing(null)}
        onSaved={(slot, entry) => {
          setEditing(null);
          changed(slot.day, slot.slot, entry);
        }}
        recipes={recipes.items}
        slot={editing}
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

/**
 * One meal: pick a recipe (searchable), type a meal, or clear it. Each is
 * saved at once.
 */
function SlotDialog({
  slot,
  recipes,
  onClose,
  onSaved,
}: {
  slot: Slot | null;
  recipes: Recipe[];
  onClose: () => void;
  /** The slot now holds entry, or nothing. */
  onSaved: (slot: Slot, entry?: MealPlanEntry) => void;
}) {
  const current = useClosingState(slot);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={slot !== null}
    >
      <DialogContent>
        {current ? (
          <SlotForm
            key={`${current.day}-${current.slot}`}
            onSaved={onSaved}
            recipes={recipes}
            slot={current}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function SlotForm({
  slot,
  recipes,
  onSaved,
}: {
  slot: Slot;
  recipes: Recipe[];
  onSaved: (slot: Slot, entry?: MealPlanEntry) => void;
}) {
  const [query, setQuery] = useState("");
  const [text, setText] = useState(
    slot.entry && !slot.entry.recipeId ? slot.entry.title : "",
  );
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const matches = byTitle(recipes).filter((recipe) =>
    recipeMatches(recipe, query),
  );
  const path = { date: slot.day, slot: slot.slot };

  const set = (body: { recipeId: string } | { text: string }) => {
    setPending(true);
    setError(undefined);
    void load(
      () =>
        foodClient.PUT("/v1/meal-plan/{date}/{slot}", {
          params: { path, header: writeHeader() },
          body,
        }),
      "Could not plan this meal.",
    ).then((result) => {
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      onSaved(slot, result.data);
    });
  };

  const clear = () => {
    setPending(true);
    setError(undefined);
    void load(
      () =>
        foodClient
          .DELETE("/v1/meal-plan/{date}/{slot}", {
            params: { path, header: writeHeader() },
          })
          // Already empty is cleared too.
          .then(({ response, error }) => ({
            data: response.ok || response.status === 404 ? true : undefined,
            error: response.status === 404 ? undefined : error,
          })),
      "Could not clear this meal.",
    ).then((result) => {
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      onSaved(slot);
    });
  };

  const submitText = (event: FormEvent) => {
    event.preventDefault();
    if (text.trim()) set({ text: text.trim() });
  };

  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle>
          {slotLabel(slot.slot)}, {dayHeading(slot.day)}
        </DialogTitle>
        <DialogDescription>
          {slot.entry
            ? `Planned: ${slot.entry.title}.`
            : "Pick a recipe or type a meal."}
        </DialogDescription>
      </DialogHeader>

      <Field className="gap-2">
        <FieldLabel htmlFor="food-slot-search">Recipe</FieldLabel>
        <Input
          className="h-10 text-base"
          id="food-slot-search"
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search recipes"
          type="search"
          value={query}
        />
        {recipes.length ? (
          matches.length ? (
            <ul
              className="divide-y rounded-lg border"
              data-testid="food-slot-recipes"
            >
              {matches.slice(0, RECIPE_CHOICES).map((recipe) => (
                <li key={recipe.id}>
                  <button
                    aria-pressed={slot.entry?.recipeId === recipe.id}
                    className="focus-visible:ring-ring aria-pressed:bg-muted flex min-h-11 w-full items-center px-3 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset disabled:opacity-50"
                    disabled={pending}
                    onClick={() => set({ recipeId: recipe.id })}
                    type="button"
                  >
                    <span className="wrap-anywhere">{recipe.title}</span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-muted-foreground text-sm">No recipe matches.</p>
          )
        ) : (
          <p className="text-muted-foreground text-sm">
            No recipes yet: add them in Recipes, or type the meal below.
          </p>
        )}
      </Field>

      <form className="grid gap-2" onSubmit={submitText}>
        <Field className="gap-2">
          <FieldLabel htmlFor="food-slot-text">Or type a meal</FieldLabel>
          <div className="flex gap-2">
            <Input
              className="h-10 flex-1 text-base"
              id="food-slot-text"
              maxLength={200}
              onChange={(event) => setText(event.target.value)}
              placeholder="Pizza with friends"
              value={text}
            />
            <Button disabled={pending || !text.trim()} size="lg" type="submit">
              Save
            </Button>
          </div>
        </Field>
      </form>

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      {slot.entry ? (
        <Button
          className="w-fit"
          disabled={pending}
          onClick={clear}
          size="lg"
          variant="ghost"
        >
          Clear {slotLabel(slot.slot).toLowerCase()}
        </Button>
      ) : null}
    </div>
  );
}
