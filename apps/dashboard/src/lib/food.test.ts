import assert from "node:assert/strict";
import test from "node:test";

import {
  NEW_STORE,
  type PantryItem,
  type ShoppingItem,
  type Store,
  activeStores,
  addedNote,
  aisleGroups,
  boughtItems,
  endedItems,
  endedText,
  expiry,
  foodErrorMessage,
  foodHref,
  homeFood,
  matchesStore,
  newIdempotencyKey,
  newPantryItem,
  pantryDraft,
  pantryGroups,
  pantryItemPatch,
  parseFoodTab,
  parseQuickAdd,
  parseStoreFilter,
  reopened,
  shoppingDraft,
  shoppingItemPatch,
  storeMoves,
  upsert,
} from "./food";

const STAMP = "2026-10-01T00:00:00Z";
const actor = { kind: "user" as const };

function shopping(overrides: Partial<ShoppingItem> = {}): ShoppingItem {
  const name = overrides.name ?? "Milk";
  return {
    id: name.toLowerCase(),
    name,
    normalizedName: name.toLowerCase(),
    amount: null,
    note: null,
    aisle: "other",
    aisleSetBy: "rule",
    storeId: null,
    storeName: null,
    state: "needed",
    boughtAt: null,
    source: "manual",
    sourceRef: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    createdBy: actor,
    updatedBy: actor,
    ...overrides,
  };
}

function pantry(overrides: Partial<PantryItem> = {}): PantryItem {
  const name = overrides.name ?? "Milk";
  return {
    id: name.toLowerCase(),
    name,
    normalizedName: name.toLowerCase(),
    amount: null,
    location: "fridge",
    expiresOn: null,
    openedOn: null,
    note: null,
    state: "in_stock",
    endedAt: null,
    source: "manual",
    sourceRef: null,
    fromShoppingItemId: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    createdBy: actor,
    updatedBy: actor,
    ...overrides,
  };
}

test("quick add splits a leading or trailing amount from the name", () => {
  assert.deepEqual(parseQuickAdd("2 l milk"), { name: "milk", amount: "2 l" });
  assert.deepEqual(parseQuickAdd("milk 2 l"), { name: "milk", amount: "2 l" });
  assert.deepEqual(parseQuickAdd("6 eggs"), { name: "eggs", amount: "6" });
  assert.deepEqual(parseQuickAdd("500 g Hackfleisch"), {
    name: "Hackfleisch",
    amount: "500 g",
  });
  assert.deepEqual(parseQuickAdd("Milch 1,5 l"), {
    name: "Milch",
    amount: "1,5 l",
  });
  assert.deepEqual(parseQuickAdd("500g Hackfleisch"), {
    name: "Hackfleisch",
    amount: "500g",
  });
  assert.deepEqual(parseQuickAdd("Cola Zero 1.5l"), {
    name: "Cola Zero",
    amount: "1.5l",
  });
  assert.deepEqual(parseQuickAdd("2 Packungen Nudeln"), {
    name: "Nudeln",
    amount: "2 Packungen",
  });
  assert.deepEqual(parseQuickAdd("Eier 10 Stück"), {
    name: "Eier",
    amount: "10 Stück",
  });
  assert.deepEqual(parseQuickAdd("1 кг картошки"), {
    name: "картошки",
    amount: "1 кг",
  });
  assert.deepEqual(parseQuickAdd("молоко 2 л"), {
    name: "молоко",
    amount: "2 л",
  });
  assert.deepEqual(parseQuickAdd("2x Joghurt"), {
    name: "Joghurt",
    amount: "2x",
  });
  assert.deepEqual(parseQuickAdd("1/2 Gurke"), {
    name: "Gurke",
    amount: "1/2",
  });
  assert.deepEqual(parseQuickAdd("  2   l   milk "), {
    name: "milk",
    amount: "2 l",
  });
});

