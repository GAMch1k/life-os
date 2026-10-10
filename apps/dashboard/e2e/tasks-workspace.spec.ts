import { expect, test, type APIRequestContext, type Page } from "./fixtures";

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
    await expect(region(page, "Overdue")).toContainText(titles.overdue);
    await expect(region(page, "Today")).toContainText(titles.today);
    await expect(region(page, "Upcoming")).toContainText(titles.upcoming);
    await expect(region(page, "Unscheduled / No date")).toContainText(
      titles.unscheduled,
    );

    // Schedule projects the focused day's Monday-to-Sunday week, and undated
    // work stays listed beside it.
    await page.getByRole("button", { name: "Schedule", exact: true }).click();
    await expect(page.getByTestId(`schedule-day-${today}`)).toContainText(
      titles.today,
    );
    await expect(region(page, "Unscheduled / No date")).toContainText(
      titles.unscheduled,
    );
    await pickDay(page, today, upcomingDay);
    await expect(page.getByTestId(`schedule-day-${upcomingDay}`)).toContainText(
      titles.upcoming,
    );

    // In List, the focused day's Tasks come first, listed once.
    await page.getByRole("button", { name: "List", exact: true }).click();
    const focused = page.getByTestId("tasks-selected-day");
    await expect(focused).toContainText(titles.upcoming);
    await expect(region(page, "Upcoming")).not.toContainText(titles.upcoming);
    await expect(region(page, "Unscheduled / No date")).toContainText(
      titles.unscheduled,
    );
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
    await page.getByLabel(`Edit ${original}`).click();
    await page.getByLabel("Task title", { exact: true }).fill(renamed);
    await page.getByLabel("Task notes", { exact: true }).fill(notes);
    await page.getByLabel("Due date", { exact: true }).fill("2026-10-21T09:30");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText(renamed)).toBeVisible();
    // The due date is wall time in the page's (the browser's) timezone.
    const saved = await (
      await request.get(`/api/tasks/v1/tasks/${task.id}`)
    ).json();
    expect(wallTime(saved.dueAt, timeZone)).toBe("2026-10-21 09:30");

    await page.getByLabel(`Edit ${renamed}`).click();
    await page.getByRole("button", { name: "Clear date" }).click();
    await expect(region(page, "Unscheduled / No date")).toContainText(renamed);

    await page.getByLabel(`Complete ${renamed}`).click();
    await expect(region(page, "Completed")).toContainText(renamed);
    await page.getByLabel(`Reopen ${renamed}`).click();
    await expect(region(page, "Unscheduled / No date")).toContainText(renamed);

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
    await expect(region(page, "Unscheduled / No date")).toContainText(undated);

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
