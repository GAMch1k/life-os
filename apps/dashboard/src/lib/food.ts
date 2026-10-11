import { foodClient } from "@/lib/api/food-client";
import type { components } from "@/lib/api/generated/food";
import { addDays, dayKey } from "@/lib/time-zone";

// The Food workspace's data and its pure rules: the quick-add parser, the
// aisle and pantry groupings, expiry texts, and what Home shows. Everything
// but the fetch helpers is pure and unit-tested.

type Schemas = components["schemas"];

export type ShoppingItem = Schemas["ShoppingItem"];
export type ShoppingItemResult = Schemas["ShoppingItemResult"];
export type Store = Schemas["Store"];
export type Aisle = Schemas["Aisle"];
export type PantryItem = Schemas["PantryItem"];
export type PantryLocation = Schemas["PantryLocation"];
export type UpdateShoppingItemBody = Schemas["UpdateShoppingItemRequest"];
export type NewPantryItem = Schemas["NewPantryItem"];
export type UpdatePantryItemBody = Schemas["UpdatePantryItemRequest"];
export type PutAwayItem = Schemas["PutAwayItem"];
export type PantryItemResult = Schemas["PantryItemResult"];
export type Restock = Schemas["Restock"];
export type Staple = Schemas["Staple"];

export const FOOD_TABS = ["list", "pantry", "recipes", "plan"] as const;
export type FoodTab = (typeof FOOD_TABS)[number];

/** A tab named in the URL (`?tab=pantry`); the shopping list otherwise. */
export function parseFoodTab(value: unknown): FoodTab {
  return FOOD_TABS.find((tab) => tab === value) ?? "list";
}

/**
 * Where a tab lives, so a reload or a link opens it. `param` is the
 * list's store filter, or the meal plan's week (its Monday) when it is not
 * this week.
 */
export function foodHref(tab: FoodTab, param?: string) {
  if (tab === "list")
    return param ? `/food?store=${encodeURIComponent(param)}` : "/food";
  if (tab === "plan" && param) return `/food?tab=plan&week=${param}`;
  return `/food?tab=${tab}`;
}

/** The aisles in the order the list shows them, as the service orders them. */
export const AISLES: { value: Aisle; label: string }[] = [
  { value: "produce", label: "Produce" },
  { value: "bakery", label: "Bakery" },
  { value: "dairy", label: "Dairy" },
  { value: "meat_fish", label: "Meat & fish" },
  { value: "frozen", label: "Frozen" },
  { value: "pantry", label: "Pantry" },
  { value: "drinks", label: "Drinks" },
  { value: "snacks", label: "Snacks" },
  { value: "household", label: "Household" },
  { value: "drugstore", label: "Drugstore" },
  { value: "other", label: "Other" },
];

export const LOCATIONS: { value: PantryLocation; label: string }[] = [
  { value: "fridge", label: "Fridge" },
  { value: "freezer", label: "Freezer" },
  { value: "pantry", label: "Pantry" },
  { value: "other", label: "Other" },
];

export function aisleLabel(aisle: string) {
  return AISLES.find((entry) => entry.value === aisle)?.label ?? aisle;
}

export function locationLabel(location: string) {
  return LOCATIONS.find((entry) => entry.value === location)?.label ?? location;
}

// ---- Quick add --------------------------------------------------------------

