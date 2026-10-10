import { deleteSource, failingFeedUrl } from "./calendar-feed";
import { datePicker, pickDate } from "./date-picker";
import { type APIRequestContext, expect, type Page, test } from "./fixtures";

// The Calendar workspace (/calendar): FullCalendar month, week, day, and list
// views over the Calendar API, event create/inspect/edit/delete dialogs,
// default plus own reminders, priority, and Reminder defaults, search across
// months, cancelled events, phone width, and the dark theme. The browser runs in
// Europe/Berlin (playwright.config.ts).

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const ZONE = "Europe/Berlin";

const hydrationProblem =
  /hydrat|did not match|Minified React error #4(18|19|21|23|25)/i;

type Reminder = {
  kind: "relative" | "localTime";
  minutesBefore?: number;
  daysBefore?: number;
  localTime?: string;
};

type CalendarEvent = {
  id: string;
  title: string;
  allDay: boolean;
  startAt?: string | null;
  startDate?: string | null;
  endDate?: string | null;
  status: string;
  reminders: Reminder[];
  useDefaultReminders: boolean;
  effectiveReminders: Reminder[];
  priority: "low" | "medium" | "high";
};

type ReminderDefaults = { timed: Reminder[]; allDay: Reminder[] };

/** Today in the browser's zone, as YYYY-MM-DD. */
function today() {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: ZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date());
}

