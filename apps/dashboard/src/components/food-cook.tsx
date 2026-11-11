"use client";

import {
  CalendarPlus,
  ExternalLink,
  Search,
  ShoppingBasket,
  SlidersHorizontal,
  Sparkles,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { DatePicker } from "@/components/date-picker";
import { DiscoverDialog } from "@/components/food-discover-dialog";
import { PreferencesDialog } from "@/components/food-preferences-dialog";
import { useFeature } from "@/components/modules-provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldLabel } from "@/components/ui/field";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { foodClient } from "@/lib/api/food-client";
import {
  type PantryItem,
  type ShoppingItem,
  load,
  upsert,
  writeHeader,
} from "@/lib/food";
import {
  MEAL_SLOTS,
  type MealSlot,
  type Recipe,
  dayHeading,
  methodLabel,
  slotLabel,
} from "@/lib/food-recipes";
import {
  type Cooking,
  type DiscoveredDraft,
  type Idea,
  type Suggestion,
  type SuggestionList,
  IDEAS_POLL_LIMIT,
  IDEAS_POLL_MS,
  fetchIdeas,
  fetchIdeasJob,
  fetchSuggestions,
  hasNewIdea,
  ideaSource,
  ideasRunNote,
  ideasRunOver,
  requestIdeas,
  missingItems,
  missingLine,
  reasonPoints,
} from "@/lib/food-suggest";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";
import { cn } from "@/lib/utils";

const ANY_MEAL = "";

/**
 * "What can I cook?": the saved recipes Food ranks for today, each with why
 * (what is at home, what expires, the air fryer, habits) and what is
 * missing, to put on the list or plan; the recipe ideas the AI left, to
 * save or dismiss, and "Get ideas" to ask for some now; the cooking
 * preferences; and Discover, recipes from TheMealDB for an ingredient at
 * home.
 */
export function FoodCook({
  initial,
  recipes,
  shopping,
  pantry,
  today,
  onOpen,
  onDiscovered,
}: {
  initial: Cooking;
  recipes: SyncedList<Recipe>;
  shopping: SyncedList<ShoppingItem>;
  pantry: SyncedList<PantryItem>;
  today: string;
  /** Opens a recipe. */
  onOpen: (recipe: Recipe) => void;
  /** A discovered draft to check and save. */
  onDiscovered: (draft: DiscoveredDraft) => void;
}) {
  const [slot, setSlot] = useState<MealSlot | typeof ANY_MEAL>(ANY_MEAL);
  const [list, setList] = useState<SuggestionList | undefined>(
    initial.suggestions.data,
  );
  const [error, setError] = useState(initial.suggestions.error);
  const [ideas, setIdeas] = useState<Idea[]>(initial.ideas.data?.items ?? []);
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const [note, setNote] = useState<{ kind: "info" | "error"; text: string }>();
  const [planning, setPlanning] = useState<Recipe | null>(null);
  const [preferencesOpen, setPreferencesOpen] = useState(false);
  const [discovering, setDiscovering] = useState(false);
  // The latest read asked for: an older answer is dropped.
  const ticket = useRef(0);
  // While the processor thinks of ideas the owner asked for.
  const [thinking, setThinking] = useState(false);
  // Asking for ideas, while AI processing is on.
  const ideasOn = useFeature("food.ideas");
  const mounted = useRef(true);
  useEffect(
    () => () => {
      mounted.current = false;
    },
    [],
  );

  const working = (id: string, on: boolean) =>
    setBusy((current) => {
      const next = new Set(current);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  /** Reads the suggestions (and the ideas) again. */
  const read = (meal = slot) => {
    const mine = ++ticket.current;
    void Promise.all([
      fetchSuggestions(meal === ANY_MEAL ? undefined : meal),
      fetchIdeas(),
    ]).then(([suggestions, fresh]) => {
      if (mine !== ticket.current) return;
      if (suggestions.error !== undefined) setError(suggestions.error);
      else {
        setError(undefined);
        setList(suggestions.data);
      }
      if (fresh.data) setIdeas(fresh.data.items);
    });
  };

  /**
   * Asks the processor for ideas now, then reads the ideas every few
   * seconds, showing new ones as they come, until the run is over (or a
   * few minutes pass).
   */
  const getIdeas = async () => {
    setNote(undefined);
    const before = await fetchIdeasJob();
    const asked = await requestIdeas();
    if (!asked.queued) return setNote({ kind: "error", text: asked.message });
    setThinking(true);
    const known = new Set(ideas.map((idea) => idea.id));
    for (let poll = 0; poll < IDEAS_POLL_LIMIT; poll++) {
      await new Promise((resolve) => setTimeout(resolve, IDEAS_POLL_MS));
      if (!mounted.current) return;
      const [fresh, job] = await Promise.all([fetchIdeas(), fetchIdeasJob()]);
      if (!mounted.current) return;
      if (fresh.data) setIdeas(fresh.data.items);
      const over = job
        ? ideasRunOver(job, before)
        : hasNewIdea(known, fresh.data?.items ?? []);
      if (over) {
        setThinking(false);
        if (job) setNote({ kind: "info", text: ideasRunNote(job) });
        return;
      }
    }
    setThinking(false);
    setNote({
      kind: "info",
      text: "No new ideas yet: they show up here when they are ready.",
    });
  };

  const addMissing = (suggestion: Suggestion) => {
    const items = missingItems(suggestion);
    if (!items.length) return;
    setNote(undefined);
    working(suggestion.recipe.id, true);
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/shopping-items", {
            params: { header: writeHeader() },
            body: { items },
          }),
        "Could not add the missing ingredients.",
      );
      working(suggestion.recipe.id, false);
      if (result.error !== undefined)
        return setNote({ kind: "error", text: result.error });
      shopping.change((current) =>
        result.data.items.reduce(
          (all, added) => upsert(all, added.item),
          current,
        ),
      );
      const count = result.data.items.length;
      setNote({
        kind: "info",
        text: `Added ${count === 1 ? "1 item" : `${count} items`} for “${suggestion.recipe.title}” to the shopping list.`,
      });
    });
  };

  const saveIdea = (idea: Idea) => {
    setNote(undefined);
    working(idea.id, true);
    void recipes.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/ideas/{ideaId}/save", {
            params: { path: { ideaId: idea.id }, header: writeHeader() },
          }),
        `Could not save “${idea.title}”.`,
      );
      working(idea.id, false);
      if (result.error !== undefined)
        return setNote({ kind: "error", text: result.error });
      recipes.change((current) => upsert(current, result.data.recipe));
      setIdeas((current) => current.filter((entry) => entry.id !== idea.id));
      setNote({
        kind: "info",
        text: `Saved “${result.data.recipe.title}” as a recipe.`,
      });
      read();
    });
  };

  const dismissIdea = (idea: Idea) => {
    setNote(undefined);
    working(idea.id, true);
    void load(
      () =>
        foodClient.POST("/v1/ideas/{ideaId}/dismiss", {
          params: { path: { ideaId: idea.id }, header: writeHeader() },
        }),
      `Could not dismiss “${idea.title}”.`,
    ).then((result) => {
      working(idea.id, false);
      if (result.error !== undefined)
        return setNote({ kind: "error", text: result.error });
      setIdeas((current) => current.filter((entry) => entry.id !== idea.id));
      setNote({ kind: "info", text: `Dismissed “${idea.title}”.` });
    });
  };

  const items = list?.items ?? [];
  const excluded = list?.excluded ?? [];

  return (
    <section
      aria-labelledby="food-cook-title"
      className="bg-card space-y-3 rounded-lg border p-3"
      data-testid="food-cook"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="flex-1 text-base font-semibold" id="food-cook-title">
          What can I cook?
        </h2>
        <NativeSelect
          aria-label="Meal"
          className="[&>select]:h-9"
          onChange={(event) => {
            const next = event.target.value as MealSlot | typeof ANY_MEAL;
            setSlot(next);
            read(next);
          }}
          value={slot}
        >
          <NativeSelectOption value={ANY_MEAL}>Any meal</NativeSelectOption>
          {MEAL_SLOTS.map((entry) => (
            <NativeSelectOption key={entry.value} value={entry.value}>
              {entry.label}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <Button
          onClick={() => setPreferencesOpen(true)}
          size="lg"
          variant="ghost"
        >
          <SlidersHorizontal aria-hidden="true" />
          Preferences
        </Button>
        <Button
          onClick={() => setDiscovering(true)}
          size="lg"
          variant="outline"
        >
          <Search aria-hidden="true" />
          Discover
        </Button>
      </div>

      <p
        aria-live="polite"
        className={cn(
          "text-sm",
          note?.kind === "error" ? "text-destructive" : "text-muted-foreground",
          !note && "sr-only",
        )}
        data-testid="food-cook-note"
        role="status"
      >
        {note?.text}
      </p>

      {error && !items.length ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : items.length ? (
        <ol
          className="divide-y rounded-lg border"
          data-testid="food-suggestions"
        >
          {items.map((suggestion) => (
            <SuggestionRow
              busy={busy.has(suggestion.recipe.id)}
              key={suggestion.recipe.id}
              onAddMissing={() => addMissing(suggestion)}
              onOpen={() => onOpen(suggestion.recipe)}
              onPlan={() => setPlanning(suggestion.recipe)}
              suggestion={suggestion}
            />
          ))}
        </ol>
      ) : (
        <p className="text-muted-foreground text-sm">
          {recipes.items.length
            ? "Nothing to suggest right now."
            : "Save recipes, and Food suggests what to cook from what is at home."}
        </p>
      )}

      {excluded.length ? (
        <p
          className="text-muted-foreground text-xs"
          data-testid="food-excluded"
        >
          Left out for your dislikes:{" "}
          {excluded
            .map((entry) => `${entry.recipe.title} (${entry.disliked})`)
            .join(", ")}
        </p>
      ) : null}

      {/* The AI's recipe ideas: asked for while AI processing is on; those
          it left stay to save or dismiss when it is off. */}
      {ideasOn || ideas.length ? (
        <section aria-label="Ideas" className="space-y-1.5">
          <div className="flex items-center gap-2">
            <h3 className="flex-1 text-sm font-semibold">Ideas</h3>
            {ideasOn ? (
              <Button
                disabled={thinking}
                onClick={() => void getIdeas()}
                size="sm"
                variant="outline"
              >
                <Sparkles aria-hidden="true" />
                Get ideas
              </Button>
            ) : null}
          </div>
          {thinking ? (
            <p
              aria-live="polite"
              className="text-muted-foreground text-sm"
              data-testid="food-ideas-thinking"
              role="status"
            >
              Thinking of ideas…
            </p>
          ) : null}
          {ideas.length ? (
            <ul className="divide-y rounded-lg border" data-testid="food-ideas">
              {ideas.map((idea) => (
                <IdeaRow
                  busy={busy.has(idea.id)}
                  idea={idea}
                  key={idea.id}
                  onDismiss={() => dismissIdea(idea)}
                  onSave={() => saveIdea(idea)}
                />
              ))}
            </ul>
          ) : thinking ? null : (
            <p className="text-muted-foreground text-sm">
              No ideas yet. Get ideas from what is at home and your preferences.
            </p>
          )}
        </section>
      ) : null}

      <PlanDialog
        defaultSlot={slot === ANY_MEAL ? "dinner" : slot}
        onClose={() => setPlanning(null)}
        onPlanned={(text) => {
          setPlanning(null);
          setNote({ kind: "info", text });
          read();
        }}
        recipe={planning}
        today={today}
      />
      <PreferencesDialog
        onClose={() => setPreferencesOpen(false)}
        onSaved={() => {
          setPreferencesOpen(false);
          setNote({ kind: "info", text: "Preferences saved." });
          read();
        }}
        open={preferencesOpen}
      />
      <DiscoverDialog
        onChoose={(draft) => {
          setDiscovering(false);
          onDiscovered(draft);
        }}
        onClose={() => setDiscovering(false)}
        open={discovering}
        pantry={pantry.items}
      />
    </section>
  );
}

function SuggestionRow({
  suggestion,
  busy,
  onOpen,
  onAddMissing,
  onPlan,
}: {
  suggestion: Suggestion;
  busy: boolean;
  onOpen: () => void;
  onAddMissing: () => void;
  onPlan: () => void;
}) {
  const { recipe } = suggestion;
  return (
    <li className="space-y-1.5 px-3 py-2.5" data-testid="food-suggestion">
      <div className="flex items-start gap-2">
        <button
          className="focus-visible:ring-ring min-w-0 flex-1 rounded-md text-left font-medium wrap-anywhere outline-none focus-visible:ring-2"
          onClick={onOpen}
          type="button"
        >
          {recipe.title}
        </button>
        {recipe.methods.map((method) => (
          <Badge key={method} variant="outline">
            {methodLabel(method)}
          </Badge>
        ))}
      </div>
      <ul
        className="text-muted-foreground space-y-0.5 text-xs"
        data-testid="food-suggestion-reasons"
      >
        {suggestion.reasons.map((reason) => (
          <li className="flex items-baseline gap-2" key={reason.kind}>
            <span
              className={cn(
                "w-7 shrink-0 text-right tabular-nums",
                reason.points < 0 ? "text-warning" : "text-foreground",
              )}
            >
              {reasonPoints(reason)}
            </span>
            <span className="min-w-0 wrap-anywhere">{reason.text}</span>
          </li>
        ))}
      </ul>
      <p className="text-xs" data-testid="food-suggestion-missing">
        {missingLine(suggestion)}
      </p>
      <div className="flex flex-wrap gap-1.5">
        <Button
          aria-label={`Add missing to list: ${recipe.title}`}
          disabled={busy || !suggestion.missing.length}
          onClick={onAddMissing}
          size="lg"
          variant="outline"
        >
          <ShoppingBasket aria-hidden="true" />
          Add missing to list
        </Button>
        <Button
          aria-label={`Plan: ${recipe.title}`}
          disabled={busy}
          onClick={onPlan}
          size="lg"
          variant="outline"
        >
          <CalendarPlus aria-hidden="true" />
          Plan for…
        </Button>
      </div>
    </li>
  );
}

function IdeaRow({
  idea,
  busy,
  onSave,
  onDismiss,
}: {
  idea: Idea;
  busy: boolean;
  onSave: () => void;
  onDismiss: () => void;
}) {
  return (
    <li className="space-y-1.5 px-3 py-2.5" data-testid="food-idea">
      <p className="font-medium wrap-anywhere">{idea.title}</p>
      <p className="text-muted-foreground text-xs">
        {idea.sourceUrl ? (
          <a
            className="text-primary inline-flex items-center gap-1 underline-offset-4 hover:underline"
            href={idea.sourceUrl}
            rel="noreferrer"
            target="_blank"
          >
            {ideaSource(idea)}
            <ExternalLink aria-hidden="true" className="size-3" />
          </a>
        ) : (
          ideaSource(idea)
        )}
        {" · "}
        {idea.ingredients.length === 1
          ? "1 ingredient"
          : `${idea.ingredients.length} ingredients`}
        {idea.methods.length
          ? ` · ${idea.methods.map(methodLabel).join(", ")}`
          : ""}
      </p>
      <p className="text-sm wrap-anywhere">{idea.reason}</p>
      <div className="flex flex-wrap gap-1.5">
        <Button
          aria-label={`Save idea: ${idea.title}`}
          disabled={busy}
          onClick={onSave}
          size="lg"
        >
          Save
        </Button>
        <Button
          aria-label={`Dismiss idea: ${idea.title}`}
          disabled={busy}
          onClick={onDismiss}
          size="lg"
          variant="ghost"
        >
          Dismiss
        </Button>
      </div>
    </li>
  );
}

/** Plans a recipe for a day and meal (replacing what the slot held). */
function PlanDialog({
  recipe,
  today,
  defaultSlot,
  onClose,
  onPlanned,
}: {
  recipe: Recipe | null;
  today: string;
  defaultSlot: MealSlot;
  onClose: () => void;
  /** Planned: the line to show. */
  onPlanned: (text: string) => void;
}) {
  const current = useClosingState(recipe);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={recipe !== null}
    >
      <DialogContent>
        {current ? (
          <PlanForm
            defaultSlot={defaultSlot}
            key={current.id}
            onPlanned={onPlanned}
            recipe={current}
            today={today}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function PlanForm({
  recipe,
  today,
  defaultSlot,
  onPlanned,
}: {
  recipe: Recipe;
  today: string;
  defaultSlot: MealSlot;
  onPlanned: (text: string) => void;
}) {
  const [day, setDay] = useState(today);
  const [slot, setSlot] = useState<MealSlot>(defaultSlot);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();

  const plan = () => {
    setPending(true);
    setError(undefined);
    void load(
      () =>
        foodClient.PUT("/v1/meal-plan/{date}/{slot}", {
          params: { path: { date: day, slot }, header: writeHeader() },
          body: { recipeId: recipe.id },
        }),
      "Could not plan this meal.",
    ).then((result) => {
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      onPlanned(
        `Planned “${recipe.title}” for ${slotLabel(slot).toLowerCase()} on ${dayHeading(day)}.`,
      );
    });
  };

  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Plan {recipe.title}</DialogTitle>
        <DialogDescription>
          Pick a day and a meal; what that meal held is replaced.
        </DialogDescription>
      </DialogHeader>
      <Field>
        <FieldLabel htmlFor="food-plan-day">Day</FieldLabel>
        <DatePicker
          className="h-10 w-full"
          id="food-plan-day"
          label="Day"
          min={today}
          onChange={setDay}
          required
          today={today}
          value={day}
        />
      </Field>
      <Field>
        <FieldLabel>Meal</FieldLabel>
        <ToggleGroup
          aria-label="Meal"
          onValueChange={(value) => {
            if (value[0]) setSlot(value[0] as MealSlot);
          }}
          size="lg"
          spacing={1}
          value={[slot]}
          variant="outline"
        >
          {MEAL_SLOTS.map((entry) => (
            <ToggleGroupItem key={entry.value} value={entry.value}>
              {entry.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </Field>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <Button
        className="justify-self-start"
        disabled={pending || !day}
        onClick={plan}
        size="lg"
      >
        Plan
      </Button>
    </div>
  );
}