// A count or a measure: "2", "1,5", "0.5", "2-3", "1/2", "½", or "2x".
const NUMBER = String.raw`(?:\d+(?:[.,]\d+)?(?:\s*[-–]\s*\d+(?:[.,]\d+)?)?|\d+\s*\/\s*\d+|[½¼¾⅓⅔])`;
const TIMES = String.raw`(?:\d+\s*[x×]|[x×]\s*\d+)`;
// Units an amount may carry, in English, German, and Russian. Anything else
// after the number is the item's name ("6 eggs" is 6 of "eggs").
const UNITS = [
  "mg",
  "g",
  "gr",
  "kg",
  "ml",
  "cl",
  "dl",
  "l",
  "ltr",
  "liter",
  "liters",
  "litre",
  "litres",
  "oz",
  "lb",
  "lbs",
  "pc",
  "pcs",
  "piece",
  "pieces",
  "pack",
  "packs",
  "pk",
  "pkg",
  "packet",
  "packets",
  "can",
  "cans",
  "tin",
  "tins",
  "bottle",
  "bottles",
  "jar",
  "jars",
  "bag",
  "bags",
  "box",
  "boxes",
  "bunch",
  "bunches",
  "loaf",
  "loaves",
  "roll",
  "rolls",
  "dozen",
  "st",
  "stk",
  "stück",
  "packung",
  "packungen",
  "pck",
  "pkt",
  "dose",
  "dosen",
  "flasche",
  "flaschen",
  "glas",
  "gläser",
  "becher",
  "beutel",
  "bund",
  "kiste",
  "kisten",
  "kasten",
  "netz",
  "tüte",
  "tüten",
  "rolle",
  "rollen",
  "tafel",
  "tafeln",
  "шт",
  "г",
  "гр",
  "кг",
  "л",
  "мл",
  "уп",
  "упак",
  "упаковка",
  "упаковки",
  "пачка",
  "пачки",
  "пачек",
  "бут",
  "бутылка",
  "бутылки",
  "банка",
  "банки",
  "пакет",
  "пакета",
  "пакетов",
];
// Longest first, so "kg" is not read as "g" and "ltr" not as "l".
const UNIT = `(?:${[...UNITS].sort((a, b) => b.length - a.length).join("|")})\\.?`;
const AMOUNT = String.raw`(?:${TIMES}|${NUMBER}(?:\s*${UNIT})?)`;
// The amount must be a word of its own: "7up" is a name, "500g" an amount.
const LEADING = new RegExp(String.raw`^(${AMOUNT})\s+(.+)$`, "iu");
const TRAILING = new RegExp(String.raw`^(.+?)\s+(${AMOUNT})$`, "iu");
const UNIT_ONLY = new RegExp(`^${UNIT}$`, "iu");

/**
 * Splits a quick-add line into a name and an amount written before or after
 * it: "2 l milk", "milk 2 l", "6 eggs", "500 g Hackfleisch", "Milch 1,5 l".
 * The amount keeps how it was written. A line that is only an amount ("2 l")
 * stays the name, since a name is required.
 */
export function parseQuickAdd(text: string): { name: string; amount?: string } {
  const line = text.replace(/\s+/g, " ").trim();
  const leading = LEADING.exec(line);
  const trailing = leading ? null : TRAILING.exec(line);
  const [amount, name] = leading
    ? [leading[1], leading[2]]
    : trailing
      ? [trailing[2], trailing[1]]
      : [];
  // "2 l" is not 2 of "l".
  if (!amount || !name || UNIT_ONLY.test(name)) return { name: line };
  return { name, amount };
}

// ---- Shopping list ----------------------------------------------------------

/** The store filter: every item, one store's (its id), or items without one. */
export type StoreFilter = "all" | "none" | string;

export const NO_STORE = "none";

/** A store filter named in the URL, when it is still a store (or "none"). */
export function parseStoreFilter(
  value: unknown,
  stores: Pick<Store, "id" | "archived">[],
): StoreFilter {
  if (value === NO_STORE) return NO_STORE;
  return typeof value === "string" &&
    stores.some((store) => store.id === value && !store.archived)
    ? value
    : "all";
}

export function matchesStore(
  item: Pick<ShoppingItem, "storeId">,
  filter: StoreFilter,
) {
  if (filter === "all") return true;
  if (filter === NO_STORE) return item.storeId === null;
  return item.storeId === filter;
}

const AISLE_RANK = new Map(AISLES.map((aisle, index) => [aisle.value, index]));
// A fixed locale, so the server and every browser sort alike.
const collator = new Intl.Collator("en");

