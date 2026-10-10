import { type APIRequestContext, expect, type Page, test } from "./fixtures";
import { datePicker, pickDate } from "./date-picker";

// Recurring events on /calendar: Repeat in the event form (Custom weekly on
// two days, "Annually on September 17"), skipping and restoring one
// occurrence (This event), changing This and following, and deleting All
// events. The browser runs in Europe/Berlin (playwright.config.ts); setup
// follows calendar-workspace.spec.ts.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const ZONE = "Europe/Berlin";

type CalendarEvent = {
  id: string;
  title: string;
  recurrence?: {
    frequency: string;
    interval: number;
    byWeekday?: string[] | null;
  } | null;
};

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

/** The first Monday on or after `day`. */
function mondayFrom(day: string) {
  const weekday = new Date(`${day}T00:00:00Z`).getUTCDay();
  return addDays(day, (8 - weekday) % 7);
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

const workspace = (page: Page) => page.locator("#calendar");
const events = (page: Page) =>
  page.getByRole("region", { name: "Calendar events" });
const eventIn = (page: Page, title: string) =>
  events(page).locator(".fc-event", { hasText: title });
/** The event on a day of the week or month view (its column or cell). */
const eventOn = (page: Page, title: string, day: string) =>
  events(page).locator(`[data-date="${day}"] .fc-event`, { hasText: title });
const dialogs = (page: Page) => page.getByRole("dialog");

/** Opens a view at a date and waits until its events have loaded. */
async function open(page: Page, view: string, date: string) {
  await page.goto(`/calendar?view=${view}&date=${date}`);
  await expect(workspace(page)).toHaveAttribute("data-view", view);
  await expect(events(page)).toHaveAttribute("aria-busy", "false");
}

/** The ids of the series titled `title` (search lists one occurrence each). */
async function seriesIds(request: APIRequestContext, title: string) {
  const response = await request.get(
    `/api/calendar/v1/events/search?q=${encodeURIComponent(title)}&includeCancelled=true&limit=100`,
  );
  expect(response.ok()).toBeTruthy();
  const { items } = (await response.json()) as { items: CalendarEvent[] };
  return [
    ...new Set(
      items.filter((event) => event.title === title).map((event) => event.id),
    ),
  ];
}

async function deleteSeries(request: APIRequestContext, title: string) {
  for (const id of await seriesIds(request, title)) {
    await request.delete(`/api/calendar/v1/events/${id}`);
  }
}

test.describe("Recurring events", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  const stamp = Date.now();

  test.afterAll(async ({ request }) => {
    // Remove whatever a failed test left behind.
    await deleteSeries(request, `Gym ${stamp}`);
    await deleteSeries(request, `Birthday ${stamp}`);
  });

  test("Gym weekly on two days: skip one, restore it, change this and following, delete all", async ({
    page,
    request,
  }) => {
    const title = `Gym ${stamp}`;
    const monday = mondayFrom(addDays(today(), 2));
    const wednesday = addDays(monday, 2);
    const monday2 = addDays(monday, 7);
    const wednesday2 = addDays(monday, 9);
    const details = page.getByRole("dialog", { name: title });
    const editScope = page.getByRole("dialog", {
      name: "Edit recurring event",
    });
    const deleteScope = page.getByRole("dialog", {
      name: "Delete recurring event",
    });
    try {
      // Create: Custom, weekly on Monday (the start, always on) and Wednesday.
      await open(page, "week", monday);
      await page.getByRole("button", { name: "New event" }).click();
      const form = page.getByRole("dialog", { name: "New event" });
      await form.getByLabel("Title").fill(title);
      await pickDate(form, "Start date", monday);
      await form.getByLabel("Start time").fill("18:00");
      await pickDate(form, "End date", monday);
      await form.getByLabel("End time").fill("19:00");
      const repeat = form.getByLabel("Repeat", { exact: true });
      await expect(repeat).toHaveValue("none");
      await expect(repeat).toContainText("Weekly on Monday");
      await repeat.selectOption({ label: "Custom…" });
      await expect(form.getByLabel("Repeat unit")).toHaveValue("weekly");
      const days = form.getByRole("group", { name: "Repeat on" });
      const start = days.getByRole("button", { name: "Monday" });
      await expect(start).toBeDisabled();
      await expect(start).toHaveAttribute("aria-pressed", "true");
      await expect(form).toContainText("Always on Monday, the day it starts.");
      await days.getByRole("button", { name: "Wednesday" }).click();
      await expect(form.getByTestId("repeat-summary")).toHaveText(
        "Weekly on Monday and Wednesday",
      );
      await form.getByRole("button", { name: "Save" }).click();
      await expect(dialogs(page)).toHaveCount(0);

      // Both days, this week and the next.
      await expect(eventOn(page, title, monday)).toHaveCount(1);
      await expect(eventOn(page, title, wednesday)).toHaveCount(1);
      await expect(eventIn(page, title)).toHaveCount(2);
      await page.getByRole("button", { name: "Next week" }).click();
      await expect(eventOn(page, title, monday2)).toHaveCount(1);
      await expect(eventOn(page, title, wednesday2)).toHaveCount(1);
      const [id] = await seriesIds(request, title);
      const stored = (await (
        await request.get(`/api/calendar/v1/events/${id}`)
      ).json()) as CalendarEvent;
      expect(stored.recurrence).toMatchObject({
        frequency: "weekly",
        interval: 1,
        byWeekday: ["MO", "WE"],
      });

      // An occurrence shows its own date and how the series repeats.
      await eventOn(page, title, wednesday2).click();
      await expect(details.getByTestId("event-recurrence")).toHaveText(
        "Weekly on Monday and Wednesday",
      );
      await expect(details.getByTestId("event-when")).toHaveText(
        `${dayLabel(wednesday2)} · 18:00–19:00`,
      );
      await page.keyboard.press("Escape");
      await expect(dialogs(page)).toHaveCount(0);

      // Skip this week's Wednesday: hidden until Show cancelled, restorable.
      await page.getByRole("button", { name: "Previous week" }).click();
      await eventOn(page, title, wednesday).click();
      await details
        .getByRole("button", { name: "Delete", exact: true })
        .click();
      await expect(
        deleteScope.getByRole("button", { name: "This event", exact: true }),
      ).toBeVisible();
      await deleteScope
        .getByRole("button", { name: "This event", exact: true })
        .click();
      await expect(dialogs(page)).toHaveCount(0);
      await expect(eventOn(page, title, wednesday)).toHaveCount(0);
      await expect(eventOn(page, title, monday)).toHaveCount(1);
      await page.getByRole("checkbox", { name: "Show cancelled" }).check();
      const skipped = eventOn(page, title, wednesday);
      await expect(skipped).toHaveClass(/lifeos-event-cancelled/);
      await skipped.click();
      await expect(
        details.getByText("Cancelled", { exact: true }),
      ).toBeVisible();
      await details.getByRole("button", { name: "Restore this event" }).click();
      await expect(details.getByText("Cancelled", { exact: true })).toHaveCount(
        0,
      );
      await page.keyboard.press("Escape");
      await expect(dialogs(page)).toHaveCount(0);
      await page.getByRole("checkbox", { name: "Show cancelled" }).uncheck();
      await expect(eventOn(page, title, wednesday)).toHaveCount(1);
      await expect(eventOn(page, title, wednesday)).not.toHaveClass(
        /lifeos-event-cancelled/,
      );

      // This and following: from next Monday on, an hour later.
      await page.getByRole("button", { name: "Next week" }).click();
      await eventOn(page, title, monday2).click();
      await details.getByRole("button", { name: "Edit", exact: true }).click();
      const edit = page.getByRole("dialog", { name: "Edit event" });
      await expect(datePicker(edit, "Start date")).toHaveAttribute(
        "data-value",
        monday2,
      );
      await edit.getByLabel("Start time").fill("19:00");
      await expect(edit.getByLabel("End time")).toHaveValue("20:00");
      await edit.getByRole("button", { name: "Save" }).click();
      await expect(
        editScope.getByRole("button", { name: "This event", exact: true }),
      ).toBeVisible();
      await editScope
        .getByRole("button", { name: "This and following events" })
        .click();
      await expect(dialogs(page)).toHaveCount(0);
      for (const day of [monday2, wednesday2]) {
        await eventOn(page, title, day).click();
        await expect(details.getByTestId("event-when")).toHaveText(
          `${dayLabel(day)} · 19:00–20:00`,
        );
        await page.keyboard.press("Escape");
        await expect(dialogs(page)).toHaveCount(0);
      }
      // The earlier occurrences keep their time.
      await page.getByRole("button", { name: "Previous week" }).click();
      for (const day of [monday, wednesday]) {
        await eventOn(page, title, day).click();
        await expect(details.getByTestId("event-when")).toHaveText(
          `${dayLabel(day)} · 18:00–19:00`,
        );
        await page.keyboard.press("Escape");
        await expect(dialogs(page)).toHaveCount(0);
      }
      // The old series ended; a new one starts next Monday.
      expect(await seriesIds(request, title)).toHaveLength(2);

      // All events: the new series goes, the earlier week stays.
      await page.getByRole("button", { name: "Next week" }).click();
      await eventOn(page, title, monday2).click();
      await details
        .getByRole("button", { name: "Delete", exact: true })
        .click();
      await deleteScope.getByRole("button", { name: "All events" }).click();
      await expect(dialogs(page)).toHaveCount(0);
      await expect(eventIn(page, title)).toHaveCount(0);
      expect(await seriesIds(request, title)).toHaveLength(1);
      await page.getByRole("button", { name: "Previous week" }).click();
      await expect(eventIn(page, title)).toHaveCount(2);
      await eventOn(page, title, monday).click();
      await details
        .getByRole("button", { name: "Delete", exact: true })
        .click();
      await deleteScope.getByRole("button", { name: "All events" }).click();
      await expect(dialogs(page)).toHaveCount(0);
      await expect(eventIn(page, title)).toHaveCount(0);
      expect(await seriesIds(request, title)).toEqual([]);
    } finally {
      await deleteSeries(request, title);
    }
  });

  test("a birthday repeats annually on September 17", async ({
    page,
    request,
  }) => {
    const title = `Birthday ${stamp}`;
    const year = Number(today().slice(0, 4));
    const first = `${year}-09-17`;
    const next = `${year + 1}-09-17`;
    const details = page.getByRole("dialog", { name: title });
    try {
      await open(page, "month", first);
      await page.getByRole("button", { name: "New event" }).click();
      const form = page.getByRole("dialog", { name: "New event" });
      await form.getByLabel("Title").fill(title);
      await form.getByRole("checkbox", { name: "All day" }).check();
      await pickDate(form, "Start date", first);
      await pickDate(form, "End date", first);
      // The plainly worded choice follows the start date.
      await form
        .getByLabel("Repeat", { exact: true })
        .selectOption({ label: "Annually on September 17" });
      await form.getByRole("button", { name: "Save" }).click();
      await expect(dialogs(page)).toHaveCount(0);
      await expect(eventOn(page, title, first)).toBeVisible();

      // Next year, on the same date.
      await open(page, "month", next);
      const again = eventOn(page, title, next);
      await expect(again).toBeVisible();
      await expect(eventIn(page, title)).toHaveCount(1);
      await again.click();
      await expect(details.getByTestId("event-recurrence")).toHaveText(
        "Annually on September 17",
      );
      await expect(details.getByTestId("event-when")).toHaveText(
        `${dayLabel(next)} · All day`,
      );

      // All events: every year goes.
      await details
        .getByRole("button", { name: "Delete", exact: true })
        .click();
      await page
        .getByRole("dialog", { name: "Delete recurring event" })
        .getByRole("button", { name: "All events" })
        .click();
      await expect(dialogs(page)).toHaveCount(0);
      await expect(eventIn(page, title)).toHaveCount(0);
      expect(await seriesIds(request, title)).toEqual([]);
    } finally {
      await deleteSeries(request, title);
    }
  });
});
