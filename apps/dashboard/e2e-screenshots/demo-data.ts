import { type APIRequestContext, expect } from "@playwright/test";

/**
 * The demo dataset of the README's screenshots: a made-up person's week in
 * Life OS. Everything here is fictional: the tasks, events, captures, the
 * accounts and their money, the shops and the food. No real person, place,
 * company, or account number may go in here (the screenshots are public);
 * the accounts are manual ones, so no IBAN shows anywhere.
 *
 * Dates are relative to the day the screenshots are taken, in the stack's
 * timezone, so the pages look current whenever they are made again. The
 * writes go through the gateway with the demo account's session, as the
 * owner's own (X-Life-OS-Actor: user): the data is the demo person's, on a
 * throwaway stack, never anyone's live data.
 */

export const ZONE = "Europe/Berlin";

/** Today in `zone`, as YYYY-MM-DD. */
export function today(zone = ZONE) {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: zone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date());
}

/** `day` plus `days` (negative: before), as YYYY-MM-DD. */
export function addDays(day: string, days: number) {
  const date = new Date(`${day}T12:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** The Monday of `day`'s week. */
export function monday(day: string) {
  const weekday = new Date(`${day}T12:00:00Z`).getUTCDay();
  return addDays(day, -((weekday + 6) % 7));
}

/** How far `zone` is ahead of UTC at `instant`, in minutes. */
function offsetMinutes(instant: number, zone: string) {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    })
      .formatToParts(new Date(instant))
      .map((part) => [part.type, part.value]),
  );
  const wall = Date.UTC(
    Number(parts.year),
    Number(parts.month) - 1,
    Number(parts.day),
    Number(parts.hour),
    Number(parts.minute),
    Number(parts.second),
  );
  return Math.round((wall - instant) / 60_000);
}

/** The instant of the wall-clock `time` (HH:MM) on `day` in `zone`. */
export function at(day: string, time: string, zone = ZONE) {
  const [year, month, date] = day.split("-").map(Number);
  const [hour, minute] = time.split(":").map(Number);
  const wall = Date.UTC(year, month - 1, date, hour, minute);
  let instant = wall - offsetMinutes(wall, zone) * 60_000;
  instant = wall - offsetMinutes(instant, zone) * 60_000;
  return new Date(instant).toISOString();
}

type Created = { id: string };

async function post<T = Created>(
  request: APIRequestContext,
  path: string,
  data?: unknown,
): Promise<T> {
  const response = await request.post(
    path,
    data === undefined ? undefined : { data },
  );
  expect(
    response.ok(),
    `POST ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return (await response.json()) as T;
}

async function put(request: APIRequestContext, path: string, data: unknown) {
  const response = await request.put(path, { data });
  expect(
    response.ok(),
    `PUT ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
}

async function patch(request: APIRequestContext, path: string, data: unknown) {
  const response = await request.patch(path, { data });
  expect(
    response.ok(),
    `PATCH ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
}

async function seedTasks(request: APIRequestContext, day: string) {
  const api = "/api/tasks/v1/tasks";
  const task = (title: string, dueAt?: string, extra: object = {}) =>
    post<Created & { title: string }>(request, api, {
      title,
      ...(dueAt ? { dueAt } : {}),
      ...extra,
    });
  // A date alone is due when the day starts (shown without a time).
  const date = (offset: number) => at(addDays(day, offset), "00:00");
  const tasks = {
    passport: await task("Renew passport", date(-2)),
    insurance: await task("Send the insurance form", date(-1)),
    landlord: await task(
      "Call the landlord about the heating",
      at(day, "17:00"),
    ),
    train: await task("Book train tickets to Hamburg", date(0)),
    cleaning: await task("Pick up the dry cleaning", at(day, "12:00")),
    electricity: await task("Pay the electricity bill", date(1)),
    plants: await task("Water the plants", date(2), {
      recurrence: { frequency: "weekly", interval: 1 },
    }),
    library: await task("Return the library books", date(3)),
    phone: await task("Pay the phone bill", date(4)),
    trip: await task("Plan the weekend trip to the lake", date(6)),
  };
  await task("Fix the bike light");
  await task("Sort out old holiday photos");
  await task("Try the new ramen place", undefined, {
    notes: "The one next to the bookshop.",
  });
  for (const title of ["Send the tax documents", "Buy a birthday present"]) {
    const done = await task(title, date(-3));
    await post(request, `${api}/${done.id}/complete`);
  }
  return tasks;
}

async function seedEvents(request: APIRequestContext, day: string) {
  const week = monday(day);
  const event = (
    offset: number,
    start: string,
    end: string,
    title: string,
    extra: object = {},
  ) =>
    post(request, "/api/calendar/v1/events", {
      title,
      allDay: false,
      startAt: at(addDays(week, offset), start),
      endAt: at(addDays(week, offset), end),
      ...extra,
    });
  const weekly = { recurrence: { frequency: "weekly", interval: 1 } };
  await event(0, "09:30", "09:45", "Team stand-up", {
    recurrence: {
      frequency: "weekly",
      interval: 1,
      byWeekday: ["MO", "TU", "WE", "TH", "FR"],
    },
  });
  await event(0, "18:30", "19:30", "Yoga class", {
    ...weekly,
    location: "Lotus Studio",
  });
  await event(1, "10:00", "11:00", "Dentist check-up", {
    location: "City Dental Clinic",
    priority: "high",
  });
  await event(1, "12:30", "13:30", "Lunch with Sam", {
    location: "Hilltop Café",
  });
  await event(2, "14:00", "15:30", "Project review");
  await event(2, "19:00", "21:00", "Football practice", weekly);
  await event(3, "08:00", "08:30", "Car service drop-off", {
    location: "Riverside Garage",
  });
  await event(3, "17:30", "18:00", "Haircut");
  await event(4, "19:30", "22:00", "Cinema with friends");
  await event(5, "09:00", "11:00", "Farmers' market");
  await event(5, "15:00", "18:00", "Mia's birthday party");
  await post(request, "/api/calendar/v1/events", {
    title: "Mum's birthday",
    allDay: true,
    startDate: addDays(week, 6),
    endDate: addDays(week, 6),
    recurrence: { frequency: "yearly", interval: 1 },
  });
  await post(request, "/api/calendar/v1/events", {
    title: "Weekend trip to the lake",
    allDay: true,
    startDate: addDays(week, 12),
    endDate: addDays(week, 13),
  });
}

/** A capture the AI filed: an attempt that ends processed. */
async function processedCapture(
  request: APIRequestContext,
  content: string,
  key: string,
) {
  const item = await post(request, "/api/inbox/v1/inbox-items", {
    content,
    source: "manual",
  });
  const attempt = await post(
    request,
    `/api/inbox/v1/inbox-items/${item.id}/processing-attempts`,
    { idempotencyKey: `demo-${key}`, agentName: "demo", modelProfile: "demo" },
  );
  await patch(
    request,
    `/api/inbox/v1/inbox-items/${item.id}/processing-attempts/${attempt.id}`,
    { status: "processed", actions: [] },
  );
}

async function seedInbox(
  request: APIRequestContext,
  bills: { title: string; id: string }[],
) {
  await processedCapture(request, "Dentist on Tuesday at 10", "dentist");
  await processedCapture(request, "Buy oat milk and bananas", "shopping");
  await processedCapture(
    request,
    "Paid 12.40 for lunch at Hilltop Café",
    "lunch",
  );
  // The AI was not sure which Task was meant, and asks in Human Review.
  const review = await post(request, "/api/inbox/v1/inbox-items", {
    content: "Mark the bill as paid",
    source: "manual",
  });
  const attempt = await post(
    request,
    `/api/inbox/v1/inbox-items/${review.id}/processing-attempts`,
    { idempotencyKey: "demo-review", agentName: "demo", modelProfile: "demo" },
  );
  await post(request, `/api/inbox/v1/inbox-items/${review.id}/human-review`, {
    attemptId: attempt.id,
    proposedAction: { tool: "task_complete", arguments: { taskQuery: "bill" } },
    candidates: bills,
    errorSummary: "Two Tasks match “bill”. Which one did you pay?",
  });
  await post(request, "/api/inbox/v1/inbox-items", {
    content: "Idea: picnic at the lake on Sunday, ask Sam and Alex",
    source: "manual",
  });
}

async function seedFinance(request: APIRequestContext, day: string) {
  const api = "/api/finance/v1";
  const account = (name: string, kind: string, openingBalanceMinor: number) =>
    post(request, `${api}/accounts`, {
      name,
      kind,
      currency: "EUR",
      openingBalanceMinor,
    });
  const everyday = await account("Everyday account", "bank", 184_250);
  const savings = await account("Savings", "savings", 820_000);
  const card = await account("Credit card", "card", 0);
  const accounts = (await (await request.get(`${api}/accounts`)).json()) as {
    items: { id: string; builtIn: boolean }[];
  };
  const cash = accounts.items.find((item) => item.builtIn)!;

  const category = (name: string, kind: string) =>
    post(request, `${api}/categories`, { name, kind });
  const c = {
    salary: await category("Salary", "income"),
    groceries: await category("Groceries", "expense"),
    eatingOut: await category("Eating out", "expense"),
    rent: await category("Rent", "expense"),
    transport: await category("Transport", "expense"),
    utilities: await category("Utilities", "expense"),
    subscriptions: await category("Subscriptions", "expense"),
    fun: await category("Fun", "expense"),
    transfer: await category("Transfers", "transfer"),
  };

  const month = day.slice(0, 7);
  const first = `${month}-01`;
  // Within this month where it can be, so the budget has this month's money.
  const daysAgo = (days: number) => {
    const date = addDays(day, -days);
    return date < first ? first : date;
  };
  type Spend = {
    account: Created;
    euros: number;
    days: number;
    counterparty: string;
    category: Created;
    description?: string;
    status?: "booked" | "pending";
  };
  const spend = ({
    account,
    euros,
    days,
    counterparty,
    category,
    ...rest
  }: Spend) =>
    post(request, `${api}/transactions`, {
      accountId: account.id,
      amount: { amount: Math.round(euros * 100), currency: "EUR" },
      bookingDate: daysAgo(days),
      counterparty,
      categoryId: category.id,
      ...rest,
    });
  const salaryDays = Number(day.slice(8, 10)) - 1;
  await spend({
    account: everyday,
    euros: 3200,
    days: salaryDays,
    counterparty: "Acme Corp",
    category: c.salary,
    description: "Salary",
  });
  await spend({
    account: everyday,
    euros: -1150,
    days: salaryDays,
    counterparty: "Maple Street Apartments",
    category: c.rent,
    description: "Rent",
  });
  await spend({
    account: everyday,
    euros: -300,
    days: salaryDays,
    counterparty: "Savings",
    category: c.transfer,
  });
  await spend({
    account: savings,
    euros: 300,
    days: salaryDays,
    counterparty: "Everyday account",
    category: c.transfer,
  });
  const streaming = await spend({
    account: card,
    euros: -12.99,
    days: 8,
    counterparty: "StreamBox",
    category: c.subscriptions,
  });
  const gym = await spend({
    account: everyday,
    euros: -29.9,
    days: 10,
    counterparty: "Gym Club",
    category: c.subscriptions,
  });
  for (const [euros, days] of [
    [-23.4, 13],
    [-82.1, 9],
    [-38.75, 5],
    [-64.2, 1],
  ]) {
    await spend({
      account: card,
      euros,
      days,
      counterparty: "FreshMart",
      category: c.groceries,
    });
  }
  await spend({
    account: everyday,
    euros: -49,
    days: 15,
    counterparty: "City Transit",
    category: c.transport,
    description: "Monthly pass",
  });
  await spend({
    account: everyday,
    euros: -78,
    days: 12,
    counterparty: "Northwind Energy",
    category: c.utilities,
  });
  await spend({
    account: card,
    euros: -12.4,
    days: 2,
    counterparty: "Hilltop Café",
    category: c.eatingOut,
  });
  await spend({
    account: card,
    euros: -34.5,
    days: 6,
    counterparty: "Pizzeria Napoli",
    category: c.eatingOut,
  });
  await spend({
    account: card,
    euros: -24,
    days: 4,
    counterparty: "Starlight Cinema",
    category: c.fun,
  });
  await spend({
    account: cash,
    euros: -6.8,
    days: 0,
    counterparty: "Sunrise Bakery",
    category: c.groceries,
  });
  await spend({
    account: card,
    euros: -18.5,
    days: 0,
    counterparty: "Corner Bookshop",
    category: c.fun,
    status: "pending",
  });

  for (const paid of [streaming, gym]) {
    await post(request, `${api}/subscriptions`, {
      transactionId: paid.id,
      cadence: "monthly",
    });
  }
  await put(request, `${api}/budgets/${month}`, {
    scope: "month",
    expectedIncomeMinor: 320_000,
    savingsGoalMinor: 30_000,
    lines: [
      { categoryId: c.rent.id, amountMinor: 115_000 },
      { categoryId: c.groceries.id, amountMinor: 40_000 },
      { categoryId: c.eatingOut.id, amountMinor: 12_000 },
      { categoryId: c.transport.id, amountMinor: 6_000 },
      { categoryId: c.utilities.id, amountMinor: 9_000 },
      { categoryId: c.subscriptions.id, amountMinor: 5_000 },
      { categoryId: c.fun.id, amountMinor: 10_000 },
    ],
  });
}

async function seedFood(request: APIRequestContext, day: string) {
  const api = "/api/food/v1";
  const items: [string, string | null, string, string?][] = [
    ["Bananas", "6", "produce"],
    ["Cherry tomatoes", "250 g", "produce"],
    ["Baby spinach", null, "produce"],
    ["Lemons", "2", "produce"],
    ["Sourdough bread", null, "bakery", "Sunrise Bakery"],
    ["Oat milk", "2 l", "dairy"],
    ["Greek yogurt", "500 g", "dairy"],
    ["Chicken thighs", "600 g", "meat_fish"],
    ["Frozen peas", null, "frozen"],
    ["Basmati rice", "1 kg", "pantry"],
    ["Olive oil", null, "pantry"],
    ["Sparkling water", "6", "drinks"],
    ["Dish soap", null, "household"],
    ["Toothpaste", null, "drugstore"],
  ];
  await post(request, `${api}/shopping-items`, {
    items: items.map(([name, amount, aisle, store]) => ({
      name,
      aisle,
      storeName: store ?? "FreshMart",
      ...(amount ? { amount } : {}),
    })),
  });
  const eggs = await post<{ items: { item: Created }[] }>(
    request,
    `${api}/shopping-items`,
    { items: [{ name: "Eggs", amount: "10", aisle: "dairy" }] },
  );
  await post(request, `${api}/shopping-items/${eggs.items[0].item.id}/bought`);

  const pantry: [string, string, number | null, string?][] = [
    ["Milk", "fridge", 2, "1 l"],
    ["Baby spinach", "fridge", 1],
    ["Cheddar", "fridge", 12],
    ["Butter", "fridge", 20],
    ["Frozen berries", "freezer", 120, "500 g"],
    ["Spaghetti", "pantry", 300, "2 packs"],
    ["Chickpeas", "pantry", 400, "3 tins"],
    ["Coffee beans", "pantry", 90],
  ];
  await post(request, `${api}/pantry-items`, {
    items: pantry.map(([name, location, days, amount]) => ({
      name,
      location,
      ...(days === null ? {} : { expiresOn: addDays(day, days) }),
      ...(amount ? { amount } : {}),
    })),
  });

  const recipe = (data: object) => post(request, `${api}/recipes`, data);
  const pasta = await recipe({
    title: "Lemon chickpea pasta",
    servings: 2,
    methods: ["stovetop"],
    tags: ["quick", "vegetarian"],
    ingredients: [
      { name: "Spaghetti", amount: "200 g" },
      { name: "Chickpeas", amount: "1 tin" },
      { name: "Lemons", amount: "1" },
      { name: "Baby spinach", amount: "2 handfuls" },
      { name: "Olive oil" },
    ],
    steps:
      "Cook the pasta. Fry the chickpeas in olive oil until golden, add the spinach, lemon zest and juice, then toss with the pasta.",
  });
  const chicken = await recipe({
    title: "Sheet-pan chicken with vegetables",
    servings: 4,
    methods: ["oven"],
    ingredients: [
      { name: "Chicken thighs", amount: "600 g" },
      { name: "Cherry tomatoes", amount: "250 g" },
      { name: "Olive oil" },
    ],
    steps: "Roast everything at 200 °C for 35 minutes.",
  });
  const oats = await recipe({
    title: "Overnight oats with berries",
    servings: 1,
    methods: ["no_cook"],
    ingredients: [
      { name: "Oats", amount: "50 g" },
      { name: "Oat milk", amount: "150 ml" },
      { name: "Frozen berries", amount: "1 handful" },
    ],
  });
  await put(request, `${api}/meal-plan/${day}/dinner`, { recipeId: pasta.id });
  await put(request, `${api}/meal-plan/${addDays(day, 1)}/dinner`, {
    recipeId: chicken.id,
  });
  await put(request, `${api}/meal-plan/${addDays(day, 1)}/breakfast`, {
    recipeId: oats.id,
  });
}

/** Fills an empty Life OS with the demo dataset. */
export async function seed(request: APIRequestContext) {
  const day = today();
  const tasks = await seedTasks(request, day);
  await seedEvents(request, day);
  await seedFinance(request, day);
  await seedFood(request, day);
  // Last, so that the captures are the newest activity.
  await seedInbox(request, [
    { id: tasks.electricity.id, title: tasks.electricity.title },
    { id: tasks.phone.id, title: tasks.phone.title },
  ]);
}