function byAisleThenName(a: ShoppingItem, b: ShoppingItem) {
  const rank =
    (AISLE_RANK.get(a.aisle) ?? AISLES.length) -
    (AISLE_RANK.get(b.aisle) ?? AISLES.length);
  return rank || collator.compare(a.normalizedName, b.normalizedName);
}

/**
 * The needed items matching the filter, grouped by aisle in the list's
 * order and by name within an aisle; empty aisles are left out.
 */
export function aisleGroups(items: ShoppingItem[], filter: StoreFilter) {
  const needed = items
    .filter((item) => item.state === "needed" && matchesStore(item, filter))
    .sort(byAisleThenName);
  return AISLES.map((aisle) => ({
    ...aisle,
    items: needed.filter((item) => item.aisle === aisle.value),
  })).filter((group) => group.items.length > 0);
}

/** Bought items, the most recently bought first. */
export function boughtItems(items: ShoppingItem[]) {
  return items
    .filter((item) => item.state === "bought")
    .sort(
      (a, b) =>
        Date.parse(b.boughtAt ?? b.updatedAt) -
        Date.parse(a.boughtAt ?? a.updatedAt),
    );
}

/** The list with `item` added, or put in place of the item with its id. */
export function upsert<T extends { id: string }>(items: T[], item: T) {
  return items.some((entry) => entry.id === item.id)
    ? items.map((entry) => (entry.id === item.id ? item : entry))
    : [...items, item];
}

/**
 * The list after a bought item was put back. When an item with its name was
 * added meanwhile the two merged: the service returns the needed item (with
 * another id) and has deleted the bought one.
 */
export function reopened(
  items: ShoppingItem[],
  boughtId: string,
  result: ShoppingItem,
) {
  return upsert(
    items.filter((item) => item.id !== boughtId || result.id === boughtId),
    result,
  );
}

/** The line under the quick add after an add: what happened, in plain words. */
export function addedNote({ item, merged }: ShoppingItemResult) {
  return merged
    ? `“${item.name}” was already on the list — updated.`
    : `Added “${item.name}” to ${aisleLabel(item.aisle)}.`;
}

/** The store picker's value for a store typed in by name. */
export const NEW_STORE = "new";

/** The edit dialog's fields; `store` is "", a store id, or NEW_STORE. */
export type ShoppingDraft = {
  name: string;
  amount: string;
  note: string;
  aisle: Aisle;
  store: string;
  newStore: string;
};

export type DraftErrors = { name?: string; newStore?: string };

export function shoppingDraft(item: ShoppingItem): ShoppingDraft {
  return {
    name: item.name,
    amount: item.amount ?? "",
    note: item.note ?? "",
    aisle: item.aisle,
    store: item.storeId ?? "",
    newStore: "",
  };
}

/** Text a field holds: trimmed, and null when empty (the API's "cleared"). */
function optional(value: string) {
  const text = value.trim();
  return text ? text : null;
}

/**
 * The PATCH body for what the dialog changed, or null when nothing did. A
 * store typed in goes by name: Food matches an existing one or creates it.
 */
export function shoppingItemPatch(
  item: ShoppingItem,
  draft: ShoppingDraft,
): { body: UpdateShoppingItemBody | null; errors: DraftErrors } {
  const errors: DraftErrors = {};
  const name = draft.name.trim();
  if (!name) errors.name = "Give the item a name.";
  const newStore = draft.newStore.trim();
  if (draft.store === NEW_STORE && !newStore)
    errors.newStore = "Name the store.";
  if (Object.keys(errors).length) return { body: null, errors };
  const body: UpdateShoppingItemBody = {};
  if (name !== item.name) body.name = name;
  const amount = optional(draft.amount);
  if (amount !== item.amount) body.amount = amount;
  const note = optional(draft.note);
  if (note !== item.note) body.note = note;
  if (draft.aisle !== item.aisle) body.aisle = draft.aisle;
  if (draft.store === NEW_STORE) body.storeName = newStore;
  else if ((draft.store || null) !== item.storeId)
    body.storeId = draft.store || null;
  return { body: Object.keys(body).length ? body : null, errors };
}