test("quick add keeps names that only look like amounts", () => {
  assert.deepEqual(parseQuickAdd("Milch"), { name: "Milch" });
  assert.deepEqual(parseQuickAdd("7up"), { name: "7up" });
  assert.deepEqual(parseQuickAdd("Vitamin D3"), { name: "Vitamin D3" });
  assert.deepEqual(parseQuickAdd("Milch 3,5%"), { name: "Milch 3,5%" });
  assert.deepEqual(parseQuickAdd("10er Eier"), { name: "10er Eier" });
  // A unit is only a unit as a word of its own.
  assert.deepEqual(parseQuickAdd("2 lemons"), { name: "lemons", amount: "2" });
  assert.deepEqual(parseQuickAdd("2 gurken"), { name: "gurken", amount: "2" });
  // A line that is only an amount stays the name.
  assert.deepEqual(parseQuickAdd("2 l"), { name: "2 l" });
  assert.deepEqual(parseQuickAdd("500"), { name: "500" });
  assert.deepEqual(parseQuickAdd(""), { name: "" });
});

test("the tab and the store filter come from the URL", () => {
  assert.equal(parseFoodTab("pantry"), "pantry");
  assert.equal(parseFoodTab("recipes"), "list");
  assert.equal(parseFoodTab(undefined), "list");
  assert.equal(foodHref("pantry"), "/food?tab=pantry");
  assert.equal(foodHref("list"), "/food");
  assert.equal(foodHref("list", "none"), "/food?store=none");
  const stores = [
    { id: "lidl", archived: false },
    { id: "old", archived: true },
  ];
  assert.equal(parseStoreFilter("lidl", stores), "lidl");
  assert.equal(parseStoreFilter("none", stores), "none");
  assert.equal(parseStoreFilter("old", stores), "all");
  assert.equal(parseStoreFilter("gone", stores), "all");
  assert.equal(parseStoreFilter(["lidl"], stores), "all");
});

test("needed items group by aisle in the service's order, empty aisles hidden", () => {
  const items = [
    shopping({ name: "Soap", aisle: "drugstore" }),
    shopping({ name: "Milk", aisle: "dairy", storeId: "lidl" }),
    shopping({ name: "Apples", aisle: "produce" }),
    shopping({ name: "Butter", aisle: "dairy" }),
    shopping({ name: "Bread", aisle: "bakery", state: "bought" }),
  ];
  const groups = aisleGroups(items, "all");
  assert.deepEqual(
    groups.map((group) => group.label),
    ["Produce", "Dairy", "Drugstore"],
  );
  assert.deepEqual(
    groups[1].items.map((item) => item.name),
    ["Butter", "Milk"],
  );
  assert.deepEqual(
    aisleGroups(items, "lidl").map((group) => group.label),
    ["Dairy"],
  );
  assert.deepEqual(
    aisleGroups(items, "none")
      .flatMap((group) => group.items)
      .map((item) => item.name),
    ["Apples", "Butter", "Soap"],
  );
  assert.equal(matchesStore({ storeId: "lidl" }, "all"), true);
  assert.equal(matchesStore({ storeId: null }, "lidl"), false);
});

test("bought items are listed most recently bought first", () => {
  const items = [
    shopping({
      name: "Old",
      state: "bought",
      boughtAt: "2026-10-09T10:00:00Z",
    }),
    shopping({
      name: "New",
      state: "bought",
      boughtAt: "2026-10-10T10:00:00Z",
    }),
    shopping({ name: "Needed" }),
  ];
  assert.deepEqual(
    boughtItems(items).map((item) => item.name),
    ["New", "Old"],
  );
});

test("re-opening a bought item handles the merge into a needed one", () => {
  const bought = shopping({ id: "b", name: "Milk", state: "bought" });
  const needed = shopping({ id: "n", name: "Milk", amount: "2 l" });
  // Plain re-open: the same item comes back needed.
  assert.deepEqual(
    reopened([bought], "b", { ...bought, state: "needed" }).map((item) => [
      item.id,
      item.state,
    ]),
    [["b", "needed"]],
  );
  // Merged: the bought item is gone and the needed one is updated.
  assert.deepEqual(
    reopened([needed, bought], "b", { ...needed, note: "merged" }).map(
      (item) => [item.id, item.note],
    ),
    [["n", "merged"]],
  );
  assert.deepEqual(
    upsert([needed], shopping({ id: "x", name: "Eggs" })).map(
      (item) => item.id,
    ),
    ["n", "x"],
  );
});

