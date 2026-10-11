import { expect, test, type Page } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

/** Console errors (React reports hydration problems there) and page errors. */
function collectProblems(page: Page) {
  const problems: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error" || /hydrat/i.test(message.text())) {
      problems.push(`${message.type()}: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => problems.push(`pageerror: ${error.message}`));
  return problems;
}

/** Relative luminance (0 black – 1 white) of the computed body background. */
function bodyLuminance(page: Page) {
  return page.evaluate(() => {
    const context = document.createElement("canvas").getContext("2d")!;
    context.fillStyle = getComputedStyle(document.body).backgroundColor;
    context.fillRect(0, 0, 1, 1);
    const [r, g, b] = Array.from(context.getImageData(0, 0, 1, 1).data)
      .slice(0, 3)
      .map((value) => {
        const channel = value / 255;
        return channel <= 0.04045
          ? channel / 12.92
          : ((channel + 0.055) / 1.055) ** 2.4;
      });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  });
}

async function expectTheme(page: Page, theme: "light" | "dark") {
  const html = page.locator("html");
  if (theme === "dark") {
    await expect(html).toHaveClass(/(^|\s)dark(\s|$)/);
    expect(await bodyLuminance(page)).toBeLessThan(0.05);
  } else {
    await expect(html).not.toHaveClass(/(^|\s)dark(\s|$)/);
    expect(await bodyLuminance(page)).toBeGreaterThan(0.8);
  }
}

/** The toggle only reflects (and changes) the theme once hydrated. */
async function expectSelected(page: Page, label: string) {
  await expect(page.getByRole("button", { name: label })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
}

async function expectNoHorizontalOverflow(page: Page) {
  const { scrollWidth, innerWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  expect(scrollWidth).toBeLessThanOrEqual(innerWidth);
}

test.describe("Theme", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test.use({ colorScheme: "light" });

  test("dark and light choices persist across reloads and pages", async ({
    page,
  }) => {
    await page.goto("/");
    await expectSelected(page, "System theme");
    await expectTheme(page, "light");

    await page.getByRole("button", { name: "Dark theme" }).click();
    await expectTheme(page, "dark");
    expect(await page.evaluate(() => localStorage.getItem("theme"))).toBe(
      "dark",
    );

    await page.reload();
    await expectTheme(page, "dark");
    await expectSelected(page, "Dark theme");

    await page
      .getByRole("navigation", { name: "Main navigation" })
      .getByRole("link", { name: "Finance" })
      .click();
    await expect(page).toHaveURL(/\/finance$/);
    await expectTheme(page, "dark");

    await page.goto("/status");
    await expectTheme(page, "dark");

    await expectSelected(page, "Dark theme");
    await page.getByRole("button", { name: "Light theme" }).click();
    await expectTheme(page, "light");
    await page.reload();
    await expectTheme(page, "light");
    await expectSelected(page, "Light theme");
    await page.goto("/");
    await expectTheme(page, "light");
  });

  test("System theme follows the device preference", async ({ page }) => {
    await page.goto("/");
    await expectSelected(page, "System theme");
    await page.emulateMedia({ colorScheme: "dark" });
    await expectTheme(page, "dark");
    await page.emulateMedia({ colorScheme: "light" });
    await expectTheme(page, "light");
  });

  for (const theme of ["light", "dark"] as const) {
    test(`loads in ${theme} without console errors or hydration warnings`, async ({
      page,
    }) => {
      await page.addInitScript((value) => {
        localStorage.setItem("theme", value);
      }, theme);
      const problems = collectProblems(page);
      for (const path of ["/", "/finance"]) {
        await page.goto(path);
        await expectSelected(
          page,
          theme === "dark" ? "Dark theme" : "Light theme",
        );
        await expectTheme(page, theme);
      }
      await page.waitForLoadState("networkidle");
      expect(problems).toEqual([]);
    });
  }

  test("phone layout fits 360 px without sideways scrolling", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 360, height: 780 });
    for (const path of ["/", "/finance", "/food"]) {
      await page.goto(path);
      const mobileNav = page.getByRole("navigation", {
        name: "Mobile navigation",
      });
      await expect(mobileNav).toBeVisible();
      await expect(mobileNav.getByRole("link")).toHaveCount(8);
      // Every label is shown in full, not cut off.
      const clipped = await mobileNav
        .locator("span")
        .evaluateAll((labels) =>
          labels
            .filter((label) => label.scrollWidth > label.clientWidth)
            .map((label) => label.textContent),
        );
      expect(clipped).toEqual([]);
      await expectNoHorizontalOverflow(page);
    }
  });

  test("Finance does not scroll sideways on a desktop (BUG-11)", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/finance");
    await expect(
      page.getByRole("heading", { name: /^Finance/, level: 1 }),
    ).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });
});