/** Active stores in the list's order: by sort order, then name. */
export function activeStores(stores: Store[]) {
  return stores
    .filter((store) => !store.archived)
    .sort(
      (a, b) => a.sortOrder - b.sortOrder || collator.compare(a.name, b.name),
    );
}

/**
 * Moves the store at `index` up (-1) or down (+1) and numbers the stores
 * 10, 20, 30, …: the sort orders that change, as PATCHes to send.
 */
export function storeMoves(stores: Store[], index: number, delta: -1 | 1) {
  const target = index + delta;
  if (target < 0 || target >= stores.length) return [];
  const order = [...stores];
  [order[index], order[target]] = [order[target], order[index]];
  return order
    .map((store, position) => ({
      id: store.id,
      sortOrder: (position + 1) * 10,
    }))
    .filter(
      (move) =>
        stores.find((store) => store.id === move.id)?.sortOrder !==
        move.sortOrder,
    );
}

// ---- Pantry -----------------------------------------------------------------

/** Days from `today` to `day` (both YYYY-MM-DD); negative in the past. */
export function daysBetween(today: string, day: string) {
  return Math.round(
    (Date.parse(`${day}T00:00:00Z`) - Date.parse(`${today}T00:00:00Z`)) /
      86_400_000,
  );
}

const shortDays = new Map<boolean, Intl.DateTimeFormat>();

/** A plain day as "24 Oct", with the year when it is not this year's. */
export function shortDay(day: string, today: string) {
  const withYear = day.slice(0, 4) !== today.slice(0, 4);
  let format = shortDays.get(withYear);
  if (!format) {
    format = new Intl.DateTimeFormat("en-GB", {
      day: "numeric",
      month: "short",
      ...(withYear ? { year: "numeric" } : {}),
      timeZone: "UTC",
    });
    shortDays.set(withYear, format);
  }
  return format.format(new Date(`${day}T00:00:00Z`));
}

/** How an expiry date reads, and how urgent it looks. */
export type Expiry = {
  text: string;
  /** destructive: expired; warning: today to 2 days; muted: later. */
  tone: "destructive" | "warning" | "muted";
  days: number;
};

/**
 * An expiry date relative to the owner's today: "Expired 2 days ago",
 * "Expired yesterday", "Expires today", "Expires tomorrow", "Expires in 3
 * days" (up to a week), then "Expires 24 Oct".
 */
export function expiry(expiresOn: string, today: string): Expiry {
  const days = daysBetween(today, expiresOn);
  const tone = days < 0 ? "destructive" : days <= 2 ? "warning" : "muted";
  const text =
    days < -1
      ? `Expired ${-days} days ago`
      : days === -1
        ? "Expired yesterday"
        : days === 0
          ? "Expires today"
          : days === 1
            ? "Expires tomorrow"
            : days < 7
              ? `Expires in ${days} days`
              : `Expires ${shortDay(expiresOn, today)}`;
  return { text, tone, days };
}

/** A day relative to today, for history lines: "today", "yesterday", "9 Oct". */
export function relativeDay(day: string, today: string) {
  const days = daysBetween(today, day);
  if (days === 0) return "today";
  if (days === -1) return "yesterday";
  return shortDay(day, today);
}

/** An ended item's line: "Used up today", "Thrown away 9 Oct". */
export function endedText(
  item: Pick<PantryItem, "state" | "endedAt" | "updatedAt">,
  today: string,
  timeZone: string,
) {
  const verb = item.state === "thrown_away" ? "Thrown away" : "Used up";
  const at = item.endedAt ?? item.updatedAt;
  return `${verb} ${relativeDay(dayKey(at, timeZone), today)}`;
}

