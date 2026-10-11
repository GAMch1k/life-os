"use client";

import { useCallback, useState, useSyncExternalStore } from "react";

import { FoodMealPlan } from "@/components/food-meal-plan";
import { FoodPantry } from "@/components/food-pantry";
import { FoodRecipes } from "@/components/food-recipes";
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
import {
  type MealPlanEntry,
  type Recipe,
  fetchRecipes,
  weekStart,
} from "@/lib/food-recipes";
import type { FoodList, Loaded } from "@/lib/food";
import { useSyncedList } from "@/lib/use-synced-list";

const noSubscription = () => () => {};

/**
 * The Food workspace: the shopping list, grouped by aisle and made for the
 * phone in the shop, the pantry with its expiry dates, the recipes, and the
 * week's meal plan. The server renders the first view; every change is
 * applied at once and then read back from Food. The tab, the store filter,
 * and the plan's week are kept in the URL (`/food?tab=pantry`,
 * `/food?store=<id>`, `/food?tab=plan&week=2026-10-12`), so a reload or a
 * link opens them.
 */
export function FoodWorkspace({
  state,
  recipes,
  plan,
  initialTab,
  initialStore,
  initialWeek,
  today,
  timeZone,
}: {
  state: FoodWorkspaceState;
  recipes: Loaded<FoodList<Recipe>>;
  /** The meal plan of initialWeek. */
  plan: Loaded<MealPlanEntry[]>;
  initialTab: FoodTab;
  /** The store filter the URL named, checked against the stores. */
  initialStore?: string;
  /** The plan's week the URL named (its Monday); this week otherwise. */
  initialWeek?: string;
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
      initialWeek={initialWeek ?? weekStart(today)}
      plan={plan}
      recipes={recipes}
      timeZone={timeZone}
      today={today}
    />
  );
}

function ReadyWorkspace({
  initial,
  recipes: initialRecipes,
  plan,
  initialTab,
  initialStore,
  initialWeek,
  today,
  timeZone,
}: {
  initial: Extract<FoodWorkspaceState, { kind: "ready" }>;
  recipes: Loaded<FoodList<Recipe>>;
  plan: Loaded<MealPlanEntry[]>;
  initialTab: FoodTab;
  initialStore?: string;
  initialWeek: string;
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
  const recipes = useSyncedList(initialRecipes.data?.items ?? [], fetchRecipes);
  const [week, setWeek] = useState(initialWeek);

  /** The URL of a tab, with its filter or week. */
  const hrefOf = (next: FoodTab, plannedWeek = week) =>
    foodHref(
      next,
      next === "list"
        ? filter === "all"
          ? undefined
          : filter
        : next === "plan" && plannedWeek !== weekStart(today)
          ? plannedWeek
          : undefined,
    );

  const changeTab = (next: FoodTab) => {
    setTab(next);
    window.history.replaceState(null, "", hrefOf(next));
  };

  const changeWeek = (monday: string) => {
    setWeek(monday);
    window.history.replaceState(null, "", hrefOf("plan", monday));
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
        {/* Full width on a phone: large targets for the thumb, with short
            names that fit 360 px. */}
        <TabsList className="w-full group-data-horizontal/tabs:h-10 sm:w-fit">
          <TabsTrigger className="sm:px-3" value="list">
            <span className="sm:hidden">List</span>
            <span className="hidden sm:inline">Shopping list</span>
          </TabsTrigger>
          <TabsTrigger className="sm:px-3" value="pantry">
            Pantry
          </TabsTrigger>
          <TabsTrigger className="sm:px-3" value="recipes">
            Recipes
          </TabsTrigger>
          <TabsTrigger className="sm:px-3" value="plan">
            <span className="sm:hidden">Plan</span>
            <span className="hidden sm:inline">Meal plan</span>
          </TabsTrigger>
        </TabsList>
        <TabsContent value="list">
          <FoodShoppingList
            capped={initial.shopping.capped}
            // An archived store's filter shows everything again.
            filter={parseStoreFilter(filter, stores)}
            onFilterChange={changeFilter}
            onStoresChanged={storesChanged}
            pantry={pantry}
            shopping={shopping}
            stores={stores}
            storesError={storesError}
            today={today}
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
        <TabsContent value="recipes">
          <FoodRecipes
            error={initialRecipes.error}
            recipes={recipes}
            shopping={shopping}
          />
        </TabsContent>
        {/* Kept mounted, so the week and its meals stay when the tab is
            left and opened again. */}
        <TabsContent keepMounted value="plan">
          <FoodMealPlan
            initialEntries={plan.data ?? []}
            initialError={plan.error}
            initialWeek={initialWeek}
            onWeekChange={changeWeek}
            recipes={recipes}
            shopping={shopping}
            today={today}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}
