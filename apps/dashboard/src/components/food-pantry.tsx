"use client";

import { ChevronRight, Plus, Star } from "lucide-react";
import { useState } from "react";

import {
  type PantryDialogState,
  PantryItemDialog,
} from "@/components/food-pantry-dialog";
import { StaplesDialog } from "@/components/food-staples-dialog";
import { Button } from "@/components/ui/button";
import { Toggle } from "@/components/ui/toggle";
import { foodClient } from "@/lib/api/food-client";
import {
  type PantryItem,
  type PantryItemResult,
  type ShoppingItem,
  type Staple,
  type Store,
  addedNote,
  endedItems,
  endedText,
  expiry,
  load,
  pantryChangeNote,
  pantryGroups,
  shortDay,
  stapleOf,
  upsert,
  writeHeader,
} from "@/lib/food";
import { usePersistentToggle } from "@/lib/persistent-toggle";
import type { SyncedList } from "@/lib/use-synced-list";
import { cn } from "@/lib/utils";

/** localStorage key for the history's expanded state. */
const HISTORY_EXPANDED = "lifeos.food.history.expanded";
/** Ended items listed before "Show all". */
const HISTORY_PREVIEW = 20;
const EMPTY =
  "text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm";

const TONES = {
  destructive: "text-destructive",
  warning: "text-warning",
  muted: "text-muted-foreground",
} as const;

type Action = "used-up" | "thrown-away" | "restock" | "running-low" | "not-low";

/** The tooltip of a pantry row's star. */
const STAPLE_HINT =
  "Staple: goes on the shopping list by itself when it runs low, is used up, or is thrown away";

/**
 * The status line, and after an item ended, the offer to put it on the
 * shopping list (none when it is already there).
 */
type Notice = { kind: "info" | "error"; text: string; offer?: PantryItem };

/**
 * The pantry: what is at home, by place (Fridge, Freezer, Pantry, Other),
 * the soonest expiry first. An item can be marked used up, thrown away, or
 * running low, put on the shopping list, starred as a staple, or edited.
 * A staple that runs low, is used up, or is thrown away goes on the list by
 * itself and the status line says so; for another item the page offers to
 * put it on the list. Ended items stay in a folded history, from which they
 * can be restocked. Staples opens the list of staples.
 */