function bySoonestExpiry(a: PantryItem, b: PantryItem) {
  if (a.expiresOn !== b.expiresOn) {
    if (a.expiresOn === null) return 1;
    if (b.expiresOn === null) return -1;
    return a.expiresOn < b.expiresOn ? -1 : 1;
  }
  return collator.compare(a.normalizedName, b.normalizedName);
}

/**
 * In-stock items by location (Fridge, Freezer, Pantry, Other), soonest
 * expiry first and items without a date last; empty locations are left out.
 */
export function pantryGroups(items: PantryItem[]) {
  const inStock = items
    .filter((item) => item.state === "in_stock")
    .sort(bySoonestExpiry);
  return LOCATIONS.map((location) => ({
    ...location,
    items: inStock.filter((item) => item.location === location.value),
  })).filter((group) => group.items.length > 0);
}

/** Used up and thrown away items, the most recently ended first. */
export function endedItems(items: PantryItem[]) {
  return items
    .filter((item) => item.state !== "in_stock")
    .sort(
      (a, b) =>
        Date.parse(b.endedAt ?? b.updatedAt) -
        Date.parse(a.endedAt ?? a.updatedAt),
    );
}

/** The pantry dialog's fields; `location` "" lets Food choose by name. */
export type PantryDraft = {
  name: string;
  amount: string;
  location: "" | PantryLocation;
  expiresOn: string;
  openedOn: string;
  note: string;
};

export function pantryDraft(item?: PantryItem): PantryDraft {
  return {
    name: item?.name ?? "",
    amount: item?.amount ?? "",
    location: item?.location ?? "",
    expiresOn: item?.expiresOn ?? "",
    openedOn: item?.openedOn ?? "",
    note: item?.note ?? "",
  };
}

/** A new pantry item from the dialog; what is left empty is not sent. */
export function newPantryItem(draft: PantryDraft): {
  body: NewPantryItem | null;
  errors: DraftErrors;
} {
  const name = draft.name.trim();
  if (!name) return { body: null, errors: { name: "Give the item a name." } };
  const body: NewPantryItem = { name };
  const amount = optional(draft.amount);
  if (amount) body.amount = amount;
  if (draft.location) body.location = draft.location;
  if (draft.expiresOn) body.expiresOn = draft.expiresOn;
  if (draft.openedOn) body.openedOn = draft.openedOn;
  const note = optional(draft.note);
  if (note) body.note = note;
  return { body, errors: {} };
}

/** The PATCH body for what the dialog changed, or null when nothing did. */
export function pantryItemPatch(
  item: PantryItem,
  draft: PantryDraft,
): { body: UpdatePantryItemBody | null; errors: DraftErrors } {
  const name = draft.name.trim();
  if (!name) return { body: null, errors: { name: "Give the item a name." } };
  const body: UpdatePantryItemBody = {};
  if (name !== item.name) body.name = name;
  const amount = optional(draft.amount);
  if (amount !== item.amount) body.amount = amount;
  if (draft.location && draft.location !== item.location)
    body.location = draft.location;
  const expiresOn = draft.expiresOn || null;
  if (expiresOn !== item.expiresOn) body.expiresOn = expiresOn;
  const openedOn = draft.openedOn || null;
  if (openedOn !== item.openedOn) body.openedOn = openedOn;
  const note = optional(draft.note);
  if (note !== item.note) body.note = note;
  return { body: Object.keys(body).length ? body : null, errors: {} };
}

/**
 * Where an item of an aisle usually goes at home, as Food decides for an
 * item put away without a place: frozen food the freezer; dairy, meat, and
 * fish the fridge; bakery, pantry, drinks, and snacks the pantry.
 */
export function aisleLocation(aisle: Aisle): PantryLocation {
  switch (aisle) {
    case "frozen":
      return "freezer";
    case "dairy":
    case "meat_fish":
      return "fridge";
    case "bakery":
    case "pantry":
    case "drinks":
    case "snacks":
      return "pantry";
    default:
      return "other";
  }
}

