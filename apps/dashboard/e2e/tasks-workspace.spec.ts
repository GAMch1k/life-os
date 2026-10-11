import {
  expect,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "./fixtures";
import { pickDate } from "./date-picker";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Task = { id: string; title: string; dueAt?: string | null };

async function createTask(
  request: APIRequestContext,
  title: string,
  dueAt?: string,
): Promise<Task> {
  const response = await request.post("/api/tasks/v1/tasks", {
    data: { title, ...(dueAt ? { dueAt } : {}) },
  });
  expect(response.status()).toBe(201);
  return response.json();
}

/**
 * Opens /tasks and reads today and the timezone the server used, once the
 * page has settled in the browser's timezone (a first visit renders in
 * LIFE_OS_TIMEZONE and refreshes once).
 */
async function openTasks(page: Page) {
  await page.goto("/tasks");
  const workspace = page.getByTestId("tasks-workspace");
  await expect(workspace).toHaveAttribute(
    "data-time-zone",
    await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone),
  );
  return {
    today: (await workspace.getAttribute("data-today"))!,
    timeZone: (await workspace.getAttribute("data-time-zone"))!,
  };
}

/** The day `days` after a YYYY-MM-DD day. */
function shift(day: string, days: number) {
  const date = new Date(`${day}T12:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** Noon UTC is the same calendar day for every owner timezone within ±11 h. */
const noon = (day: string) => `${day}T12:00:00Z`;

/** An instant's wall time (YYYY-MM-DD HH:mm) in a timezone. */
function wallTime(instant: string, timeZone: string) {
  const parts = new Intl.DateTimeFormat("en-GB", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(new Date(instant));
  const part = (type: string) =>
    parts.find((entry) => entry.type === type)?.value;
  return `${part("year")}-${part("month")}-${part("day")} ${part("hour")}:${part("minute")}`;
}

/** Picks a day in the Tasks date picker, paging from today's month. */
async function pickDay(page: Page, today: string, day: string) {
  const calendar = page.getByTestId("tasks-calendar");
  const months =
    (Number(day.slice(0, 4)) - Number(today.slice(0, 4))) * 12 +
    Number(day.slice(5, 7)) -
    Number(today.slice(5, 7));
  const pager =
    months > 0 ? "Go to the Next Month" : "Go to the Previous Month";
  for (let step = 0; step < Math.abs(months); step++)
    await calendar.getByRole("button", { name: pager }).click();
  await calendar
    .locator(`td[data-day="${day}"]:not([data-outside]) button`)
    .click();
}

const region = (page: Page, name: string) =>
  page.getByRole("region", { name, exact: true });

const UNDATED = "Unscheduled / No date";
const undatedToggle = (page: Page) =>
  page.locator('[aria-controls="tasks-undated-list"]');
const completedToggle = (page: Page) =>
  page.locator('[aria-controls="tasks-completed-list"]');

/** Expands a section toggle, retrying a click made before hydration. */
async function expand(toggle: Locator) {
  await expect(async () => {
    if ((await toggle.getAttribute("aria-expanded")) !== "true")
      await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true", {
      timeout: 1_000,
    });
  }).toPass();
}

/**
 * No date lists the newest five undated Tasks until expanded, and other specs
 * add undated Tasks to the shared stack, so list them all before looking for
 * one. Without more than five there is nothing to expand.
 */
async function showAllUndated(page: Page) {
  if (await undatedToggle(page).count()) await expand(undatedToggle(page));
}

/** Creates Tasks a few at a time in parallel. */
async function createTasks(
  request: APIRequestContext,
  tasks: { title: string; dueAt?: string }[],
) {
  const created: Task[] = [];
  for (let start = 0; start < tasks.length; start += BATCH) {
    created.push(
      ...(await Promise.all(
        tasks
          .slice(start, start + BATCH)
          .map(({ title, dueAt }) => createTask(request, title, dueAt)),
      )),
    );
    await pace();
  }
  return created;
}

// The gateway allows 30 API requests per second per client (burst 60), shared
// with the specs running in parallel; bulk seeding stays well under it.
const BATCH = 10;
const pace = () => new Promise((resolve) => setTimeout(resolve, 1_000));

async function completeTask(request: APIRequestContext, id: string) {
  const response = await request.post(`/api/tasks/v1/tasks/${id}/complete`);
  expect(response.ok()).toBeTruthy();
}

test.describe("Tasks workspace", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  // Days and times follow the browser's timezone, here far from the
  // Dashboard's LIFE_OS_TIMEZONE.
  test.use({ timezoneId: "America/New_York" });

  test("groups open Tasks by the owner's day and follows the focused date", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const { today } = await openTasks(page);
    const upcomingDay = shift(today, 2);
    const titles = {
      overdue: `P8 overdue ${stamp}`,
      today: `P8 today ${stamp}`,
      upcoming: `P8 upcoming ${stamp}`,
      unscheduled: `P8 unscheduled ${stamp}`,
    };
    await createTask(request, titles.overdue, noon(shift(today, -2)));
    await createTask(request, titles.today, noon(today));
    await createTask(request, titles.upcoming, noon(upcomingDay));
    await createTask(request, titles.unscheduled);

    await page.reload();
    await showAllUndated(page);
    await expect(region(page, "Overdue")).toContainText(titles.overdue);
    await expect(region(page, "Today")).toContainText(titles.today);
    await expect(region(page, "Upcoming")).toContainText(titles.upcoming);
    await expect(region(page, UNDATED)).toContainText(titles.unscheduled);

    // Schedule projects the focused day's Monday-to-Sunday week, and undated
    // work stays listed beside it.
    await page.getByRole("button", { name: "Schedule", exact: true }).click();
    await expect(page.getByTestId(`schedule-day-${today}`)).toContainText(
      titles.today,
    );
    await expect(region(page, UNDATED)).toContainText(titles.unscheduled);
    await pickDay(page, today, upcomingDay);
    await expect(page.getByTestId(`schedule-day-${upcomingDay}`)).toContainText(
      titles.upcoming,
    );

    // In List, the focused day's Tasks come first, listed once.
    await page.getByRole("button", { name: "List", exact: true }).click();
    const focused = page.getByTestId("tasks-selected-day");
    await expect(focused).toContainText(titles.upcoming);
    await expect(region(page, "Upcoming")).not.toContainText(titles.upcoming);
    await expect(region(page, UNDATED)).toContainText(titles.unscheduled);
    await page.getByRole("button", { name: "Back to today" }).click();
    await expect(focused).toContainText(titles.today);
    await expect(region(page, "Upcoming")).toContainText(titles.upcoming);
  });

  test("edits and reopens an existing Task through the owning service", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const original = `P8 lifecycle ${stamp}`;
    const renamed = `P8 renamed ${stamp}`;
    const notes = `notes ${stamp}`;
    const task = await createTask(request, original);

    const { timeZone } = await openTasks(page);
    await showAllUndated(page);
    await page.getByLabel(`Edit ${original}`).click();
    await page.getByLabel("Task title", { exact: true }).fill(renamed);
    await page.getByLabel("Task notes", { exact: true }).fill(notes);
    await pickDate(page, "Due date", "2026-10-21");
    await page.getByLabel("Due time", { exact: true }).fill("09:30");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText(renamed)).toBeVisible();
    // The due date is wall time in the page's (the browser's) timezone.
    const saved = await (
      await request.get(`/api/tasks/v1/tasks/${task.id}`)
    ).json();
    expect(wallTime(saved.dueAt, timeZone)).toBe("2026-10-21 09:30");

    await page.getByLabel(`Edit ${renamed}`).click();
    await page.getByRole("button", { name: "Clear date" }).click();
    await expect(region(page, UNDATED)).toContainText(renamed);

    // Completed is collapsed by default; a Task completed here joins it.
    await page.getByLabel(`Complete ${renamed}`).click();
    await expect(region(page, UNDATED)).not.toContainText(renamed);
    await expand(completedToggle(page));
    await expect(region(page, "Completed")).toContainText(renamed);
    await page.getByLabel(`Reopen ${renamed}`).click();
    await expect(region(page, UNDATED)).toContainText(renamed);

    await page.reload();
    const persisted = await request.get(`/api/tasks/v1/tasks/${task.id}`);
    expect(persisted.ok()).toBeTruthy();
    const persistedTask = await persisted.json();
    expect(persistedTask).toMatchObject({
      title: renamed,
      notes,
      completed: false,
    });
    expect(persistedTask.dueAt).toBeFalsy();
  });

  test("deletes open and completed Tasks after confirming, and the Event Log keeps them", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const open = await createTask(request, `P8 delete open ${stamp}`);
    const done = await createTask(request, `P8 delete done ${stamp}`);
    await completeTask(request, done.id);

    await openTasks(page);
    await showAllUndated(page);
    await page.getByLabel(`Edit ${open.title}`).click();
    const confirm = page.getByRole("dialog");
    // Keeping it changes nothing.
    await page.getByLabel(`Delete ${open.title}`).click();
    await expect(confirm).toContainText(`Delete “${open.title}”?`);
    await expect(confirm).toContainText("This can't be undone.");
    await confirm.getByRole("button", { name: "Keep it" }).click();
    await expect(confirm).toBeHidden();
    await expect(region(page, UNDATED)).toContainText(open.title);
    await page.getByLabel(`Delete ${open.title}`).click();
    await confirm.getByRole("button", { name: "Delete Task" }).click();
    await expect(confirm).toBeHidden();
    await expect(region(page, UNDATED)).not.toContainText(open.title);

    await expand(completedToggle(page));
    await expect(region(page, "Completed")).toContainText(done.title);
    await page.getByLabel(`Edit ${done.title}`).click();
    await page.getByLabel(`Delete ${done.title}`).click();
    await expect(confirm).toContainText(`Delete “${done.title}”?`);
    await confirm.getByRole("button", { name: "Delete Task" }).click();
    await expect(confirm).toBeHidden();
    await expect(region(page, "Completed")).not.toContainText(done.title);

    // Gone from the Tasks API, so from every list after a reload.
    for (const task of [open, done])
      expect(
        (await request.get(`/api/tasks/v1/tasks/${task.id}`)).status(),
      ).toBe(404);
    await page.reload();
    await showAllUndated(page);
    await expect(region(page, UNDATED)).not.toContainText(open.title);
    await expect(region(page, "Completed")).not.toContainText(done.title);

    // The Event Log names each deleted Task, deleted by the owner (You).
    await page.goto("/activity?type=task.deleted");
    const entries = page.getByRole("list", { name: "Activity entries" });
    for (const task of [open, done]) {
      const entry = entries
        .getByRole("link")
        .filter({ hasText: `Task deleted: ${task.title}` });
      await expect(entry).toBeVisible();
      await expect(entry).toContainText("Tasks · You");
    }
  });
});

test.describe("Tasks workspace sections", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("No date counts every undated Task, lists the newest five, and remembers Show all", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    await createTasks(
      request,
      Array.from({ length: 7 }, (_, index) => ({
        title: `Undated ${stamp} ${index}`,
      })),
    );

    await openTasks(page);
    const section = region(page, UNDATED);
    const rows = page.locator("#tasks-undated-list li");
    const toggle = undatedToggle(page);
    // Other specs share the stack, so read the total rather than assume it.
    const total = Number(
      await section.locator("h2 + span").first().textContent(),
    );
    expect(total).toBeGreaterThanOrEqual(7);
    await expect(rows).toHaveCount(5);
    await expect(toggle).toHaveText(`Show all ${total}`);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");

    await expand(toggle);
    await expect(rows).toHaveCount(total);
    await expect(toggle).toHaveText("Show fewer");
    for (let index = 0; index < 7; index++)
      await expect(section).toContainText(`Undated ${stamp} ${index}`);

    // The choice survives a reload, applied once the page has hydrated.
    await page.reload();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(section).toContainText(`Undated ${stamp} 0`);
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(rows).toHaveCount(5);
    await page.reload();
    await expect(page.getByTestId("tasks-workspace")).toBeVisible();
    await expect(rows).toHaveCount(5);
  });

  test("a Task completed now is listed under Completed once expanded, and the section stays open", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const title = `Done this week ${stamp}`;
    const task = await createTask(request, title);
    await completeTask(request, task.id);

    await openTasks(page);
    const section = region(page, "Completed");
    const toggle = completedToggle(page);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(toggle).toContainText(/Completed\s*\d+\+? this week/);
    await expect(section).not.toContainText(title);

    await expand(toggle);
    await expect(section).toContainText("Completed in the last 7 days");
    await expect(section).toContainText(title);

    await page.reload();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(section).toContainText(title);
  });

  test("an undated Task stays listed behind more than 100 dated ones", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const { today } = await openTasks(page);
    // Well after every other spec's dates, so they land at the end of
    // Upcoming and Home's urgent card never sees them.
    const due = noon(shift(today, 90));
    const dated = await createTasks(
      request,
      Array.from({ length: 101 }, (_, index) => ({
        title: `Bulk dated ${stamp} ${index}`,
        dueAt: due,
      })),
    );
    const undated = `Behind the bulk ${stamp}`;
    await createTask(request, undated);

    try {
      await page.reload();
      await showAllUndated(page);
      await expect(region(page, UNDATED)).toContainText(undated);
      // Every dated one is listed too: the page read past the first 100.
      await expect(
        region(page, "Upcoming").getByText(`Bulk dated ${stamp} `),
      ).toHaveCount(101);
    } finally {
      // Keep the shared stack's open lists small for the other specs.
      for (let start = 0; start < dated.length; start += BATCH) {
        await Promise.all(
          dated
            .slice(start, start + BATCH)
            .map((task) => completeTask(request, task.id)),
        );
        await pace();
      }
    }
  });
});

test.describe("Tasks workspace in a German browser in Berlin", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test.use({ timezoneId: "Europe/Berlin", locale: "de-DE" });

  test("hydrates cleanly, aligns due dates, starts weeks on Monday, and fits phones", async ({
    page,
    request,
  }) => {
    const problems: string[] = [];
    page.on("pageerror", (error) => problems.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") problems.push(message.text());
    });
    const hydrationErrors = () =>
      problems.filter((text) => /hydrat|#418|did not match/i.test(text));

    const stamp = Date.now();
    await page.setViewportSize({ width: 1280, height: 900 });
    const { today } = await openTasks(page);
    const tomorrow = shift(today, 1);
    const short = `Tidy ${stamp}`;
    const long = `Sort the household paperwork, insurance renewals, and the long overdue tax folder before the end of the month ${stamp}`;
    const undated = `Someday ${stamp}`;
    await createTask(request, short, noon(tomorrow));
    await createTask(request, long, noon(tomorrow));
    await createTask(request, undated);

    await page.reload();
    await page.waitForLoadState("networkidle");
    // Responding to clicks proves the page has hydrated.
    await page.getByRole("button", { name: "Schedule", exact: true }).click();
    await expect(
      page.getByRole("region", { name: "Schedule view" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "List", exact: true }).click();
    await expect(region(page, "Upcoming")).toContainText(short);
    await showAllUndated(page);
    expect(hydrationErrors()).toEqual([]);

    // The date picker is a calendar grid whose weeks start on Monday (its
    // weekday header row is aria-hidden, so check the day buttons as well).
    const grid = page.getByTestId("tasks-calendar").getByRole("grid");
    await expect(grid).toBeVisible();
    await expect(grid.locator("thead th").first()).toHaveAttribute(
      "aria-label",
      "Monday",
    );
    for (const week of await grid.getByRole("row").all())
      await expect(week.getByRole("button").first()).toHaveAccessibleName(
        /^(Today, )?Monday/,
      );

    // Due dates share one column whatever the title length or section.
    const dueX = async (title: string) => {
      const box = await page
        .getByRole("listitem")
        .filter({ hasText: title })
        .locator('[data-slot="task-due"]')
        .boundingBox();
      expect(box).toBeTruthy();
      return box!.x;
    };
    const shortX = await dueX(short);
    expect(Math.abs((await dueX(long)) - shortX)).toBeLessThanOrEqual(1);
    expect(Math.abs((await dueX(undated)) - shortX)).toBeLessThanOrEqual(1);

    // Selecting a day lists that day's Tasks; undated work stays listed.
    await pickDay(page, today, tomorrow);
    const focused = page.getByTestId("tasks-selected-day");
    await expect(focused).toContainText(short);
    await expect(focused).toContainText(long);
    await expect(region(page, UNDATED)).toContainText(undated);

    // Phones reflow instead of scrolling sideways, with the date picker
    // collapsed and expanded.
    const fits = () =>
      page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      );
    const picker = page.locator('[aria-controls="tasks-date-picker"]');
    for (const width of [360, 390, 599]) {
      await page.setViewportSize({ width, height: 800 });
      await expect(picker).toBeVisible();
      for (let toggle = 0; toggle < 2; toggle++) {
        expect(await fits(), `no horizontal overflow at ${width}px`).toBe(true);
        await picker.click();
      }
    }
    await expect(picker).toHaveAttribute("aria-expanded", "false");
    await picker.click();
    await expect(grid).toBeVisible();
    expect(await fits()).toBe(true);
    expect(hydrationErrors()).toEqual([]);
  });
});