export function FoodPantry({
  pantry,
  shopping,
  staples,
  stores,
  error,
  today,
  timeZone,
}: {
  pantry: SyncedList<PantryItem>;
  shopping: SyncedList<ShoppingItem>;
  staples: SyncedList<Staple>;
  stores: Store[];
  /** Why the pantry could not be read on the server, if it could not. */
  error?: string;
  today: string;
  timeZone: string;
}) {
  const [dialog, setDialog] = useState<PantryDialogState>(null);
  const [staplesOpen, setStaplesOpen] = useState(false);
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice] = useState<Notice>();
  const groups = pantryGroups(pantry.items);
  const ended = endedItems(pantry.items);

  const working = (id: string, on: boolean) =>
    setBusy((current) => {
      const next = new Set(current);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  const send = (item: PantryItem, action: Action) => {
    const params = { path: { itemId: item.id }, header: writeHeader() };
    switch (action) {
      case "used-up":
        return foodClient.POST("/v1/pantry-items/{itemId}/used-up", { params });
      case "thrown-away":
        return foodClient.POST("/v1/pantry-items/{itemId}/thrown-away", {
          params,
        });
      case "restock":
        return foodClient.POST("/v1/pantry-items/{itemId}/restock", { params });
      case "running-low":
        // Only a staple goes on the list; another item is offered.
        return foodClient.POST("/v1/pantry-items/{itemId}/running-low", {
          params,
          body: { addToList: false },
        });
      case "not-low":
        return foodClient.DELETE("/v1/pantry-items/{itemId}/running-low", {
          params,
        });
    }
  };

  const act = (item: PantryItem, action: Action) => {
    setNotice(undefined);
    working(item.id, true);
    // Whether it is needed already, before a restock puts it there.
    const listed = shopping.items.some(
      (entry) =>
        entry.state === "needed" &&
        entry.normalizedName === item.normalizedName,
    );
    void pantry.write(async () => {
      const result = await load<PantryItemResult>(
        () => send(item, action),
        `Could not update “${item.name}”.`,
      );
      working(item.id, false);
      if (result.error !== undefined)
        return setNotice({ kind: "error", text: result.error });
      const { restock, ...changed } = result.data;
      pantry.change((items) => upsert(items, changed));
      if (restock) {
        shopping.change((items) => upsert(items, restock.shoppingItem));
        staples.reload();
      }
      if (action === "restock") return;
      if (action === "not-low")
        return setNotice({
          kind: "info",
          text: `“${changed.name}” is no longer running low.`,
        });
      const note = pantryChangeNote(result.data, action, listed);
      setNotice({
        kind: "info",
        text: note.text,
        offer: note.offer ? changed : undefined,
      });
    });
  };

  /** Stars an item as a staple, or unstars it. */
  const toggleStaple = (item: PantryItem) => {
    setNotice(undefined);
    working(item.id, true);
    const staple = stapleOf(staples.items, item.normalizedName);
    void staples.write(async () => {
      const result = staple
        ? await load(
            () =>
              foodClient
                .DELETE("/v1/staples/{stapleId}", {
                  params: {
                    path: { stapleId: staple.id },
                    header: writeHeader(),
                  },
                })
                .then(({ response, error }) => ({
                  data: response.ok ? null : undefined,
                  error,
                })),
            `Could not change whether “${item.name}” is a staple.`,
          )
        : await load(
            () =>
              foodClient.POST("/v1/staples", {
                params: { header: writeHeader() },
                body: { name: item.name },
              }),
            `Could not change whether “${item.name}” is a staple.`,
          );
      working(item.id, false);
      if (result.error !== undefined)
        return setNotice({ kind: "error", text: result.error });
      const created = result.data;
      if (created) {
        staples.change((items) => upsert(items, created));
        setNotice({
          kind: "info",
          text: `“${created.name}” is now a staple: it goes on the shopping list by itself when it runs low, is used up, or is thrown away.`,
        });
      } else if (staple) {
        staples.change((items) =>
          items.filter((entry) => entry.id !== staple.id),
        );
        setNotice({
          kind: "info",
          text: `“${staple.name}” is no longer a staple.`,
        });
      }
    });
  };

  /** Puts an item with the same name on the shopping list. */
  const addToList = (item: PantryItem) => {
    setNotice(undefined);
    working(item.id, true);
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/shopping-items", {
            params: { header: writeHeader() },
            body: { items: [{ name: item.name }] },
          }),
        `Could not add “${item.name}” to the shopping list.`,
      );
      working(item.id, false);
      const added = result.data?.items[0];
      if (!added) return setNotice({ kind: "error", text: result.error ?? "" });
      shopping.change((items) => upsert(items, added.item));
      setNotice({
        kind: "info",
        text: added.merged
          ? addedNote(added)
          : `Added “${added.item.name}” to the shopping list.`,
      });
    });
  };

  return (
    <div className="space-y-4" data-testid="food-pantry">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-muted-foreground text-sm">
          What is at home, soonest expiry first.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button
            onClick={() => {
              // Whether each is needed or running low may have changed.
              staples.reload();
              setStaplesOpen(true);
            }}
            size="lg"
            variant="outline"
          >
            <Star aria-hidden="true" />
            Staples
            <span className="text-muted-foreground text-xs tabular-nums">
              {staples.items.length}
            </span>
          </Button>
          <Button onClick={() => setDialog({ mode: "new" })} size="lg">
            <Plus aria-hidden="true" />
            Add item
          </Button>
        </div>
      </div>

      <p
        aria-live="polite"
        className={cn(
          "text-sm",
          notice?.kind === "error"
            ? "text-destructive"
            : "text-muted-foreground",
          !notice && "sr-only",
        )}
        data-testid="food-pantry-note"
        role="status"
      >
        {notice?.text}
      </p>
      {notice?.offer ? (
        <div
          className="bg-card flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2 text-sm"
          data-testid="food-pantry-offer"
        >
          <span className="min-w-0 flex-1">
            Add “{notice.offer.name}” to the shopping list?
          </span>
          <Button
            disabled={busy.has(notice.offer.id)}
            onClick={() => addToList(notice.offer!)}
            size="lg"
            variant="outline"
          >
            Add to list
          </Button>
        </div>
      ) : null}

      {error && !pantry.items.length ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : groups.length ? (
        groups.map((group) => (
          <section
            aria-label={group.label}
            data-testid={`food-location-${group.value}`}
            key={group.value}
          >
            <div className="mb-1.5 flex min-h-6 items-center gap-2">
              <h2 className="text-sm font-semibold">{group.label}</h2>
              <span className="text-muted-foreground text-xs tabular-nums">
                {group.items.length}
              </span>
            </div>
            <ul className="bg-card divide-y rounded-lg border">
              {group.items.map((item) => (
                <PantryRow
                  busy={busy.has(item.id)}
                  item={item}
                  key={item.id}
                  onAct={(action) => act(item, action)}
                  onAddToList={() => addToList(item)}
                  onEdit={() => setDialog({ mode: "edit", item })}
                  onToggleStaple={() => toggleStaple(item)}
                  staple={Boolean(stapleOf(staples.items, item.normalizedName))}
                  today={today}
                />
              ))}
            </ul>
          </section>
        ))
      ) : (
        <p className={EMPTY}>Nothing in the pantry yet.</p>
      )}

      {ended.length ? (
        <History
          busy={busy}
          items={ended}
          onRestock={(item) => act(item, "restock")}
          timeZone={timeZone}
          today={today}
        />
      ) : null}

      <PantryItemDialog
        onClose={() => setDialog(null)}
        pantry={pantry}
        state={dialog}
        today={today}
      />
      <StaplesDialog
        onClose={() => setStaplesOpen(false)}
        open={staplesOpen}
        pantry={pantry}
        shopping={shopping}
        staples={staples}
        stores={stores}
      />
    </div>
  );
}

