import { pickDate } from "./date-picker";
import {
  type APIRequestContext,
  expect,
  type Locator,
  type Page,
  test,
} from "./fixtures";

// The Food workspace (P15-02): the shopping list's quick add with amounts
// and merges, store tags and the store filter, ticking items off and back
// on (also merging into an item added meanwhile), clearing bought items,
// the pantry's add, used up, history, restock, and add to list, Home's
// Food line, and the list on a phone. Every name carries the test's tag
// (e2e<timestamp>, never a trailing number, which quick add would read as
// an amount), and everything tagged is removed afterwards; stores, which
// Food never deletes, are archived.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

const api = "/api/food/v1";

type ShoppingItem = {
  id: string;
  name: string;
  amount: string | null;
  aisle: string;
  state: "needed" | "bought";
  storeId: string | null;
  storeName: string | null;
};
type PantryItem = {
  id: string;
  name: string;
  amount: string | null;
  location: string;
  expiresOn: string | null;
  state: "in_stock" | "used_up" | "thrown_away";
  fromShoppingItemId: string | null;
};
type Store = { id: string; name: string; archived: boolean };
type Recipe = { id: string; title: string };
type MealPlanEntry = {
  date: string;
  slot: string;
  title: string;
  recipeId: string | null;
};

/** Every item of a Food list, following its cursor. */
async function all<T>(
  request: APIRequestContext,
  list: "shopping-items" | "pantry-items",
  query = "",
): Promise<T[]> {
  const items: T[] = [];
  let cursor = "";
  for (let page = 0; page < 20; page++) {
    const response = await request.get(
      `${api}/${list}?limit=200${query}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
    );
    expect(response.ok()).toBeTruthy();
    const body = (await response.json()) as {
      items: T[];
      nextCursor?: string | null;
    };
    items.push(...body.items);
    if (!body.nextCursor) break;
    cursor = body.nextCursor;
  }
  return items;
}

async function shoppingNamed(request: APIRequestContext, name: string) {
  return (await all<ShoppingItem>(request, "shopping-items")).filter(
    (item) => item.name === name,
  );
}

async function pantryNamed(request: APIRequestContext, name: string) {
  return (await all<PantryItem>(request, "pantry-items")).filter(
    (item) => item.name === name,
  );
}

async function addShopping(
  request: APIRequestContext,
  item: { name: string; amount?: string },
): Promise<ShoppingItem> {
  const response = await request.post(`${api}/shopping-items`, {
    data: { items: [item] },
  });
  expect(response.ok()).toBeTruthy();
  return (await response.json()).items[0].item;
}

async function markBought(request: APIRequestContext, id: string) {
  const response = await request.post(`${api}/shopping-items/${id}/bought`);
  expect(response.ok()).toBeTruthy();
}

async function addPantry(
  request: APIRequestContext,
  item: { name: string; expiresOn?: string; location?: string },
): Promise<PantryItem> {
  const response = await request.post(`${api}/pantry-items`, {
    data: { items: [item] },
  });
  expect(response.ok()).toBeTruthy();
  return (await response.json()).items[0];
}

/** The days `days` before and after the Berlin today (the plan's range). */
function aroundToday(days: number) {
  const day = (offset: number) =>
    new Intl.DateTimeFormat("en-CA", { timeZone: "Europe/Berlin" }).format(
      new Date(Date.now() + offset * 86_400_000),
    );
  return { from: day(-days), to: day(days) };
}

async function mealPlan(request: APIRequestContext, from: string, to: string) {
  const response = await request.get(`${api}/meal-plan?from=${from}&to=${to}`);
  expect(response.ok()).toBeTruthy();
  return (await response.json()).items as MealPlanEntry[];
}

/**
 * Removes everything the test made: items, recipes, and planned meals
 * deleted, stores archived.
 */
async function cleanUp(request: APIRequestContext, tag: string) {
  const { from, to } = aroundToday(15);
  for (const entry of await mealPlan(request, from, to)) {
    if (entry.title.includes(tag))
      await request.delete(`${api}/meal-plan/${entry.date}/${entry.slot}`);
  }
  const recipes = (
    await (
      await request.get(`${api}/recipes?q=${encodeURIComponent(tag)}`)
    ).json()
  ).items as Recipe[];
  for (const recipe of recipes) {
    await request.delete(`${api}/recipes/${recipe.id}`);
  }
  for (const item of await all<ShoppingItem>(request, "shopping-items")) {
    if (item.name.includes(tag))
      await request.delete(`${api}/shopping-items/${item.id}`);
  }
  for (const item of await all<PantryItem>(request, "pantry-items")) {
    if (item.name.includes(tag))
      await request.delete(`${api}/pantry-items/${item.id}`);
  }
  const stores = (await (await request.get(`${api}/stores`)).json())
    .items as Store[];
  for (const store of stores) {
    if (store.name.includes(tag))
      await request.patch(`${api}/stores/${store.id}`, {
        data: { archived: true },
      });
  }
}

/**
 * Opens /food (or a tab) and reads the owner's today, once the page has
 * settled in the browser's timezone and hydrated (it is busy until then).
 */
async function openFood(page: Page, path = "/food") {
  await page.goto(path);
  const workspace = page.getByTestId("food-workspace");
  await expect(workspace).toHaveAttribute(
    "data-time-zone",
    await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone),
  );
  await expect(workspace).toHaveAttribute("aria-busy", "false");
  return { today: (await workspace.getAttribute("data-today"))! };
}

/** The day `days` after a YYYY-MM-DD day. */
function shift(day: string, days: number) {
  const date = new Date(`${day}T12:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

function quickAdd(page: Page) {
  return page.getByRole("textbox", { name: "Add to the shopping list" });
}

async function add(page: Page, text: string) {
  await quickAdd(page).fill(text);
  await quickAdd(page).press("Enter");
}

/** A needed item's row on the list. */
function row(page: Page, name: string): Locator {
  return page.getByTestId("food-item").filter({ hasText: name });
}

function note(page: Page) {
  return page.getByTestId("food-quick-add-note");
}

/**
 * Unfolds a folded section unless it already is; the browser applies its
 * remembered state right after hydration.
 */
async function expand(toggle: Locator) {
  await expect(async () => {
    if ((await toggle.getAttribute("aria-expanded")) !== "true")
      await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true", {
      timeout: 1_000,
    });
  }).toPass();
}

function boughtToggle(page: Page) {
  return page
    .getByTestId("food-bought")
    .getByRole("button", { name: /^Bought/ });
}

async function expectNoHorizontalOverflow(page: Page) {
  const { scrollWidth, innerWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  expect(scrollWidth).toBeLessThanOrEqual(innerWidth);
}

test.describe("Food workspace", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  let tag = "";
  test.beforeEach(() => {
    tag = `e2e${Date.now()}`;
  });
  test.afterEach(async ({ request }) => {
    await cleanUp(request, tag);
  });

  test("quick add splits the amount off, and a duplicate merges", async ({
    page,
    request,
  }) => {
    const name = `Milch ${tag}`;
    await openFood(page);
    await expect(
      page.getByRole("heading", { name: "Food", level: 1 }),
    ).toBeVisible();

    await add(page, `2 l ${name}`);
    await expect(note(page)).toHaveText(`Added “${name}” to Dairy.`);
    // The field is empty and ready for the next item.
    await expect(quickAdd(page)).toHaveValue("");
    await expect(quickAdd(page)).toBeFocused();
    // "Milch" is dairy by the built-in aisle rules.
    const dairy = page.getByTestId("food-aisle-dairy");
    await expect(
      dairy.getByTestId("food-item").filter({ hasText: name }),
    ).toContainText("2 l");
    await expect
      .poll(async () =>
        (await shoppingNamed(request, name)).map((item) => item.amount),
      )
      .toEqual(["2 l"]);

    // The same name again, with the amount after it: one item, updated.
    await add(page, `${name.toUpperCase()} 3 l`);
    await expect(note(page)).toHaveText(
      `“${name}” was already on the list — updated.`,
    );
    await expect(row(page, name)).toHaveCount(1);
    await expect(row(page, name)).toContainText("3 l");
    const items = await shoppingNamed(request, name);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      amount: "3 l",
      aisle: "dairy",
      state: "needed",
    });

    // Both survive a reload.
    await page.reload();
    await expect(row(page, name)).toContainText("3 l");
  });

  test("items are tagged with a store and the list filters by store", async ({
    page,
    request,
  }) => {
    const lidl = `E2E Lidl ${tag}`;
    const rewe = `E2E Rewe ${tag}`;
    const bread = `Brot ${tag}`;
    const soap = `Seife ${tag}`;
    await openFood(page);

    // A store, added in the Stores dialog.
    await page.getByRole("button", { name: "Stores", exact: true }).click();
    const storesDialog = page.getByRole("dialog", { name: "Stores" });
    await storesDialog.getByRole("textbox", { name: "New store" }).fill(lidl);
    await storesDialog.getByRole("button", { name: "Add store" }).click();
    await expect(storesDialog.getByTestId("food-stores")).toContainText(lidl);
    await page.keyboard.press("Escape");
    await expect(storesDialog).toHaveCount(0);

    // Quick add tags the item with the store picked beside it.
    const picker = page.getByRole("combobox", { name: "Store for new items" });
    await picker.selectOption({ label: lidl });
    await add(page, bread);
    await expect(row(page, bread)).toContainText(lidl);
    await picker.selectOption({ label: "No store" });
    await add(page, soap);
    await expect(row(page, soap)).toBeVisible();
    const [stored] = await shoppingNamed(request, bread);
    expect(stored.storeName).toBe(lidl);

    // The filter: one store, items without one, everything.
    const filter = page.getByRole("group", { name: "Store" });
    await filter.getByRole("button", { name: lidl, exact: true }).click();
    await expect(row(page, bread)).toBeVisible();
    await expect(row(page, soap)).toHaveCount(0);
    // The filter says which store, so the rows don't repeat it.
    await expect(row(page, bread).getByText(lidl)).toHaveCount(0);
    await expect(page).toHaveURL(new RegExp(`store=${stored.storeId}`));
    // Filtering by a store adds to it.
    await expect(picker).toHaveValue(stored.storeId!);

    await filter.getByRole("button", { name: "No store", exact: true }).click();
    await expect(row(page, soap)).toBeVisible();
    await expect(row(page, bread)).toHaveCount(0);

    await filter.getByRole("button", { name: "All", exact: true }).click();
    await expect(row(page, bread)).toBeVisible();
    await expect(row(page, soap)).toBeVisible();

    // Editing an item gives it a new store, typed by name.
    await row(page, soap).getByRole("button").click();
    const edit = page.getByRole("dialog", { name: "Edit item" });
    await edit.getByLabel("Store", { exact: true }).selectOption({
      label: "New store…",
    });
    await edit.getByLabel("New store", { exact: true }).fill(rewe);
    await edit.getByLabel("Amount", { exact: true }).fill("2 Stück");
    await edit.getByRole("button", { name: "Save" }).click();
    await expect(edit).toHaveCount(0);
    await expect(row(page, soap)).toContainText(rewe);
    await expect(row(page, soap)).toContainText("2 Stück");
    const [edited] = await shoppingNamed(request, soap);
    expect(edited).toMatchObject({ storeName: rewe, amount: "2 Stück" });
    // The new store joins the filter.
    await expect(
      filter.getByRole("button", { name: rewe, exact: true }),
    ).toBeVisible();
  });

  test("ticking items off and back on, merging, and clearing bought", async ({
    page,
    request,
  }) => {
    const apples = `Äpfel ${tag}`;
    const butter = `Butter ${tag}`;
    await addShopping(request, { name: apples });
    await addShopping(request, { name: butter, amount: "250 g" });
    await openFood(page);

    // Ticked off: it leaves the list at once and Food has it bought.
    await page.getByRole("checkbox", { name: `${apples} bought` }).click();
    await expect(row(page, apples)).toHaveCount(0);
    await expect
      .poll(async () =>
        (await shoppingNamed(request, apples)).map((item) => item.state),
      )
      .toEqual(["bought"]);
    // Bought is folded, with a count.
    const bought = page.getByTestId("food-bought");
    await expect(boughtToggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(boughtToggle(page)).toContainText(/Bought\s*\d+/);

    // A tap in Bought puts it back.
    await expand(boughtToggle(page));
    await bought.getByRole("checkbox", { name: `${apples} bought` }).click();
    await expect(row(page, apples)).toBeVisible();
    await expect
      .poll(async () =>
        (await shoppingNamed(request, apples)).map((item) => item.state),
      )
      .toEqual(["needed"]);

    // Bought, then added again meanwhile: putting it back merges the two.
    await page.getByRole("checkbox", { name: `${butter} bought` }).click();
    await expect
      .poll(async () =>
        (await shoppingNamed(request, butter)).map((item) => item.state),
      )
      .toEqual(["bought"]);
    const again = await addShopping(request, { name: butter });
    await openFood(page);
    await expect(row(page, butter)).toHaveCount(1);
    await expand(boughtToggle(page));
    await bought.getByRole("checkbox", { name: `${butter} bought` }).click();
    await expect(
      bought.getByTestId("food-bought-item").filter({ hasText: butter }),
    ).toHaveCount(0);
    await expect(row(page, butter)).toHaveCount(1);
    // The needed item stays and takes the bought one's amount.
    await expect(row(page, butter)).toContainText("250 g");
    await expect
      .poll(async () =>
        (await shoppingNamed(request, butter)).map((item) => [
          item.id,
          item.state,
          item.amount,
        ]),
      )
      .toEqual([[again.id, "needed", "250 g"]]);

    // Clear bought asks first, then deletes the bought items.
    await page.getByRole("checkbox", { name: `${apples} bought` }).click();
    await expect(
      bought.getByTestId("food-bought-item").filter({ hasText: apples }),
    ).toBeVisible();
    await bought.getByRole("button", { name: "Clear bought" }).click();
    await expect(bought).toContainText(/Delete the \d+ bought items? from/);
    await bought.getByRole("button", { name: "Clear", exact: true }).click();
    await expect(bought).toHaveCount(0);
    await expect
      .poll(
        async () =>
          (await all<ShoppingItem>(request, "shopping-items", "&state=bought"))
            .length,
      )
      .toBe(0);
    expect(await shoppingNamed(request, apples)).toHaveLength(0);
  });

  test("the pantry adds with an expiry, ends items into history, and adds to the list", async ({
    page,
    request,
  }) => {
    const yoghurt = `Joghurt ${tag}`;
    const { today } = await openFood(page, "/food?tab=pantry");
    await expect(page.getByRole("tab", { name: "Pantry" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    await page.getByRole("button", { name: "Add item" }).click();
    const dialog = page.getByRole("dialog", { name: "Add to the pantry" });
    await dialog.getByLabel("Name", { exact: true }).fill(yoghurt);
    await dialog.getByLabel("Amount", { exact: true }).fill("500 g");
    await pickDate(dialog, "Expires on", shift(today, 1));
    await dialog.getByRole("button", { name: "Add item" }).click();
    await expect(dialog).toHaveCount(0);

    // No place given: Food puts dairy in the fridge.
    const fridge = page.getByTestId("food-location-fridge");
    const item = fridge
      .getByTestId("food-pantry-item")
      .filter({ hasText: yoghurt });
    await expect(item).toContainText("500 g");
    await expect(item).toContainText("Expires tomorrow");
    const [stored] = await pantryNamed(request, yoghurt);
    expect(stored).toMatchObject({
      location: "fridge",
      expiresOn: shift(today, 1),
      state: "in_stock",
    });

    // Add to list puts an item of the same name on the shopping list.
    await item.getByRole("button", { name: `Add to list: ${yoghurt}` }).click();
    await expect(page.getByTestId("food-pantry-note")).toHaveText(
      `Added “${yoghurt}” to the shopping list.`,
    );
    await expect
      .poll(async () =>
        (await shoppingNamed(request, yoghurt)).map((entry) => entry.state),
      )
      .toEqual(["needed"]);
    await page.getByRole("tab", { name: "Shopping list" }).click();
    await expect(row(page, yoghurt)).toBeVisible();
    await expect(page).toHaveURL(/\/food$/);
    await page.getByRole("tab", { name: "Pantry" }).click();
    await expect(page).toHaveURL(/\/food\?tab=pantry$/);

    // Used up: it leaves the pantry for the folded history.
    await item.getByRole("button", { name: `Used up: ${yoghurt}` }).click();
    await expect(item).toHaveCount(0);
    await expect
      .poll(async () =>
        (await pantryNamed(request, yoghurt)).map((entry) => entry.state),
      )
      .toEqual(["used_up"]);
    const history = page.getByTestId("food-history");
    await expand(history.getByRole("button", { name: /^History/ }));
    const ended = history
      .getByTestId("food-history-item")
      .filter({ hasText: yoghurt });
    await expect(ended).toContainText("Used up today");

    // Restocked, it is back in the fridge.
    await ended.getByRole("button", { name: `Restock: ${yoghurt}` }).click();
    await expect(item).toContainText("Expires tomorrow");
    await expect
      .poll(async () =>
        (await pantryNamed(request, yoghurt)).map((entry) => entry.state),
      )
      .toEqual(["in_stock"]);
  });

  test("Home counts the shopping list and names food expiring today", async ({
    page,
    request,
  }) => {
    const milk = `Milch ${tag}`;
    await addShopping(request, { name: `Brot ${tag}` });
    const { today } = await openFood(page);
    await addPantry(request, { name: milk, expiresOn: today });
    const count = (
      await all<ShoppingItem>(request, "shopping-items", "&state=needed")
    ).length;

    await page.goto("/");
    const card = page.getByRole("region", { name: "Food", exact: true });
    await expect(card).toContainText(
      count > 200
        ? "200+ items on the shopping list"
        : `${count} item${count === 1 ? "" : "s"} on the shopping list`,
    );
    await expect(
      card.getByRole("link", { name: /Open shopping list/ }),
    ).toHaveAttribute("href", "/food");
    const expiring = card.getByRole("link", { name: milk });
    await expect(expiring).toHaveAttribute("href", "/food?tab=pantry");
    await expect(
      card.getByRole("listitem").filter({ hasText: milk }),
    ).toContainText("Expires today");
    await expiring.click();
    await expect(page).toHaveURL(/\/food\?tab=pantry$/);
    await expect(
      page.getByTestId("food-pantry-item").filter({ hasText: milk }),
    ).toContainText("Expires today");
  });

  test("bought items are put away into the pantry with a place and an expiry", async ({
    page,
    request,
  }) => {
    const milk = `Milch ${tag}`;
    const peas = `Erbsen ${tag}`;
    const bought = [
      await addShopping(request, { name: milk, amount: "1 l" }),
      await addShopping(request, { name: peas }),
    ];
    for (const item of bought) await markBought(request, item.id);
    const { today } = await openFood(page);
    await expand(boughtToggle(page));

    // One item: it starts in its aisle's usual place, the fridge for dairy.
    await page.getByRole("button", { name: `Put away ${milk}` }).click();
    const dialog = page.getByRole("dialog", { name: "Put away" });
    const entry = dialog
      .getByTestId("food-put-away-item")
      .filter({ hasText: milk });
    await expect(entry).toContainText("1 l");
    await expect(entry.getByLabel("Place")).toHaveValue("fridge");
    await pickDate(entry, "Expires on", shift(today, 3));
    await dialog.getByRole("button", { name: "Put away", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByTestId("food-list-note")).toHaveText(
      `Put away “${milk}” in the fridge.`,
    );
    await expect(
      page.getByTestId("food-bought-item").filter({ hasText: milk }),
    ).toHaveCount(0);
    expect(await shoppingNamed(request, milk)).toHaveLength(0);
    const [stored] = await pantryNamed(request, milk);
    expect(stored).toMatchObject({
      location: "fridge",
      amount: "1 l",
      expiresOn: shift(today, 3),
      state: "in_stock",
      fromShoppingItemId: bought[0].id,
    });

    // The rest at once, the place changed.
    await page
      .getByTestId("food-bought")
      .getByRole("button", { name: "Put away all" })
      .click();
    const rest = page.getByRole("dialog", { name: "Put away" });
    const peasEntry = rest
      .getByTestId("food-put-away-item")
      .filter({ hasText: peas });
    await expect(rest.getByTestId("food-put-away-item")).toHaveCount(
      await page.getByTestId("food-bought-item").count(),
    );
    await peasEntry.getByLabel("Place").selectOption("freezer");
    await rest.getByRole("button", { name: "Put away", exact: true }).click();
    await expect(rest).toHaveCount(0);
    await expect(page.getByTestId("food-bought")).toHaveCount(0);
    expect(await shoppingNamed(request, peas)).toHaveLength(0);

    // Both are in the pantry now.
    await page.getByRole("tab", { name: "Pantry" }).click();
    await expect(
      page
        .getByTestId("food-location-fridge")
        .getByTestId("food-pantry-item")
        .filter({ hasText: milk }),
    ).toContainText("Expires in 3 days");
    await expect(
      page
        .getByTestId("food-location-freezer")
        .getByTestId("food-pantry-item")
        .filter({ hasText: peas }),
    ).toBeVisible();
  });

  test("used up or thrown away offers to put the item back on the list", async ({
    page,
    request,
  }) => {
    const butter = `Butter ${tag}`;
    const cheese = `Käse ${tag}`;
    await addPantry(request, { name: butter });
    await addPantry(request, { name: cheese });
    await addShopping(request, { name: cheese });
    await openFood(page, "/food?tab=pantry");
    const note = page.getByTestId("food-pantry-note");
    const offer = page.getByTestId("food-pantry-offer");

    await page.getByRole("button", { name: `Used up: ${butter}` }).click();
    await expect(note).toHaveText(`Used up “${butter}”.`);
    await expect(offer).toContainText(`Add “${butter}” to the shopping list?`);
    await offer.getByRole("button", { name: "Add to list" }).click();
    await expect(note).toHaveText(`Added “${butter}” to the shopping list.`);
    await expect(offer).toHaveCount(0);
    await expect
      .poll(async () =>
        (await shoppingNamed(request, butter)).map((item) => item.state),
      )
      .toEqual(["needed"]);

    // Already on the list: nothing to offer.
    await page.getByRole("button", { name: `Thrown away: ${cheese}` }).click();
    await expect(note).toHaveText(
      `Threw away “${cheese}”. It is already on the shopping list.`,
    );
    await expect(offer).toHaveCount(0);
    expect(await shoppingNamed(request, cheese)).toHaveLength(1);
  });

  test("a recipe is planned for a day and its missing ingredients go on the list", async ({
    page,
    request,
  }) => {
    const title = `Lasagne ${tag}`;
    const sheets = `Nudelplatten ${tag}`;
    const tomatoes = `Tomaten ${tag}`;
    const cheese = `Mozzarella ${tag}`;
    // Tomatoes are at home; mozzarella is already on the list.
    await addPantry(request, { name: tomatoes });
    await addShopping(request, { name: cheese });
    const { today } = await openFood(page, "/food?tab=recipes");

    // A recipe with three ingredients, two steps, and a tag.
    await page.getByRole("button", { name: "New recipe" }).click();
    const form = page.getByRole("dialog", { name: "New recipe" });
    await form.getByLabel("Title", { exact: true }).fill(title);
    await form.getByLabel("Servings", { exact: true }).fill("4");
    await form.getByLabel("Ingredient 1", { exact: true }).fill(sheets);
    await form.getByLabel("Amount of ingredient 1").fill("250 g");
    await form.getByRole("button", { name: "Add ingredient" }).click();
    await form.getByLabel("Ingredient 2", { exact: true }).fill(tomatoes);
    await form.getByLabel("Amount of ingredient 2").fill("1 Dose");
    await form.getByRole("button", { name: "Add ingredient" }).click();
    await form.getByLabel("Ingredient 3", { exact: true }).fill(cheese);
    await form
      .getByLabel("Steps", { exact: true })
      .fill("1. Schichten.\n2. Backen.");
    await form.getByLabel("Tags", { exact: true }).fill(tag);
    await form.getByRole("button", { name: "Add recipe" }).click();
    await expect(form).toHaveCount(0);

    // It opens with its ingredients and numbered steps.
    const view = page.getByRole("dialog", { name: title });
    await expect(view).toContainText("4 servings · 3 ingredients");
    await expect(
      view.getByRole("region", { name: "Ingredients" }).getByRole("listitem"),
    ).toHaveCount(3);
    await expect(
      view.getByRole("region", { name: "Steps" }).getByRole("listitem"),
    ).toHaveText(["Schichten.", "Backen."]);
    await page.keyboard.press("Escape");
    await expect(view).toHaveCount(0);
    await expect(
      page.getByTestId("food-recipe").filter({ hasText: title }),
    ).toBeVisible();

    // Planned for today's dinner.
    await page.getByRole("tab", { name: "Meal plan" }).click();
    await expect(page).toHaveURL(/\/food\?tab=plan$/);
    const day = page.locator(
      `[data-testid="food-plan-day"][data-day="${today}"]`,
    );
    await day.locator('[data-meal="dinner"]').click();
    const slot = page.getByRole("dialog", { name: /^Dinner, / });
    await slot.getByLabel("Recipe", { exact: true }).fill(tag);
    await slot.getByRole("button", { name: title }).click();
    await expect(slot).toHaveCount(0);
    await expect(day.locator('[data-meal="dinner"]')).toContainText(title);
    const [planned] = (await mealPlan(request, today, today)).filter(
      (entry) => entry.slot === "dinner",
    );
    expect(planned).toMatchObject({ title });
    expect(planned.recipeId).toBeTruthy();

    // Home names today's meal.
    await page.goto("/");
    await expect(
      page.getByRole("region", { name: "Food", exact: true }),
    ).toContainText(`${title} (dinner)`);

    // The week's missing ingredients: a preview first.
    await openFood(page, "/food?tab=plan");
    await page
      .getByRole("button", { name: "Add missing ingredients for this week" })
      .click();
    const missing = page.getByRole("dialog", {
      name: "Add missing ingredients",
    });
    const line = (name: string) =>
      missing.getByTestId("food-missing-item").filter({ hasText: name });
    await expect(line(sheets)).toHaveAttribute("data-status", "added");
    await expect(line(sheets)).toContainText("250 g");
    await expect(line(sheets)).toContainText("Will add");
    await expect(line(tomatoes)).toHaveAttribute("data-status", "in_pantry");
    await expect(line(tomatoes)).toContainText("At home");
    await expect(line(cheese)).toHaveAttribute("data-status", "on_list");
    await expect(line(cheese)).toContainText("On the list");
    // Nothing changed yet.
    expect(await shoppingNamed(request, sheets)).toHaveLength(0);
    await missing
      .getByRole("button", { name: /^Add \d+ to the list$/ })
      .click();
    await expect(missing).toHaveCount(0);
    await expect(page.getByTestId("food-plan-note")).toHaveText(
      /^Added \d+ items? to the shopping list\.$/,
    );
    const [added] = await shoppingNamed(request, sheets);
    expect(added).toMatchObject({ amount: "250 g", state: "needed" });
    expect(await shoppingNamed(request, tomatoes)).toHaveLength(0);
    expect(await shoppingNamed(request, cheese)).toHaveLength(1);

    // Cleared again.
    await day.locator('[data-meal="dinner"]').click();
    const again = page.getByRole("dialog", { name: /^Dinner, / });
    await again.getByRole("button", { name: "Clear dinner" }).click();
    await expect(again).toHaveCount(0);
    await expect(day.locator('[data-meal="dinner"]')).toContainText("Add");
    expect(
      (await mealPlan(request, today, today)).filter(
        (entry) => entry.slot === "dinner",
      ),
    ).toHaveLength(0);
  });

  test("the list is usable on a phone", async ({ page, request }) => {
    const tomatoes = `Tomaten ${tag}`;
    await page.setViewportSize({ width: 390, height: 844 });
    await addShopping(request, { name: tomatoes, amount: "500 g" });
    await openFood(page);

    await expect(
      page
        .getByRole("navigation", { name: "Mobile navigation" })
        .getByRole("link", { name: "Food" }),
    ).toHaveAttribute("aria-current", "page");
    await expect(quickAdd(page)).toBeVisible();
    await expectNoHorizontalOverflow(page);

    // The checkbox's tap target is at least 44 × 44 px.
    const target = row(page, tomatoes).locator("label");
    const box = await target.boundingBox();
    expect(box?.width).toBeGreaterThanOrEqual(44);
    expect(box?.height).toBeGreaterThanOrEqual(44);
    // Tapping anywhere in it ticks the item off.
    await target.click({ position: { x: 4, y: 4 } });
    await expect(row(page, tomatoes)).toHaveCount(0);
    await expect
      .poll(async () =>
        (await shoppingNamed(request, tomatoes)).map((item) => item.state),
      )
      .toEqual(["bought"]);
    await expectNoHorizontalOverflow(page);
  });
});
