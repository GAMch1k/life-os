import { expect, type Page, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

const format =
  (timeZone: string, options: Intl.DateTimeFormatOptions) => (date: Date) =>
    new Intl.DateTimeFormat("en-GB", { ...options, timeZone }).format(date);

const browserTimeZone = (page: Page) =>
  page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone);

const hydrationProblem =
  /hydrat|did not match|Minified React error #4(18|19|21|23|25)/i;

test.describe("Owner timezone", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("Calendar shows event times and today in the browser's timezone", async ({
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
    // A first visit renders in LIFE_OS_TIMEZONE and then refreshes once in
    // the browser's zone, which every time on the page is shown in.
    const timeZone = await browserTimeZone(page);
    await expect(page.locator("#calendar")).toHaveAttribute(
      "data-time-zone",
      timeZone,
    );

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

  test.describe("in a New York browser", () => {
    test.use({ timezoneId: "America/New_York" });

    test("pages re-render in the browser's timezone without hydration errors", async ({
      context,
      page,
    }) => {
      const problems: string[] = [];
      page.on("pageerror", (error) => problems.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error" && hydrationProblem.test(message.text()))
          problems.push(message.text());
      });
      await context.clearCookies();
      await page.goto("/tasks");
      const workspace = page.getByTestId("tasks-workspace");
      await expect(workspace).toHaveAttribute(
        "data-time-zone",
        "America/New_York",
      );
      const stored = (await context.cookies()).find(
        (cookie) => cookie.name === "lifeos-tz",
      );
      expect(decodeURIComponent(stored?.value ?? "")).toBe("America/New_York");
      // Later visits render in the browser's zone straight away.
      for (const path of ["/", "/inbox", "/calendar", "/activity", "/status"]) {
        await page.goto(path);
        await page.waitForLoadState("networkidle");
      }
      await page.goto("/tasks");
      await expect(workspace).toHaveAttribute(
        "data-time-zone",
        "America/New_York",
      );
      expect(problems).toEqual([]);
    });
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
