import { type APIRequestContext, expect, type Page, test } from "./fixtures";

// Polish — Life OS 1 sign-off across every Dashboard page: no horizontal
// overflow at phone or desktop width, no hydration errors for a Berlin,
// German-language browser, and both themes, with realistic data on the pages.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

const pages = [
  "/",
  "/tasks",
  "/inbox",
  "/calendar",
  "/activity",
  "/status",
  "/finance",
  "/food",
] as const;

const viewports = [
  { width: 360, height: 780 },
  { width: 390, height: 844 },
  { width: 1280, height: 800 },
] as const;

const hydrationProblem =
  /hydrat|did not match|Minified React error #4(18|19|21|23|25)/i;

async function seed(request: APIRequestContext, stamp: number) {
  const long =
    "Polish long row: this name is long enough to push a fixed desktop row layout past the edge of a phone screen";
  for (const data of [
    { title: `${long} ${stamp}` },
    { title: `Polish dated ${stamp}`, dueAt: new Date().toISOString() },
    {
      title: `Polish overdue ${stamp}`,
      dueAt: new Date(Date.now() - 2 * 86_400_000).toISOString(),
    },
  ]) {
    const response = await request.post("/api/tasks/v1/tasks", { data });
    expect(response.status()).toBe(201);
  }
  const start = new Date(Date.now() + 86_400_000);
  const event = await request.post("/api/calendar/v1/events", {
    data: {
      title: `Polish event with a fairly long title ${stamp}`,
      allDay: false,
      startAt: start.toISOString(),
      endAt: new Date(start.getTime() + 3_600_000).toISOString(),
    },
  });
  expect(event.status()).toBe(201);
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

async function horizontalOverflow(page: Page) {
  return page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
}

test.describe("Polish — Life OS 1 acceptance", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test.use({ timezoneId: "Europe/Berlin", locale: "de-DE" });

  test.beforeAll(async ({ request }) => {
    await seed(request, Date.now());
  });

  for (const theme of ["light", "dark"] as const) {
    for (const viewport of viewports) {
      test(`every page fits ${viewport.width}px in ${theme} theme without hydration errors`, async ({
        page,
      }) => {
        await page.setViewportSize(viewport);
        await page.addInitScript((value) => {
          window.localStorage.setItem("theme", value);
        }, theme);
        const problems = watchForProblems(page);
        for (const path of pages) {
          await page.goto(path);
          await page.waitForLoadState("networkidle");
          await expect(page.locator("html")).toHaveClass(
            theme === "dark" ? /\bdark\b/ : /^(?!.*\bdark\b)/,
          );
          const { scrollWidth, innerWidth } = await horizontalOverflow(page);
          expect(
            scrollWidth,
            `${path} overflows horizontally at ${viewport.width}px (${theme})`,
          ).toBeLessThanOrEqual(innerWidth);
        }
        expect(problems).toEqual([]);
      });
    }
  }

  for (const viewport of [viewports[1], viewports[2]]) {
    test(`Home opens with the capture composer visible at ${viewport.width}px`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto("/");
      const composer = page.locator("#inbox-capture");
      await expect(composer).toBeVisible();
      await expect(composer).toBeInViewport({ ratio: 1 });
      await expect(
        page.getByRole("button", { name: /record|voice|dictate/i }).first(),
      ).toBeInViewport();
    });
  }
});
