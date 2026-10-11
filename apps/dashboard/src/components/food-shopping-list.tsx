"use client";

import { ChevronRight, Plus, Store as StoreIcon } from "lucide-react";
import { type FormEvent, useState } from "react";

import { ShoppingItemDialog } from "@/components/food-item-dialog";
import { StoresDialog } from "@/components/food-stores-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { foodClient } from "@/lib/api/food-client";
import {
  FOOD_MAX_PAGES,
  FOOD_PAGE_SIZE,
  NO_STORE,
  type ShoppingItem,
  type Store,
  type StoreFilter,
  activeStores,
  addedNote,
  aisleGroups,
  boughtItems,
  load,
  parseQuickAdd,
  reopened,
  upsert,
  writeHeader,
} from "@/lib/food";
import { usePersistentToggle } from "@/lib/persistent-toggle";
import type { SyncedList } from "@/lib/use-synced-list";
import { cn } from "@/lib/utils";

/** localStorage key for the Bought section's expanded state. */
const BOUGHT_EXPANDED = "lifeos.food.bought.expanded";
const EMPTY =
  "text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm";

type Notice = { kind: "info" | "error"; text: string };

/**
 * The shopping list, made for one hand on a phone in the shop: a quick add
 * that reads "2 l milk" as an amount and a name, a store filter, the needed
 * items by aisle with large checkboxes, and the bought items folded away.
 * Ticking an item moves it at once and puts it back if Food refuses.
 */
export function FoodShoppingList({
  shopping,
  stores,
  storesError,
  filter,
  onFilterChange,
  onStoresChanged,
  capped,
}: {
  shopping: SyncedList<ShoppingItem>;
  stores: Store[];
  storesError?: string;
  filter: StoreFilter;
  onFilterChange: (filter: StoreFilter) => void;
  onStoresChanged: () => void;
  /** The list has more items than the page read. */
  capped: boolean;
}) {
  const [editing, setEditing] = useState<ShoppingItem | null>(null);
  const [storesOpen, setStoresOpen] = useState(false);
  const [error, setError] = useState<string>();
  const active = activeStores(stores);
  const groups = aisleGroups(shopping.items, filter);
  const bought = boughtItems(shopping.items);
  const filterName =
    filter === NO_STORE
      ? "without a store"
      : active.find((store) => store.id === filter)?.name;

  /** Ticks an item off (or back on), at once; Food's refusal undoes it. */
  const mark = (item: ShoppingItem, state: ShoppingItem["state"]) => {
    setError(undefined);
    shopping.change((items) =>
      items.map((entry) =>
        entry.id === item.id
          ? {
              ...entry,
              state,
              boughtAt: state === "bought" ? new Date().toISOString() : null,
            }
          : entry,
      ),
    );
    const params = { path: { itemId: item.id }, header: writeHeader() };
    void shopping.write(async () => {
      if (state === "bought") {
        const result = await load(
          () =>
            foodClient.POST("/v1/shopping-items/{itemId}/bought", { params }),
          `Could not mark “${item.name}” bought.`,
        );
        if (result.data) shopping.change((items) => upsert(items, result.data));
        else {
          shopping.change((items) => upsert(items, item));
          setError(result.error);
        }
        return;
      }
      const result = await load(
        () => foodClient.POST("/v1/shopping-items/{itemId}/needed", { params }),
        `Could not put “${item.name}” back on the list.`,
      );
      // A re-opened item may merge into one added meanwhile (another id).
      if (result.data)
        shopping.change((items) => reopened(items, item.id, result.data.item));
      else {
        shopping.change((items) => upsert(items, item));
        setError(result.error);
      }
    });
  };

  return (
    <div className="space-y-4" data-testid="food-shopping-list">
      <QuickAdd filter={filter} shopping={shopping} stores={active} />

      <div className="flex flex-wrap items-center gap-2">
        {active.length ? (
          <ToggleGroup
            aria-label="Store"
            className="flex-wrap"
            onValueChange={(chosen) => {
              // Pressing the current filter again would leave none pressed.
              if (chosen[0]) onFilterChange(chosen[0] as StoreFilter);
            }}
            size="lg"
            spacing={1}
            value={[filter]}
            variant="outline"
          >
            <ToggleGroupItem value="all">All</ToggleGroupItem>
            {active.map((store) => (
              <ToggleGroupItem key={store.id} value={store.id}>
                {store.name}
              </ToggleGroupItem>
            ))}
            <ToggleGroupItem value={NO_STORE}>No store</ToggleGroupItem>
          </ToggleGroup>
        ) : null}
        <Button
          className="ml-auto"
          onClick={() => setStoresOpen(true)}
          size="lg"
          variant="ghost"
        >
          <StoreIcon aria-hidden="true" />
          Stores
        </Button>
      </div>
      {storesError ? (
        <p className="text-destructive text-sm">{storesError}</p>
      ) : null}

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      {capped ? (
        <p className={EMPTY} role="note">
          Only the first {FOOD_PAGE_SIZE * FOOD_MAX_PAGES} items are shown.
        </p>
      ) : null}

      {groups.length ? (
        <div className="space-y-4">
          {groups.map((group) => (
            <section
              aria-label={group.label}
              data-testid={`food-aisle-${group.value}`}
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
                  <NeededRow
                    item={item}
                    key={item.id}
                    onBought={() => mark(item, "bought")}
                    onEdit={() => setEditing(item)}
                    // The filter already says which store.
                    showStore={filter === "all"}
                  />
                ))}
              </ul>
            </section>
          ))}
        </div>
      ) : (
        <p className={EMPTY}>
          {filterName
            ? `Nothing to buy ${filter === NO_STORE ? filterName : `at ${filterName}`}.`
            : "The shopping list is empty."}
        </p>
      )}

      {bought.length ? (
        <BoughtSection
          items={bought}
          onReopen={(item) => mark(item, "needed")}
          shopping={shopping}
        />
      ) : null}

      <ShoppingItemDialog
        item={editing}
        onClose={() => setEditing(null)}
        onStoresChanged={onStoresChanged}
        shopping={shopping}
        stores={stores}
      />
      <StoresDialog
        onChanged={onStoresChanged}
        onClose={() => setStoresOpen(false)}
        open={storesOpen}
        stores={stores}
      />
    </div>
  );
}