function addDays(day: string, days: number) {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** "Wed 21 Oct" (and " 2027" outside this year), as the workspace labels days. */
function dayLabel(day: string) {
  const label = new Intl.DateTimeFormat("en-GB", {
    timeZone: "UTC",
    weekday: "short",
    day: "numeric",
    month: "short",
  }).format(new Date(`${day}T00:00:00Z`));
  return day.slice(0, 4) === today().slice(0, 4)
    ? label
    : `${label} ${day.slice(0, 4)}`;
}

/** The instant of a Berlin wall-clock time (UTC+1 or +2), via the browser. */
async function berlinInstant(page: Page, day: string, time: string) {
  return page.evaluate(
    ([d, t]) => new Date(`${d}T${t}:00`).toISOString(),
    [day, time],
  );
}

function watchForProblems(page: Page) {
  const problems: string[] = [];
  page.on("pageerror", (error) => problems.push(`pageerror: ${error.message}`));
  page.on("console", (message) => {
    if (message.type() === "error" && hydrationProblem.test(message.text())) {
      problems.push(`console: ${message.text()}`);
    }
  });
  return problems;
}

const workspace = (page: Page) => page.locator("#calendar");
const events = (page: Page) =>
  page.getByRole("region", { name: "Calendar events" });
const eventIn = (page: Page, title: string) =>
  events(page).locator(".fc-event", { hasText: title });

/** Opens a view at a date and waits until its events have loaded. */
async function open(page: Page, view: string, date: string) {
  await page.goto(`/calendar?view=${view}&date=${date}`);
  await expect(workspace(page)).toHaveAttribute("data-view", view);
  await expect(events(page)).toHaveAttribute("aria-busy", "false");
}

async function findByTitle(request: APIRequestContext, title: string) {
  const response = await request.get(
    `/api/calendar/v1/events/search?q=${encodeURIComponent(title)}&includeCancelled=true`,
  );
  expect(response.ok()).toBeTruthy();
  const { items } = (await response.json()) as { items: CalendarEvent[] };
  return items.filter((event) => event.title === title);
}

/** The owner's default reminders (Calendar settings). */
async function ownerDefaults(request: APIRequestContext) {
  const response = await request.get("/api/calendar/v1/settings");
  expect(response.ok()).toBeTruthy();
  return ((await response.json()) as { defaultReminders: ReminderDefaults })
    .defaultReminders;
}

async function setOwnerDefaults(
  request: APIRequestContext,
  defaultReminders: ReminderDefaults,
) {
  const response = await request.patch("/api/calendar/v1/settings", {
    data: { defaultReminders },
  });
  expect(response.ok(), await response.text()).toBeTruthy();
}

async function horizontalOverflow(page: Page) {
  return page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
}

test.describe("Calendar workspace", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  const stamp = Date.now();
  const word = `Zephyr${stamp}`;

  test.afterAll(async ({ request }) => {
    // Remove whatever a failed test left behind.
    for (const query of [`${stamp}`, word]) {
      const response = await request.get(
        `/api/calendar/v1/events/search?q=${query}&includeCancelled=true&limit=100`,
      );
      if (!response.ok()) continue;
      const { items } = (await response.json()) as { items: CalendarEvent[] };
      for (const event of items) {
        await request.delete(`/api/calendar/v1/events/${event.id}`);
      }
    }
  });

  test("creates, inspects, edits, moves, and deletes a timed event in every view", async ({
    page,
    request,
  }) => {
    const problems = watchForProblems(page);
    const title = `Workspace dentist ${stamp}`;
    const moved = `${title} moved`;
    const day = addDays(today(), 7);
    const next = addDays(day, 1);

    await open(page, "week", day);
    await page.getByRole("button", { name: "New event" }).click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("heading", { name: "New event" }),
    ).toBeVisible();
    await dialog.getByLabel("Title").fill(title);
    await expect(
      dialog.getByRole("checkbox", { name: "All day" }),
    ).not.toBeChecked();
    await pickDate(dialog, "Start date", day);
    await dialog.getByLabel("Start time").fill("10:00");
    await pickDate(dialog, "End date", day);
    await dialog.getByLabel("End time").fill("11:30");
    await dialog.getByLabel("Location").fill("Praxis Dr. Weber");
    await dialog.getByLabel("Notes").fill("Bring the insurance card");
    await dialog.getByRole("button", { name: "Add reminder" }).click();
    await dialog.getByLabel("Reminder 1 amount").fill("45");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toBeHidden();
    await expect(eventIn(page, title)).toBeVisible();

    const [stored] = await findByTitle(request, title);
    expect(Date.parse(stored.startAt ?? "")).toBe(
      Date.parse(await berlinInstant(page, day, "10:00")),
    );

    // The URL keeps the view and date, so every view survives a reload.
    for (const view of ["Month", "Day", "List", "Week"]) {
      await page
        .getByRole("group", { name: "View" })
        .getByRole("button", { name: view })
        .click();
      await expect(workspace(page)).toHaveAttribute(
        "data-view",
        view.toLowerCase(),
      );
      await expect(page).toHaveURL(
        new RegExp(`view=${view.toLowerCase()}&date=${day}`),
      );
      await page.reload();
      await expect(events(page)).toHaveAttribute("aria-busy", "false");
      await expect(eventIn(page, title)).toBeVisible();
    }

    // Inspect.
    await eventIn(page, title).click();
    await expect(dialog.getByRole("heading", { name: title })).toBeVisible();
    await expect(dialog.getByTestId("event-when")).toHaveText(
      `${dayLabel(day)} · 10:00–11:30`,
    );
    await expect(dialog).toContainText(`Times in ${ZONE}`);
    await expect(dialog).toContainText("Praxis Dr. Weber");
    await expect(dialog).toContainText("Bring the insurance card");
    await expect(dialog).toContainText("45 minutes before");

    // Edit: a new title and the next day; the end moves with the start.
    await dialog.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(
      dialog.getByRole("heading", { name: "Edit event" }),
    ).toBeVisible();
    await dialog.getByLabel("Title").fill(moved);
    await pickDate(dialog, "Start date", next);
    await expect(datePicker(dialog, "End date")).toHaveAttribute(
      "data-value",
      next,
    );
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog.getByRole("heading", { name: moved })).toBeVisible();
    await expect(dialog.getByTestId("event-when")).toHaveText(
      `${dayLabel(next)} · 10:00–11:30`,
    );
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(eventIn(page, moved)).toBeVisible();

    await open(page, "list", day);
    const nextDay = events(page).locator(`.fc-list-day[data-date="${next}"]`);
    await expect(nextDay).toBeVisible();
    await expect(
      events(page).locator(`.fc-list-day[data-date="${day}"]`),
    ).toHaveCount(0);
    await open(page, "day", next);
    await expect(eventIn(page, moved)).toBeVisible();
    const [edited] = await findByTitle(request, moved);
    expect(Date.parse(edited.startAt ?? "")).toBe(
      Date.parse(await berlinInstant(page, next, "10:00")),
    );

    // Delete.
    await eventIn(page, moved).click();
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await dialog.getByRole("button", { name: "Delete event" }).click();
    await expect(dialog).toBeHidden();
    await expect(eventIn(page, moved)).toHaveCount(0);
    await page.reload();
    await expect(events(page)).toHaveAttribute("aria-busy", "false");
    await expect(eventIn(page, moved)).toHaveCount(0);
    expect(
      (await request.get(`/api/calendar/v1/events/${edited.id}`)).status(),
    ).toBe(404);
    expect(problems).toEqual([]);
  });

  test("creates and edits an all-day event with an inclusive last day", async ({
    page,
    request,
  }) => {
    const problems = watchForProblems(page);
    const title = `Workspace trip ${stamp}`;
    const first = addDays(today(), 10);
    const last = addDays(first, 1);

    await open(page, "month", first);
    await page.getByRole("button", { name: "New event" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Title").fill(title);
    await dialog.getByRole("checkbox", { name: "All day" }).check();
    await expect(dialog.getByLabel("Start time")).toHaveCount(0);
    await pickDate(dialog, "Start date", first);
    await pickDate(dialog, "End date", last);
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toBeHidden();
    await expect(eventIn(page, title).first()).toBeVisible();

    const [stored] = await findByTitle(request, title);
    expect(stored).toMatchObject({
      allDay: true,
      startDate: first,
      endDate: last,
    });

    // Both days carry it in the list; the last day is included, not cut off.
    await open(page, "list", first);
    await expect(eventIn(page, title)).toHaveCount(2);
    await eventIn(page, title).first().click();
    await expect(dialog.getByTestId("event-when")).toHaveText(
      `${dayLabel(first)} – ${dayLabel(last)} · All day`,
    );

    await dialog.getByRole("button", { name: "Edit", exact: true }).click();
    await pickDate(dialog, "End date", first);
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog.getByTestId("event-when")).toHaveText(
      `${dayLabel(first)} · All day`,
    );
    await page.keyboard.press("Escape");

    await page.reload();
    await expect(events(page)).toHaveAttribute("aria-busy", "false");
    await expect(eventIn(page, title)).toHaveCount(1);
    for (const view of ["month", "week", "day"]) {
      await open(page, view, first);
      await expect(eventIn(page, title)).toBeVisible();
    }
    const [single] = await findByTitle(request, title);
    expect(single).toMatchObject({ startDate: first, endDate: first });

    await eventIn(page, title).click();
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await dialog.getByRole("button", { name: "Delete event" }).click();
    await expect(dialog).toBeHidden();
    await expect(eventIn(page, title)).toHaveCount(0);
    expect(problems).toEqual([]);
  });

  test("shows the Calendar API's validation message at the field", async ({
    page,
  }) => {
    const day = addDays(today(), 3);
    await open(page, "week", day);
    await page.getByRole("button", { name: "New event" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog.getByText("Add a title.")).toBeVisible();

    await dialog.getByLabel("Title").fill(`Workspace invalid ${stamp}`);
    await pickDate(dialog, "Start date", day);
    await dialog.getByLabel("Start time").fill("10:00");
    await pickDate(dialog, "End date", day);
    await dialog.getByLabel("End time").fill("09:00");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(
      dialog.getByText("The end must be after the start."),
    ).toBeVisible();
    await expect(dialog.getByLabel("End time")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
  });

  test("an event gets several reminders of its own on top of the defaults", async ({
    page,
    request,
  }) => {
    const problems = watchForProblems(page);
    const title = `Workspace reminders ${stamp}`;
    const day = addDays(today(), 4);
    const original = await ownerDefaults(request);
    try {
      await setOwnerDefaults(request, {
        timed: [{ kind: "relative", minutesBefore: 10 }],
        allDay: [],
      });

      await open(page, "week", day);
      await page.getByRole("button", { name: "New event" }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel("Title").fill(title);
      await pickDate(dialog, "Start date", day);
      await dialog.getByLabel("Start time").fill("14:00");
      await pickDate(dialog, "End date", day);
      await dialog.getByLabel("End time").fill("15:00");
      const reminders = dialog.getByRole("group", {
        name: "Reminders",
        exact: true,
      });
      await expect(
        reminders.getByRole("checkbox", { name: "Use default reminders" }),
      ).toBeChecked();
      await expect(dialog.getByTestId("default-reminders")).toHaveText(
        "10 minutes before (your default for timed events)",
      );

      // Like Google Calendar: 1 day and 3 hours before, as well as the default.
      const add = reminders.getByRole("button", { name: "Add reminder" });
      await add.click();
      await dialog.getByLabel("Reminder 1 amount").fill("1");
      await dialog.getByLabel("Reminder 1 unit").selectOption("days");
      await add.click();
      await dialog.getByLabel("Reminder 2 amount").fill("3");
      await dialog.getByLabel("Reminder 2 unit").selectOption("hours");
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog).toBeHidden();

      const [stored] = await findByTitle(request, title);
      expect(stored.useDefaultReminders).toBe(true);
      expect(stored.reminders).toEqual([
        { kind: "relative", minutesBefore: 1440 },
        { kind: "relative", minutesBefore: 180 },
      ]);
      expect(stored.effectiveReminders).toHaveLength(3);

      // The details list every reminder that applies, the default marked.
      await eventIn(page, title).click();
      const listed = dialog
        .getByTestId("event-reminders")
        .getByRole("listitem");
      await expect(listed).toHaveText([
        "10 minutes before · default",
        "1 day before",
        "3 hours before",
      ]);

      // Without the defaults, and one reminder fewer.
      await dialog.getByRole("button", { name: "Edit", exact: true }).click();
      await dialog
        .getByRole("checkbox", { name: "Use default reminders" })
        .uncheck();
      await dialog.getByRole("button", { name: "Remove reminder 2" }).click();
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(listed).toHaveText(["1 day before"]);

      // Back to the defaults alone.
      await dialog.getByRole("button", { name: "Edit", exact: true }).click();
      await dialog
        .getByRole("checkbox", { name: "Use default reminders" })
        .check();
      await dialog.getByRole("button", { name: "Remove reminder 1" }).click();
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(listed).toHaveText(["10 minutes before · default"]);
      const [edited] = await findByTitle(request, title);
      expect(edited).toMatchObject({
        useDefaultReminders: true,
        reminders: [],
      });

      // Neither: no reminders at all.
      await dialog.getByRole("button", { name: "Edit", exact: true }).click();
      await dialog
        .getByRole("checkbox", { name: "Use default reminders" })
        .uncheck();
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(listed).toHaveText(["No reminders"]);
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();
      expect(problems).toEqual([]);
    } finally {
      await setOwnerDefaults(request, original);
      for (const event of await findByTitle(request, title)) {
        await request.delete(`/api/calendar/v1/events/${event.id}`);
      }
    }
  });

  test("a high-priority event shows its priority in its details", async ({
    page,
    request,
  }) => {
    const problems = watchForProblems(page);
    const title = `Workspace visa ${stamp}`;
    const day = addDays(today(), 5);
    try {
      await open(page, "week", day);
      await page.getByRole("button", { name: "New event" }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel("Title").fill(title);
      await pickDate(dialog, "Start date", day);
      await dialog.getByLabel("Start time").fill("09:00");
      await pickDate(dialog, "End date", day);
      await dialog.getByLabel("End time").fill("10:00");
      const priority = dialog.getByLabel("Priority");
      await expect(priority).toHaveValue("medium");
      await priority.selectOption("high");
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog).toBeHidden();

      const [stored] = await findByTitle(request, title);
      expect(stored.priority).toBe("high");
      // The calendar marks it.
      await expect(eventIn(page, title)).toHaveClass(/lifeos-event-high/);

      await eventIn(page, title).click();
      await expect(dialog.getByTestId("event-priority")).toHaveText(
        "High priority",
      );

      // Low is named too; medium, the usual, is not.
      await dialog.getByRole("button", { name: "Edit", exact: true }).click();
      await expect(dialog.getByLabel("Priority")).toHaveValue("high");
      await dialog.getByLabel("Priority").selectOption("low");
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog.getByTestId("event-priority")).toHaveText(
        "Low priority",
      );
      await dialog.getByRole("button", { name: "Edit", exact: true }).click();
      await dialog.getByLabel("Priority").selectOption("medium");
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog.getByRole("heading", { name: title })).toBeVisible();
      await expect(dialog.getByTestId("event-priority")).toHaveCount(0);
      const [edited] = await findByTitle(request, title);
      expect(edited.priority).toBe("medium");
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();
      await expect(eventIn(page, title)).not.toHaveClass(/lifeos-event-high/);
      expect(problems).toEqual([]);
    } finally {
      for (const event of await findByTitle(request, title)) {
        await request.delete(`/api/calendar/v1/events/${event.id}`);
      }
    }
  });

  test("Reminder defaults sets the owner's and a source's defaults", async ({
    page,
    request,
  }) => {
    const problems = watchForProblems(page);
    const title = `Workspace defaults ${stamp}`;
    const feedName = `Workspace feed ${stamp}`;
    const day = addDays(today(), 6);
    const original = await ownerDefaults(request);
    // Disabled, so it is never refreshed and Status stays quiet about it.
    const created = await request.post("/api/calendar/v1/sources", {
      data: { name: feedName, url: failingFeedUrl(stamp), enabled: false },
    });
    expect(created.status(), await created.text()).toBe(201);
    const source = (await created.json()) as { id: string };
    try {
      await setOwnerDefaults(request, { timed: [], allDay: [] });

      await open(page, "month", day);
      await page.getByRole("button", { name: "Reminder defaults" }).click();
      const dialog = page.getByRole("dialog");
      await expect(
        dialog.getByRole("heading", { name: "Reminder defaults" }),
      ).toBeVisible();
      const timed = dialog.getByRole("group", { name: "Your timed events" });
      const allDay = dialog.getByRole("group", { name: "Your all-day events" });
      const feed = dialog.getByRole("group", { name: feedName });
      await expect(allDay).toContainText("No default reminders.");
      await expect(feed).toContainText("No default reminders.");

      await timed.getByRole("button", { name: "Add reminder" }).click();
      await dialog.getByLabel("Timed default 1 amount").fill("2");
      await dialog.getByLabel("Timed default 1 unit").selectOption("hours");
      // An all-day default starts as the evening before.
      await allDay.getByRole("button", { name: "Add reminder" }).click();
      await expect(
        dialog.getByLabel("All-day default 1 days before"),
      ).toHaveValue("1");
      await dialog.getByLabel("All-day default 1 time").fill("18:30");
      await feed.getByRole("button", { name: "Add reminder" }).click();
      await dialog
        .getByLabel(`${feedName} default 1 type`)
        .selectOption("localTime");
      // The feed's events are important unless one gets another priority.
      await expect(feed.getByLabel("Default priority")).toHaveValue("medium");
      await feed.getByLabel("Default priority").selectOption("high");
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog).toBeHidden();

      expect(await ownerDefaults(request)).toEqual({
        timed: [{ kind: "relative", minutesBefore: 120 }],
        allDay: [{ kind: "localTime", daysBefore: 1, localTime: "18:30" }],
      });
      const saved = await request.get(`/api/calendar/v1/sources/${source.id}`);
      expect(
        (await saved.json()) as {
          defaultReminders: Reminder[] | null;
          defaultPriority: string;
        },
      ).toMatchObject({
        defaultReminders: [
          { kind: "localTime", daysBefore: 1, localTime: "19:00" },
        ],
        defaultPriority: "high",
      });

      // A new event offers the defaults for its kind and uses them.
      await page.getByRole("button", { name: "New event" }).click();
      await dialog.getByLabel("Title").fill(title);
      await expect(dialog.getByTestId("default-reminders")).toHaveText(
        "2 hours before (your default for timed events)",
      );
      await dialog.getByRole("checkbox", { name: "All day" }).check();
      await expect(dialog.getByTestId("default-reminders")).toHaveText(
        "1 day before at 18:30 (your default for all-day events)",
      );
      await pickDate(dialog, "Start date", day);
      await pickDate(dialog, "End date", day);
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog).toBeHidden();
      await eventIn(page, title).click();
      await expect(
        dialog.getByTestId("event-reminders").getByRole("listitem"),
      ).toHaveText(["1 day before at 18:30 · default"]);
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();

      // The dialog reopens with what was saved; Cancel changes nothing.
      await page.getByRole("button", { name: "Reminder defaults" }).click();
      await expect(dialog.getByLabel("All-day default 1 time")).toHaveValue(
        "18:30",
      );
      await dialog
        .getByRole("button", { name: "Remove all-day default 1" })
        .click();
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toBeHidden();
      expect((await ownerDefaults(request)).allDay).toHaveLength(1);
      expect(problems).toEqual([]);
    } finally {
      await setOwnerDefaults(request, original);
      await deleteSource(request, source.id);
      for (const event of await findByTitle(request, title)) {
        await request.delete(`/api/calendar/v1/events/${event.id}`);
      }
    }
  });

  test("search finds an event months away and opens it on its date", async ({
    page,
    request,
  }) => {
    const problems = watchForProblems(page);
    const far = addDays(today(), 95);
    const farTitle = `${word} dentist checkup`;
    const past = addDays(today(), -40);
    const pastTitle = `${word} old visit`;
    for (const data of [
      {
        title: farTitle,
        allDay: false,
        startAt: await berlinInstant(page, far, "15:00"),
        endAt: await berlinInstant(page, far, "16:00"),
        location: "Praxis",
      },
      { title: pastTitle, allDay: true, startDate: past, endDate: past },
    ]) {
      const created = await request.post("/api/calendar/v1/events", { data });
      expect(created.status()).toBe(201);
    }

    await open(page, "month", today());
    const search = page.getByRole("combobox", { name: "Search events" });
    await search.fill(`nothing-matches-${stamp}`);
    const results = page.getByTestId("calendar-search-results");
    await expect(results).toContainText("No events match");

    await search.fill(word);
    await expect(results).toContainText("Upcoming");
    await expect(results).toContainText("Past");
    await expect(
      results.getByRole("option", { name: new RegExp(farTitle) }),
    ).toContainText(`${dayLabel(far)} · 15:00–16:00`);
    await expect(
      results.getByRole("option", { name: new RegExp(pastTitle) }),
    ).toContainText(`${dayLabel(past)} · All day`);

    // Keyboard: the first result is the upcoming one.
    await search.press("ArrowDown");
    await search.press("Enter");
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: farTitle })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    const month = new Intl.DateTimeFormat("en-GB", {
      timeZone: "UTC",
      month: "long",
      year: "numeric",
    }).format(new Date(`${far}T00:00:00Z`));
    await expect(page.getByTestId("calendar-title")).toHaveText(month);
    await expect(eventIn(page, farTitle)).toBeVisible();

    // The past event, chosen with the pointer, in the list view.
    await page
      .getByRole("group", { name: "View" })
      .getByRole("button", { name: "List" })
      .click();
    await search.fill("");
    await search.fill(word);
    await results.getByRole("option", { name: new RegExp(pastTitle) }).click();
    await expect(
      dialog.getByRole("heading", { name: pastTitle }),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(
      events(page).locator(`.fc-list-day[data-date="${past}"]`),
    ).toBeVisible();
    await expect(eventIn(page, pastTitle)).toBeVisible();
    expect(problems).toEqual([]);
  });

  test("cancelled events stay hidden until asked for and can be restored", async ({
    page,
    request,
  }) => {
    const day = addDays(today(), 5);
    const title = `Workspace cancelled ${stamp}`;
    const created = await request.post("/api/calendar/v1/events", {
      data: {
        title,
        allDay: false,
        startAt: await berlinInstant(page, day, "12:00"),
        endAt: await berlinInstant(page, day, "13:00"),
      },
    });
    expect(created.status()).toBe(201);
    const { id } = (await created.json()) as CalendarEvent;
    const cancelled = await request.patch(`/api/calendar/v1/events/${id}`, {
      data: { status: "cancelled" },
    });
    expect(cancelled.ok()).toBeTruthy();

    await open(page, "list", day);
    await expect(eventIn(page, title)).toHaveCount(0);
    await page.getByRole("checkbox", { name: "Show cancelled" }).check();
    const shown = eventIn(page, title);
    await expect(shown).toBeVisible();
    await expect(shown).toHaveClass(/lifeos-event-cancelled/);

    await shown.click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Cancelled");
    await dialog.getByRole("button", { name: "Restore" }).click();
    await expect(dialog.getByText("Cancelled", { exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await page.getByRole("checkbox", { name: "Show cancelled" }).uncheck();
    await expect(eventIn(page, title)).toBeVisible();
    await expect(eventIn(page, title)).not.toHaveClass(
      /lifeos-event-cancelled/,
    );
  });

  test("shows every occurrence of a weekly event in the month", async ({
    page,
    request,
  }) => {
    const day = addDays(today(), 1);
    const title = `Workspace weekly ${stamp}`;
    const created = await request.post("/api/calendar/v1/events", {
      data: {
        title,
        allDay: false,
        startAt: await berlinInstant(page, day, "08:00"),
        endAt: await berlinInstant(page, day, "08:30"),
        recurrence: { frequency: "weekly", interval: 1 },
      },
    });
    expect(created.status()).toBe(201);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));

    // The list API expands the series server-side: one item per week.
    await open(page, "list", day);
    await expect(eventIn(page, title)).toHaveCount(5, { timeout: 15_000 });
    await eventIn(page, title).nth(2).click();
    await expect(page.getByRole("dialog")).toContainText(title);
    expect(errors).toEqual([]);
  });

  test("fits a 360px phone, opens in the list, and reads well in dark", async ({
    page,
  }) => {
    const problems = watchForProblems(page);
    await page.setViewportSize({ width: 360, height: 780 });
    await page.addInitScript(() => {
      window.localStorage.setItem("theme", "dark");
    });
    await page.goto("/calendar");
    await expect(page.locator("html")).toHaveClass(/\bdark\b/);
    await expect(workspace(page)).toHaveAttribute("data-view", "list");
    await expect(events(page)).toHaveAttribute("aria-busy", "false");

    // The calendar surface uses the dark card and light text.
    const [surface, text] = await events(page).evaluate((element) => {
      const luminance = (color: string) => {
        const context = document.createElement("canvas").getContext("2d")!;
        context.fillStyle = color;
        context.fillRect(0, 0, 1, 1);
        const [r, g, b] = Array.from(context.getImageData(0, 0, 1, 1).data);
        return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
      };
      const style = getComputedStyle(element);
      return [luminance(style.backgroundColor), luminance(style.color)];
    });
    expect(surface).toBeLessThan(0.25);
    expect(text).toBeGreaterThan(0.75);

    for (const view of ["Month", "Week", "Day", "List"]) {
      await page
        .getByRole("group", { name: "View" })
        .getByRole("button", { name: view })
        .click();
      await expect(workspace(page)).toHaveAttribute(
        "data-view",
        view.toLowerCase(),
      );
      expect(
        await horizontalOverflow(page),
        `${view} overflows at 360px`,
      ).toBeLessThanOrEqual(0);
    }

    await page.getByRole("button", { name: "New event" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
    await page.keyboard.press("Escape");

    await page.getByRole("combobox", { name: "Search events" }).fill("a");
    await expect(page.getByTestId("calendar-search-results")).toBeVisible();
    expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
    expect(problems).toEqual([]);
  });

  test("opens in the month on a desktop", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/calendar");
    await expect(workspace(page)).toHaveAttribute("data-view", "month");
    // Opening the page leaves its URL alone; moving records view and date.
    await expect(page).toHaveURL(/\/calendar$/);
    await page.getByRole("button", { name: "Next month" }).click();
    await expect(page).toHaveURL(
      /\/calendar\?view=month&date=\d{4}-\d{2}-\d{2}$/,
    );
  });
});
