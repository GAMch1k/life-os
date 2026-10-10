import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

const format =
  (timeZone: string, options: Intl.DateTimeFormatOptions) => (date: Date) =>
    new Intl.DateTimeFormat("en-GB", { ...options, timeZone }).format(date);

test.describe("Owner timezone", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("Calendar shows event times and today in the owner's timezone", async ({
    page,
    request,
  }) => {
    const hour = 3_600_000;
    const start = new Date(Math.floor((Date.now() + 3 * hour) / hour) * hour);
    const end = new Date(start.getTime() + hour);
    const title = `Owner timezone event ${Date.now()}`;
    const created = await request.post("/api/calendar/v1/events", {
      data: {
        title,
        allDay: false,
        startAt: start.toISOString(),
        endAt: end.toISOString(),
      },
    });
    expect(created.status()).toBe(201);

    await page.goto("/calendar");
    await expect(
      page.getByRole("heading", { level: 1, name: "Calendar" }),
    ).toBeVisible();
    // The Dashboard's LIFE_OS_TIMEZONE, which the page renders times in.
    const timeZone =
      (await page.locator("#calendar").getAttribute("data-time-zone")) ?? "";
    expect(timeZone).not.toBe("");

    const day = format(timeZone, {
      weekday: "short",
      day: "numeric",
      month: "short",
    });
    const time = format(timeZone, { hour: "2-digit", minute: "2-digit" });
    const dayKey = format(timeZone, { dateStyle: "short" });
    const label =
      dayKey(start) === dayKey(end)
        ? `${day(start)} · ${time(start)}–${time(end)}`
        : `${day(start)} · ${time(start)}`;

    const section = page.getByRole("region", {
      name: dayKey(start) === dayKey(new Date()) ? "Today" : "Upcoming",
    });
    await expect(
      section.getByRole("listitem").filter({ hasText: title }),
    ).toContainText(label);
  });

  test("Calendar and Status fit a 360px screen", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 780 });
    for (const path of ["/calendar", "/status"]) {
      await page.goto(path);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(overflow, `${path} overflows horizontally`).toBeLessThanOrEqual(0);
    }
  });
});