test("the quick add's note says whether the add merged", () => {
  assert.equal(
    addedNote({ item: shopping({ aisle: "dairy" }), merged: false }),
    "Added “Milk” to Dairy.",
  );
  assert.equal(
    addedNote({ item: shopping(), merged: true }),
    "“Milk” was already on the list — updated.",
  );
});

test("expiry dates read relative to the owner's today", () => {
  const today = "2026-10-11";
  assert.deepEqual(expiry("2026-10-09", today), {
    text: "Expired 2 days ago",
    tone: "destructive",
    days: -2,
  });
  assert.equal(expiry("2026-10-10", today).text, "Expired yesterday");
  assert.deepEqual(expiry("2026-10-11", today), {
    text: "Expires today",
    tone: "warning",
    days: 0,
  });
  assert.equal(expiry("2026-10-12", today).text, "Expires tomorrow");
  assert.equal(expiry("2026-10-12", today).tone, "warning");
  assert.equal(expiry("2026-10-14", today).text, "Expires in 3 days");
  assert.equal(expiry("2026-10-14", today).tone, "muted");
  assert.equal(expiry("2026-10-17", today).text, "Expires in 6 days");
  assert.equal(expiry("2026-10-24", today).text, "Expires 24 Oct");
  assert.equal(expiry("2027-01-05", today).text, "Expires 5 Jan 2027");
  // Across a month and a daylight-saving change, days stay whole days.
  assert.equal(expiry("2026-10-26", "2026-10-24").text, "Expires in 2 days");
});

test("ended items say how and when they ended, in the owner's timezone", () => {
  const today = "2026-10-11";
  // 23:30 UTC on the 10th is already the 11th in Berlin.
  const late = pantry({ state: "used_up", endedAt: "2026-10-10T23:30:00Z" });
  assert.equal(endedText(late, today, "Europe/Berlin"), "Used up today");
  assert.equal(endedText(late, today, "UTC"), "Used up yesterday");
  assert.equal(
    endedText(
      pantry({ state: "thrown_away", endedAt: "2026-10-02T09:00:00Z" }),
      today,
      "Europe/Berlin",
    ),
    "Thrown away 2 Oct",
  );
});

test("the pantry groups by location, soonest expiry first, no date last", () => {
  const items = [
    pantry({ name: "Peas", location: "freezer" }),
    pantry({ name: "Cheese", expiresOn: "2026-10-20" }),
    pantry({ name: "Ham" }),
    pantry({ name: "Milk", expiresOn: "2026-10-12" }),
    pantry({ name: "Yoghurt", state: "used_up", endedAt: STAMP }),
  ];
  const groups = pantryGroups(items);
  assert.deepEqual(
    groups.map((group) => group.label),
    ["Fridge", "Freezer"],
  );
  assert.deepEqual(
    groups[0].items.map((item) => item.name),
    ["Milk", "Cheese", "Ham"],
  );
  assert.deepEqual(
    endedItems([
      pantry({ name: "A", state: "used_up", endedAt: "2026-10-01T00:00:00Z" }),
      pantry({
        name: "B",
        state: "thrown_away",
        endedAt: "2026-10-05T00:00:00Z",
      }),
      pantry({ name: "C" }),
    ]).map((item) => item.name),
    ["B", "A"],
  );
});

test("Home names the list's size and food expiring today or tomorrow", () => {
  const today = "2026-10-11";
  const summary = homeFood(
    { items: [shopping(), shopping({ name: "Eggs" })], nextCursor: "more" },
    {
      items: [
        pantry({ name: "Old", expiresOn: "2026-10-10" }),
        pantry({ name: "Cheese", expiresOn: "2026-10-12" }),
        pantry({ name: "Milk", expiresOn: "2026-10-11" }),
        pantry({ name: "Later", expiresOn: "2026-10-13" }),
      ],
    },
    today,
  );
  assert.deepEqual(summary, {
    listCount: 2,
    listMore: true,
    expiring: [
      { id: "milk", name: "Milk", text: "Expires today" },
      { id: "cheese", name: "Cheese", text: "Expires tomorrow" },
    ],
    expiringMore: false,
  });
  // Nothing to say, nothing shown.
  assert.equal(homeFood({ items: [] }, { items: [] }, today), null);
  assert.equal(homeFood({ items: [] }, undefined, today), null);
  assert.equal(
    homeFood({ items: [] }, { items: [pantry({ expiresOn: today })] }, today)
      ?.listCount,
    0,
  );
});