/** One bought item in the put-away dialog: where it goes and when it expires. */
export type PutAwayDraft = {
  id: string;
  name: string;
  amount: string | null;
  location: PantryLocation;
  /** YYYY-MM-DD, or "" for none. */
  expiresOn: string;
};

export function putAwayDrafts(items: ShoppingItem[]): PutAwayDraft[] {
  return items.map((item) => ({
    id: item.id,
    name: item.name,
    amount: item.amount,
    location: aisleLocation(item.aisle),
    expiresOn: "",
  }));
}

/** The put-away request's items: each place, and the expiry date when set. */
export function putAwayItems(drafts: PutAwayDraft[]): PutAwayItem[] {
  return drafts.map((draft) => ({
    id: draft.id,
    location: draft.location,
    ...(draft.expiresOn ? { expiresOn: draft.expiresOn } : {}),
  }));
}

/** The line after a put-away: "Put away “Milch” in the fridge." */
export function putAwayNote(items: PantryItem[]) {
  if (items.length === 1) {
    const [item] = items;
    const place =
      item.location === "other"
        ? ""
        : ` in the ${locationLabel(item.location).toLowerCase()}`;
    return `Put away “${item.name}”${place}.`;
  }
  return `Put away ${items.length} items in the pantry.`;
}

/** The line after marking a pantry item used up or thrown away. */
export function endedNote(item: Pick<PantryItem, "name" | "state">) {
  return item.state === "thrown_away"
    ? `Threw away “${item.name}”.`
    : `Used up “${item.name}”.`;
}

// ---- Staples and running low ---------------------------------------------------

/** The staple with an item's normalised name, if it is one. */
export function stapleOf(staples: Staple[], normalizedName: string) {
  return staples.find((staple) => staple.normalizedName === normalizedName);
}

/** A sentence from Food's restock message: "Added “Milch” to the list: used up (staple)." */
export function restockSentence(restock: Pick<Restock, "message">) {
  return /[.!?]$/.test(restock.message)
    ? restock.message
    : `${restock.message}.`;
}

/**
 * The line after a pantry item ended or was flagged as running low, saying
 * what went on the shopping list by itself (`restock`), and whether to
 * offer to put it there: only when nothing went on the list and no item of
 * its name is needed already.
 */
export function pantryChangeNote(
  result: PantryItemResult,
  action: "used-up" | "thrown-away" | "running-low",
  listed: boolean,
): { text: string; offer: boolean } {
  const first =
    action === "running-low"
      ? `“${result.name}” is running low.`
      : endedNote(result);
  if (result.restock)
    return {
      text: `${first} ${restockSentence(result.restock)}`,
      offer: false,
    };
  if (listed)
    return {
      text: `${first} It is already on the shopping list.`,
      offer: false,
    };
  return { text: first, offer: true };
}

/** The staple's details for its row: "2 l · Aldi · On the list". */
export function stapleDetails(staple: Staple) {
  const state = staple.onList
    ? "On the list"
    : staple.runningLow
      ? "Running low"
      : staple.inStock
        ? "At home"
        : "Not at home";
  return [staple.amount, staple.storeName, state]
    .filter((part): part is string => Boolean(part))
    .join(" · ");
}

/** How many running-low names Home lists before "and n more". */
export const HOME_RUNNING_LOW_PREVIEW = 3;

/**
 * Home's running-low line, one name per normalised name: "Running low:
 * Milch, Brot and 2 more". Null when nothing is running low.
 */
export function runningLowLine(items: PantryItem[]) {
  const seen = new Set<string>();
  const names: string[] = [];
  for (const item of items) {
    if (item.state !== "in_stock" || !item.runningLow) continue;
    if (seen.has(item.normalizedName)) continue;
    seen.add(item.normalizedName);
    names.push(item.name);
  }
  if (!names.length) return null;
  const shown = names.slice(0, HOME_RUNNING_LOW_PREVIEW).join(", ");
  const more = names.length - HOME_RUNNING_LOW_PREVIEW;
  return `Running low: ${shown}${more > 0 ? ` and ${more} more` : ""}`;
}