function PantryRow({
  item,
  today,
  busy,
  staple,
  onAct,
  onAddToList,
  onEdit,
  onToggleStaple,
}: {
  item: PantryItem;
  today: string;
  busy: boolean;
  /** Its name is a staple. */
  staple: boolean;
  onAct: (action: Action) => void;
  onAddToList: () => void;
  onEdit: () => void;
  onToggleStaple: () => void;
}) {
  const expires = item.expiresOn ? expiry(item.expiresOn, today) : undefined;
  return (
    <li
      className="flex flex-col gap-2 px-3 py-2.5 sm:flex-row sm:items-center"
      data-testid="food-pantry-item"
    >
      <button
        className="focus-visible:ring-ring min-w-0 flex-1 rounded-md text-left outline-none focus-visible:ring-2"
        onClick={onEdit}
        type="button"
      >
        <span className="sr-only">Edit </span>
        <span className="font-medium wrap-anywhere">{item.name}</span>
        {item.amount ? (
          <span className="text-muted-foreground ml-1.5 text-sm">
            {item.amount}
          </span>
        ) : null}
        {item.runningLow ? (
          <span
            className="text-warning block text-xs font-medium"
            data-slot="food-running-low"
          >
            Running low
          </span>
        ) : null}
        {expires || item.openedOn ? (
          <span className="block text-xs">
            {expires ? (
              <span
                className={cn(TONES[expires.tone], "font-medium")}
                data-slot="food-expiry"
              >
                {expires.text}
              </span>
            ) : null}
            {expires && item.openedOn ? (
              <span className="text-muted-foreground"> · </span>
            ) : null}
            {item.openedOn ? (
              <span className="text-muted-foreground">
                Opened {shortDay(item.openedOn, today)}
              </span>
            ) : null}
          </span>
        ) : null}
      </button>
      <div className="flex flex-wrap gap-1.5">
        <Toggle
          aria-label={`Staple: ${item.name}`}
          disabled={busy}
          onPressedChange={onToggleStaple}
          pressed={staple}
          size="lg"
          title={STAPLE_HINT}
        >
          <Star
            aria-hidden="true"
            className={cn(staple && "text-warning fill-current")}
          />
        </Toggle>
        <Button
          aria-label={`Used up: ${item.name}`}
          disabled={busy}
          onClick={() => onAct("used-up")}
          size="lg"
          variant="outline"
        >
          Used up
        </Button>
        <Button
          aria-label={`Thrown away: ${item.name}`}
          disabled={busy}
          onClick={() => onAct("thrown-away")}
          size="lg"
          variant="outline"
        >
          Thrown away
        </Button>
        <Toggle
          aria-label={`Running low: ${item.name}`}
          disabled={busy}
          onPressedChange={(pressed) =>
            onAct(pressed ? "running-low" : "not-low")
          }
          pressed={item.runningLow}
          size="lg"
          variant="outline"
        >
          Running low
        </Toggle>
        <Button
          aria-label={`Add to list: ${item.name}`}
          disabled={busy}
          onClick={onAddToList}
          size="lg"
          variant="ghost"
        >
          Add to list
        </Button>
      </div>
    </li>
  );
}

