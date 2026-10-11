"use client";

import { useCallback, useState, useSyncExternalStore } from "react";

import { FoodPantry } from "@/components/food-pantry";
import { FoodShoppingList } from "@/components/food-shopping-list";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  type FoodTab,
  type FoodWorkspaceState,
  type StoreFilter,
  fetchPantry,
  fetchShoppingList,
  fetchStores,
  foodHref,
  parseStoreFilter,
} from "@/lib/food";
import { useSyncedList } from "@/lib/use-synced-list";

const noSubscription = () => () => {};

/**
 * The Food workspace: the shopping list, grouped by aisle and made for the
 * phone in the shop, and the pantry with its expiry dates. The server
 * renders the first view; every change is applied at once and then read
 * back from Food. The tab and the store filter are kept in the URL
 * (`/food?tab=pantry`, `/food?store=<id>`), so a reload or a link opens them.
 */
export function FoodWorkspace({
  state,
  initialTab,
  initialStore,
  today,
  timeZone,
}: {
  state: FoodWorkspaceState;
  initialTab: FoodTab;
  /** The store filter the URL named, checked against the stores. */
  initialStore?: string;
  /** The owner's today (YYYY-MM-DD), decided once by the server render. */
  today: string;
  /** The owner's timezone, for when pantry items ended. */
  timeZone: string;
}) {
  if (state.kind === "unavailable")
    return (
      <div className="mx-auto max-w-3xl space-y-4">
        <h1 className="text-xl font-semibold tracking-tight">Food</h1>
        <Card>
          <CardHeader>
            <CardTitle>Food is unavailable</CardTitle>
            <CardDescription>
              Food is unavailable right now ({state.error.replace(/\.$/, "")}).
              Your lists have not been replaced with placeholders.
            </CardDescription>
          </CardHeader>
        </Card>
      </div>
    );
  return (
    <ReadyWorkspace
      initial={state}
      initialStore={initialStore}
      initialTab={initialTab}
      timeZone={timeZone}
      today={today}
    />
  );
}

function ReadyWorkspace({
  initial,
  initialTab,
  initialStore,
  today,
  timeZone,
}: {
  initial: Extract<FoodWorkspaceState, { kind: "ready" }>;
  initialTab: FoodTab;
  initialStore?: string;
  today: string;
  timeZone: string;
}) {
  // Busy until hydrated: the server's HTML takes no taps yet.
  const hydrated = useSyncExternalStore(
    noSubscription,
    () => true,
    () => false,
  );
  const [tab, setTab] = useState(initialTab);
  const [stores, setStores] = useState(initial.stores.data ?? []);
  const [storesError, setStoresError] = useState(initial.stores.error);
  const [filter, setFilter] = useState<StoreFilter>(() =>
    parseStoreFilter(initialStore, initial.stores.data ?? []),
  );
  const shopping = useSyncedList(initial.shopping.items, fetchShoppingList);
  const pantry = useSyncedList(initial.pantry.data?.items ?? [], fetchPantry);

  const changeTab = (next: FoodTab) => {
    setTab(next);
    window.history.replaceState(
      null,
      "",
      foodHref(next, filter === "all" ? undefined : filter),
    );
  };

  const changeFilter = (next: StoreFilter) => {
    setFilter(next);
    window.history.replaceState(
      null,
      "",
      foodHref("list", next === "all" ? undefined : next),
    );
  };

  const { reload: reloadShopping } = shopping;
  // A renamed or archived store changes how items show theirs.
  const storesChanged = useCallback(() => {
    void fetchStores().then((result) => {
      if (!result.data) return;
      setStores(result.data);
      setStoresError(undefined);
    });
    reloadShopping();
  }, [reloadShopping]);

  return (
    <div
      aria-busy={!hydrated}
      className="mx-auto max-w-3xl space-y-4"
      data-testid="food-workspace"
      data-time-zone={timeZone}
      data-today={today}
    >
      <h1 className="text-xl font-semibold tracking-tight">Food</h1>
      <Tabs
        className="gap-3"
        onValueChange={(value) => changeTab(value as FoodTab)}
        value={tab}
      >
        {/* Full width on a phone: two large targets for the thumb. */}
        <TabsList className="w-full group-data-horizontal/tabs:h-10 sm:w-fit">
          <TabsTrigger className="sm:px-3" value="list">
            Shopping list
          </TabsTrigger>
          <TabsTrigger className="sm:px-3" value="pantry">
            Pantry
          </TabsTrigger>
        </TabsList>
        <TabsContent value="list">
          <FoodShoppingList
            capped={initial.shopping.capped}
            // An archived store's filter shows everything again.
            filter={parseStoreFilter(filter, stores)}
            onFilterChange={changeFilter}
            onStoresChanged={storesChanged}
            shopping={shopping}
            stores={stores}
            storesError={storesError}
          />
        </TabsContent>
        <TabsContent value="pantry">
          <FoodPantry
            error={initial.pantry.error}
            pantry={pantry}
            shopping={shopping}
            timeZone={timeZone}
            today={today}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}
