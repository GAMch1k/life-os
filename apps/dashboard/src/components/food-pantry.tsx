"use client";

import { ChevronRight, Plus } from "lucide-react";
import { useState } from "react";

import {
  type PantryDialogState,
  PantryItemDialog,
} from "@/components/food-pantry-dialog";
import { Button } from "@/components/ui/button";
import { foodClient } from "@/lib/api/food-client";
import {
  type PantryItem,
  type ShoppingItem,
  addedNote,
  endedItems,
  endedText,
  expiry,
  load,
  pantryGroups,
  shortDay,
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

type Action = "used-up" | "thrown-away" | "restock";

type Notice = { kind: "info" | "error"; text: string };

/**
 * The pantry: what is at home, by place (Fridge, Freezer, Pantry, Other),
 * the soonest expiry first. An item can be marked used up or thrown away,
 * put on the shopping list, or edited; ended items stay in a folded
 * history, from which they can be restocked.
 */
export function FoodPantry({
  pantry,
  shopping,
  error,
  today,
  timeZone,
}: {
  pantry: SyncedList<PantryItem>;
  shopping: SyncedList<ShoppingItem>;
  /** Why the pantry could not be read on the server, if it could not. */
  error?: string;
  today: string;
  timeZone: string;
}) {
  const [dialog, setDialog] = useState<PantryDialogState>(null);
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

  const act = (item: PantryItem, action: Action) => {
    setNotice(undefined);
    working(item.id, true);
    const params = { path: { itemId: item.id }, header: writeHeader() };
    void pantry.write(async () => {
      const result = await load(
        () =>
          action === "used-up"
            ? foodClient.POST("/v1/pantry-items/{itemId}/used-up", { params })
            : action === "thrown-away"
              ? foodClient.POST("/v1/pantry-items/{itemId}/thrown-away", {
                  params,
                })
              : foodClient.POST("/v1/pantry-items/{itemId}/restock", {
                  params,
                }),
        `Could not update “${item.name}”.`,
      );
      working(item.id, false);
      if (result.error !== undefined)
        return setNotice({ kind: "error", text: result.error });
      pantry.change((items) => upsert(items, result.data));
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
        <Button onClick={() => setDialog({ mode: "new" })} size="lg">
          <Plus aria-hidden="true" />
          Add item
        </Button>
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
    </div>
  );
}

function PantryRow({
  item,
  today,
  busy,
  onAct,
  onAddToList,
  onEdit,
}: {
  item: PantryItem;
  today: string;
  busy: boolean;
  onAct: (action: Action) => void;
  onAddToList: () => void;
  onEdit: () => void;
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