test("errors read in plain words", () => {
  assert.equal(
    foodErrorMessage(
      { error: { code: "already_on_list", message: "x" } },
      "Fallback.",
    ),
    "Another item on the list already has this name.",
  );
  assert.equal(
    foodErrorMessage(
      { error: { code: "invalid_request", message: "name is required" } },
      "Fallback.",
    ),
    "Name is required.",
  );
  assert.equal(foodErrorMessage(undefined, "Fallback."), "Fallback.");
});

test("every write gets a fresh UUID, also without a secure context", () => {
  const uuid =
    /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
  assert.match(newIdempotencyKey(), uuid);
  assert.notEqual(newIdempotencyKey(), newIdempotencyKey());
  const insecure = {
    getRandomValues: (bytes: Uint8Array) =>
      globalThis.crypto.getRandomValues(bytes),
  } as unknown as Crypto;
  assert.match(newIdempotencyKey(insecure), uuid);
});

test("the item dialog sends only what changed, a new store by name", () => {
  const item = shopping({
    name: "Milk",
    amount: "1 l",
    aisle: "dairy",
    storeId: "lidl",
    storeName: "Lidl",
  });
  const draft = shoppingDraft(item);
  assert.deepEqual(shoppingItemPatch(item, draft), { body: null, errors: {} });
  assert.deepEqual(
    shoppingItemPatch(item, {
      ...draft,
      name: " Oat milk ",
      amount: " ",
      note: "the blue one",
      aisle: "drinks",
      store: "",
    }).body,
    {
      name: "Oat milk",
      amount: null,
      note: "the blue one",
      aisle: "drinks",
      storeId: null,
    },
  );
  assert.deepEqual(
    shoppingItemPatch(item, { ...draft, store: NEW_STORE, newStore: " Aldi " })
      .body,
    { storeName: "Aldi" },
  );
  assert.deepEqual(
    shoppingItemPatch(item, { ...draft, name: " ", store: NEW_STORE }).errors,
    { name: "Give the item a name.", newStore: "Name the store." },
  );
});

test("stores reorder by swapping and renumbering only what moved", () => {
  const store = (id: string, sortOrder: number, archived = false): Store => ({
    id,
    name: id,
    sortOrder,
    archived,
    archivedAt: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    createdBy: actor,
    updatedBy: actor,
  });
  const stores = activeStores([
    store("rewe", 20),
    store("lidl", 10),
    store("old", 0, true),
    store("aldi", 30),
  ]);
  assert.deepEqual(
    stores.map((entry) => entry.id),
    ["lidl", "rewe", "aldi"],
  );
  assert.deepEqual(storeMoves(stores, 2, -1), [
    { id: "aldi", sortOrder: 20 },
    { id: "rewe", sortOrder: 30 },
  ]);
  assert.deepEqual(storeMoves(stores, 0, -1), []);
  // Equal sort orders (stores added at once) get numbered too.
  const tied = activeStores([store("b", 0), store("a", 0)]);
  assert.deepEqual(storeMoves(tied, 0, 1), [
    { id: "b", sortOrder: 10 },
    { id: "a", sortOrder: 20 },
  ]);
});

test("the pantry dialog adds what was filled in and patches what changed", () => {
  assert.deepEqual(newPantryItem({ ...pantryDraft(), name: " " }), {
    body: null,
    errors: { name: "Give the item a name." },
  });
  // No place: Food picks it from the name.
  assert.deepEqual(
    newPantryItem({
      ...pantryDraft(),
      name: "Joghurt",
      amount: "500 g",
      expiresOn: "2026-10-14",
    }).body,
    { name: "Joghurt", amount: "500 g", expiresOn: "2026-10-14" },
  );
  const item = pantry({
    name: "Milk",
    location: "fridge",
    expiresOn: "2026-10-14",
  });
  const draft = pantryDraft(item);
  assert.equal(pantryItemPatch(item, draft).body, null);
  assert.deepEqual(
    pantryItemPatch(item, {
      ...draft,
      location: "freezer",
      expiresOn: "",
      openedOn: "2026-10-11",
    }).body,
    { location: "freezer", expiresOn: null, openedOn: "2026-10-11" },
  );
});