/**
 * Used up and thrown away items, folded by default, the most recently
 * ended first; each can be restocked.
 */
function History({
  items,
  busy,
  today,
  timeZone,
  onRestock,
}: {
  items: PantryItem[];
  busy: ReadonlySet<string>;
  today: string;
  timeZone: string;
  onRestock: (item: PantryItem) => void;
}) {
  const [expanded, setExpanded] = usePersistentToggle(HISTORY_EXPANDED);
  const [all, setAll] = useState(false);
  const shown = all ? items : items.slice(0, HISTORY_PREVIEW);
  return (
    <section aria-label="History" data-testid="food-history">
      <h2 className="mb-1.5">
        <Button
          aria-controls="food-history-list"
          aria-expanded={expanded}
          className="-ml-2 gap-2 font-semibold aria-expanded:bg-transparent"
          onClick={() => setExpanded(!expanded)}
          size="lg"
          variant="ghost"
        >
          <ChevronRight
            aria-hidden="true"
            className={cn("transition-transform", expanded && "rotate-90")}
          />
          History
          <span className="text-muted-foreground text-xs font-normal tabular-nums">
            {items.length}
          </span>
        </Button>
      </h2>
      <div className="space-y-1.5" hidden={!expanded} id="food-history-list">
        {expanded ? (
          <>
            <ul className="bg-card divide-y rounded-lg border">
              {shown.map((item) => (
                <li
                  className="flex min-h-12 items-center gap-2 px-3 py-1.5"
                  data-testid="food-history-item"
                  key={item.id}
                >
                  <span className="min-w-0 flex-1">
                    <span className="wrap-anywhere">{item.name}</span>
                    <span className="text-muted-foreground block text-xs">
                      {endedText(item, today, timeZone)}
                    </span>
                  </span>
                  <Button
                    aria-label={`Restock: ${item.name}`}
                    disabled={busy.has(item.id)}
                    onClick={() => onRestock(item)}
                    size="lg"
                    variant="outline"
                  >
                    Restock
                  </Button>
                </li>
              ))}
            </ul>
            {items.length > HISTORY_PREVIEW ? (
              <Button onClick={() => setAll(!all)} size="sm" variant="ghost">
                {all ? "Show fewer" : `Show all ${items.length}`}
              </Button>
            ) : null}
          </>
        ) : null}
      </div>
    </section>
  );
}