// ---- Home -------------------------------------------------------------------

/** How many expiring items Home names. */
export const HOME_EXPIRING_PREVIEW = 3;

export type HomeFood = {
  /** Needed items on the shopping list. */
  listCount: number;
  /** More needed items exist than were read. */
  listMore: boolean;
  /** In-stock food that expires today or tomorrow, soonest first. */
  expiring: { id: string; name: string; text: string }[];
  expiringMore: boolean;
  /** Today's planned meals: "Today: Pasta Bolognese (dinner)". */
  meals: string | null;
  /** What is running low: "Running low: Milch, Brot". */
  runningLow: string | null;
};

/**
 * What Home says about food: the shopping list's size, the food that
 * expires today or tomorrow in the owner's timezone, today's planned meals
 * (`meals`, see todayMeals), and what is running low (`low`, the in-stock
 * items flagged so). Food that already expired is the pantry's to show.
 * Null when there is nothing to say.
 */
export function homeFood(
  list: { items: ShoppingItem[]; nextCursor?: string | null },
  pantry: { items: PantryItem[] } | undefined,
  today: string,
  meals: string | null = null,
  low: { items: PantryItem[] } | undefined = undefined,
): HomeFood | null {
  const tomorrow = addDays(today, 1);
  const soon = (pantry?.items ?? [])
    .filter(
      (item) =>
        item.state === "in_stock" &&
        item.expiresOn !== null &&
        item.expiresOn >= today &&
        item.expiresOn <= tomorrow,
    )
    .sort(bySoonestExpiry);
  const listCount = list.items.filter((item) => item.state === "needed").length;
  const runningLow = runningLowLine(low?.items ?? []);
  if (!listCount && !soon.length && !meals && !runningLow) return null;
  return {
    listCount,
    listMore: Boolean(list.nextCursor),
    expiring: soon.slice(0, HOME_EXPIRING_PREVIEW).map((item) => ({
      id: item.id,
      name: item.name,
      text: expiry(item.expiresOn!, today).text,
    })),
    expiringMore: soon.length > HOME_EXPIRING_PREVIEW,
    meals,
    runningLow,
  };
}

// ---- Errors and writes ------------------------------------------------------

/** Plain words for the Food error codes the owner can run into. */
const ERROR_TEXT: Record<string, string> = {
  already_on_list: "Another item on the list already has this name.",
  store_exists: "A store with this name already exists.",
  staple_exists: "This is already a staple.",
  not_in_stock: "This item is no longer in stock. Reload the page.",
  store_archived:
    "This store is archived. Restore it in Stores to use it again.",
  unknown_store: "That store no longer exists. Reload the page.",
  not_found: "This item no longer exists. Reload the page.",
  idempotency_conflict:
    "This change was already sent with other details. Try again.",
  service_unavailable: "Food is unavailable right now.",
};