/**
 * Enter adds what was typed, with its amount split off, to the store
 * picked beside it (the filtered store by default). The field clears and
 * keeps focus at once, so items can be typed one after another.
 */
function QuickAdd({
  shopping,
  stores,
  filter,
}: {
  shopping: SyncedList<ShoppingItem>;
  stores: Store[];
  filter: StoreFilter;
}) {
  const storeOf = (value: StoreFilter) =>
    stores.some((store) => store.id === value) ? value : "";
  const [text, setText] = useState("");
  const [storeId, setStoreId] = useState(() => storeOf(filter));
  const [seenFilter, setSeenFilter] = useState(filter);
  const [notice, setNotice] = useState<Notice>();
  // Filtering by a store adds to it, so new items stay in view.
  if (filter !== seenFilter) {
    setSeenFilter(filter);
    setStoreId(storeOf(filter));
  }

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const typed = text;
    const { name, amount } = parseQuickAdd(typed);
    if (!name) return;
    setText("");
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/shopping-items", {
            params: { header: writeHeader() },
            body: {
              items: [
                {
                  name,
                  ...(amount ? { amount } : {}),
                  ...(storeId ? { storeId } : {}),
                },
              ],
            },
          }),
        `Could not add “${name}”.`,
      );
      const added = result.data?.items[0];
      if (!added) {
        setNotice({ kind: "error", text: result.error ?? "" });
        // Give the text back unless something new was typed meanwhile.
        setText((current) => current || typed);
        return;
      }
      shopping.change((items) => upsert(items, added.item));
      setNotice({ kind: "info", text: addedNote(added) });
    });
  };

  return (
    <form className="space-y-1.5" onSubmit={submit}>
      <div className="flex gap-2">
        <Input
          aria-label="Add to the shopping list"
          autoComplete="off"
          className="h-11 flex-1 text-base"
          enterKeyHint="enter"
          maxLength={260}
          onChange={(event) => setText(event.target.value)}
          placeholder="Add an item, such as 2 l milk"
          value={text}
        />
        {stores.length ? (
          <NativeSelect
            aria-label="Store for new items"
            className="w-28 shrink-0 sm:w-36 [&>select]:h-11"
            onChange={(event) => setStoreId(event.target.value)}
            value={storeId}
          >
            <NativeSelectOption value="">No store</NativeSelectOption>
            {stores.map((store) => (
              <NativeSelectOption key={store.id} value={store.id}>
                {store.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        ) : null}
        <Button
          aria-label="Add"
          className="size-11 shrink-0"
          disabled={!text.trim()}
          size="icon-lg"
          type="submit"
        >
          <Plus aria-hidden="true" />
        </Button>
      </div>
      <p
        aria-live="polite"
        className={cn(
          "text-xs",
          notice?.kind === "error"
            ? "text-destructive"
            : "text-muted-foreground",
          !notice && "sr-only",
        )}
        data-testid="food-quick-add-note"
        role="status"
      >
        {notice?.text}
      </p>
    </form>
  );
}

/**
 * A needed item: a large checkbox on the left for the thumb, and the rest
 * of the row opens the item to edit it.
 */
function NeededRow({
  item,
  showStore,
  onBought,
  onEdit,
}: {
  item: ShoppingItem;
  showStore: boolean;
  onBought: () => void;
  onEdit: () => void;
}) {
  return (
    <li className="flex min-h-12 items-stretch" data-testid="food-item">
      <Label className="flex w-12 shrink-0 cursor-pointer items-center justify-center">
        <Checkbox
          aria-label={`${item.name} bought`}
          checked={false}
          className="size-6 rounded-md"
          onCheckedChange={onBought}
        />
      </Label>
      <button
        className="focus-visible:ring-ring flex min-w-0 flex-1 items-center gap-2 rounded-r-lg py-2.5 pr-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset"
        onClick={onEdit}
        type="button"
      >
        <span className="sr-only">Edit </span>
        <span className="min-w-0 flex-1">
          <span className="font-medium wrap-anywhere">{item.name}</span>
          {item.amount ? (
            <span className="text-muted-foreground ml-1.5 text-sm">
              {item.amount}
            </span>
          ) : null}
          {item.note ? (
            <span className="text-muted-foreground block truncate text-xs">
              {item.note}
            </span>
          ) : null}
        </span>
        {showStore && item.storeName ? (
          <Badge className="max-w-[40%] truncate" variant="secondary">
            {item.storeName}
          </Badge>
        ) : null}
      </button>
    </li>
  );
}

/**
 * Bought items, folded by default with a count. Unticking one puts it back
 * on the list; Clear bought deletes them after a confirmation.
 */
function BoughtSection({
  items,
  shopping,
  onReopen,
}: {
  items: ShoppingItem[];
  shopping: SyncedList<ShoppingItem>;
  onReopen: (item: ShoppingItem) => void;
}) {
  const [expanded, setExpanded] = usePersistentToggle(BOUGHT_EXPANDED);
  const [confirming, setConfirming] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [error, setError] = useState<string>();

  const clear = () =>
    void shopping.write(async () => {
      setClearing(true);
      setError(undefined);
      const result = await load(
        () =>
          foodClient.POST("/v1/shopping-items/clear-bought", {
            params: { header: writeHeader() },
            body: {},
          }),
        "Could not clear the bought items.",
      );
      setClearing(false);
      setConfirming(false);
      if (result.error !== undefined) return setError(result.error);
      shopping.change((current) =>
        current.filter((item) => item.state !== "bought"),
      );
    });

  return (
    <section aria-label="Bought" data-testid="food-bought">
      <h2 className="mb-1.5">
        <Button
          aria-controls="food-bought-list"
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
          Bought
          <span className="text-muted-foreground text-xs font-normal tabular-nums">
            {items.length}
          </span>
        </Button>
      </h2>
      <div className="space-y-2" hidden={!expanded} id="food-bought-list">
        {expanded ? (
          <>
            <ul className="bg-card divide-y rounded-lg border">
              {items.map((item) => (
                <li data-testid="food-bought-item" key={item.id}>
                  {/* The whole row is the checkbox's label: a tap anywhere
                      puts the item back. */}
                  <Label className="flex min-h-12 cursor-pointer items-center gap-0 text-base leading-normal font-normal">
                    <span className="flex w-12 shrink-0 justify-center">
                      <Checkbox
                        aria-label={`${item.name} bought`}
                        checked
                        className="size-6 rounded-md"
                        onCheckedChange={() => onReopen(item)}
                      />
                    </span>
                    <span className="text-muted-foreground min-w-0 flex-1 py-2.5 pr-3 wrap-anywhere line-through">
                      {item.name}
                      {item.amount ? (
                        <span className="ml-1.5 text-sm">{item.amount}</span>
                      ) : null}
                    </span>
                  </Label>
                </li>
              ))}
            </ul>
            {confirming ? (
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <p className="text-sm font-medium">
                  Delete the {items.length} bought{" "}
                  {items.length === 1 ? "item" : "items"} from the list?
                </p>
                <div className="flex gap-2">
                  <Button
                    disabled={clearing}
                    onClick={clear}
                    size="lg"
                    variant="destructive"
                  >
                    {clearing ? "Clearing…" : "Clear"}
                  </Button>
                  <Button
                    disabled={clearing}
                    onClick={() => setConfirming(false)}
                    size="lg"
                    variant="outline"
                  >
                    Keep
                  </Button>
                </div>
              </div>
            ) : (
              <Button
                onClick={() => setConfirming(true)}
                size="lg"
                variant="outline"
              >
                Clear bought
              </Button>
            )}
            {error ? (
              <p className="text-destructive text-sm" role="alert">
                {error}
              </p>
            ) : null}
          </>
        ) : null}
      </div>
    </section>
  );
}