function sentence(message: string) {
  const text = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

/**
 * What went wrong, from the API's error envelope: plain words for a known
 * code, else the service's message, else `fallback` (a network failure or a
 * body without an envelope).
 */
export function foodErrorMessage(error: unknown, fallback: string) {
  const body =
    typeof error === "object" && error !== null && "error" in error
      ? (error as { error?: { code?: unknown; message?: unknown } }).error
      : undefined;
  const code = typeof body?.code === "string" ? body.code : undefined;
  if (code && ERROR_TEXT[code]) return ERROR_TEXT[code];
  return typeof body?.message === "string" && body.message.trim()
    ? sentence(body.message.trim())
    : fallback;
}

/**
 * A fresh UUID for one user action's Idempotency-Key. crypto.randomUUID
 * needs a secure context and the Dashboard may run on plain http, where
 * getRandomValues still works.
 */
export function newIdempotencyKey(source: Crypto = globalThis.crypto) {
  if (typeof source.randomUUID === "function") return source.randomUUID();
  const bytes = source.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0"));
  return [
    hex.slice(0, 4),
    hex.slice(4, 6),
    hex.slice(6, 8),
    hex.slice(8, 10),
    hex.slice(10),
  ]
    .map((part) => part.join(""))
    .join("-");
}

/** The header params of one write: its own Idempotency-Key. */
export function writeHeader() {
  return { "Idempotency-Key": newIdempotencyKey() };
}

// ---- Loading ----------------------------------------------------------------

/** A request's data, or the message saying why there is none. */
export type Loaded<T> =
  { data: T; error?: undefined } | { data?: undefined; error: string };

/** A request's data, or the error's plain words (`fallback` without any). */
export async function load<T>(
  call: () => Promise<{ data?: T; error?: unknown }>,
  fallback: string,
): Promise<Loaded<T>> {
  try {
    const { data, error } = await call();
    if (error !== undefined || data === undefined) {
      return { error: foodErrorMessage(error, fallback) };
    }
    return { data };
  } catch {
    return { error: fallback };
  }
}

/** Items read per page, and at most this many pages (a safety cap). */
export const FOOD_PAGE_SIZE = 200;
export const FOOD_MAX_PAGES = 5;

/** A whole list read page by page; `capped` when more rows were left. */
export type FoodList<T> = { items: T[]; capped: boolean };

async function readAll<T>(
  page: (cursor: string | undefined) => Promise<{
    data?: { items: T[]; nextCursor?: string | null };
    error?: unknown;
  }>,
): Promise<{ data?: FoodList<T>; error?: unknown }> {
  const items: T[] = [];
  let cursor: string | undefined;
  for (let index = 0; index < FOOD_MAX_PAGES; index++) {
    const result = await page(cursor);
    if (!result.data) return { error: result.error ?? {} };
    items.push(...result.data.items);
    cursor = result.data.nextCursor ?? undefined;
    if (!cursor) return { data: { items, capped: false } };
  }
  return { data: { items, capped: true } };
}

/** The whole shopping list: needed items first, then bought ones. */
export function fetchShoppingList() {
  return load(
    () =>
      readAll((cursor) =>
        foodClient.GET("/v1/shopping-items", {
          params: { query: { limit: FOOD_PAGE_SIZE, cursor } },
        }),
      ),
    "The shopping list could not be loaded.",
  );
}

/** Every store, archived ones too (items keep theirs). */
export function fetchStores() {
  return load(
    () =>
      foodClient
        .GET("/v1/stores", { params: { query: { includeArchived: true } } })
        .then(({ data, error }) => ({ data: data?.items, error })),
    "Stores could not be loaded.",
  );
}

/** The whole pantry: in-stock items first, then the history. */
export function fetchPantry() {
  return load(
    () =>
      readAll((cursor) =>
        foodClient.GET("/v1/pantry-items", {
          params: { query: { limit: FOOD_PAGE_SIZE, cursor } },
        }),
      ),
    "The pantry could not be loaded.",
  );
}

/** Every staple, by name. */
export function fetchStaples() {
  return load(
    () => foodClient.GET("/v1/staples"),
    "Staples could not be loaded.",
  );
}

export type FoodWorkspaceState =
  | {
      kind: "ready";
      shopping: FoodList<ShoppingItem>;
      stores: Loaded<Store[]>;
      pantry: Loaded<FoodList<PantryItem>>;
      staples: Loaded<{ items: Staple[] }>;
    }
  | { kind: "unavailable"; error: string };

/**
 * Everything the workspace shows first. Without the shopping list Food is
 * unavailable; stores, the pantry, or the staples failing say so in their
 * own place.
 */
export async function getFoodWorkspace(): Promise<FoodWorkspaceState> {
  const [shopping, stores, pantry, staples] = await Promise.all([
    fetchShoppingList(),
    fetchStores(),
    fetchPantry(),
    fetchStaples(),
  ]);
  if (shopping.error !== undefined) {
    return { kind: "unavailable", error: shopping.error };
  }
  return { kind: "ready", shopping: shopping.data, stores, pantry, staples };
}
