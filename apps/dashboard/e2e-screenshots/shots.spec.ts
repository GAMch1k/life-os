import { readFile } from "node:fs/promises";
import path from "node:path";

import { expect, type Page, test } from "@playwright/test";

import { today } from "./demo-data";
import { screenshotsDir } from "./paths";

/**
 * The README's screenshots, of the demo data (demo.setup.ts): each page at
 * a laptop's size, and two at a phone's. They are taken at twice the size
 * and made smaller by e2e-screenshots/optimize.mjs, which writes the WebP
 * files the README shows. Last, the repository's social preview (1280×640,
 * GitHub's size), made from the Home screenshot.
 */

type Shot = {
  name: string;
  path: string;
  /** Text the page shows once the demo data is on it. */
  shows: string;
  ready?: (page: Page) => Promise<void>;
};

const calendarLoaded = async (page: Page) => {
  await expect(
    page.getByRole("region", { name: "Calendar events" }),
  ).toHaveAttribute("aria-busy", "false");
};

const desktop: Shot[] = [
  { name: "home", path: "/", shows: "Mark the bill as paid" },
  { name: "tasks", path: "/tasks", shows: "Renew passport" },
  {
    name: "calendar",
    path: `/calendar?view=week&date=${today()}`,
    shows: "Dentist check-up",
    ready: calendarLoaded,
  },
  { name: "inbox", path: "/inbox", shows: "Mark the bill as paid" },
  { name: "finance", path: "/finance", shows: "Everyday account" },
  { name: "food", path: "/food", shows: "Sourdough bread" },
];

const phone: Shot[] = [
  { name: "phone-home", path: "/", shows: "Mark the bill as paid" },
  { name: "phone-food", path: "/food", shows: "Sourdough bread" },
];

async function take(page: Page, shot: Shot) {
  await page.goto(shot.path);
  await expect(page.getByText(shot.shows).first()).toBeVisible();
  await shot.ready?.(page);
  // Late requests (the notification bell, counts) and transitions settle.
  await page.waitForLoadState("networkidle").catch(() => undefined);
  await page.evaluate(() => (document.activeElement as HTMLElement)?.blur());
  await page.mouse.move(0, 0);
  await page.waitForTimeout(800);
  await page.screenshot({
    path: path.join(screenshotsDir(), `${shot.name}.png`),
    animations: "disabled",
    caret: "hide",
  });
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2 });
  for (const shot of desktop) {
    test(shot.name, async ({ page }) => take(page, shot));
  }
});

test.describe("phone", () => {
  test.use({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true,
  });
  for (const shot of phone) {
    test(shot.name, async ({ page }) => take(page, shot));
  }
});

test.describe("social preview", () => {
  test.use({ viewport: { width: 1280, height: 640 }, deviceScaleFactor: 1 });
  test("social-preview", async ({ page }) => {
    const home = await readFile(path.join(screenshotsDir(), "home.png"));
    await page.setContent(`<!doctype html>
<html><body style="margin:0;width:1280px;height:640px;overflow:hidden;position:relative;background:#171717;color:#fafafa;font-family:system-ui,'DejaVu Sans',sans-serif">
  <div style="position:absolute;left:72px;top:96px;width:400px">
    <div style="font-size:64px;font-weight:700;letter-spacing:-1px">Life OS</div>
    <p style="margin:28px 0 0;font-size:30px;line-height:1.35;color:#e5e5e5">Tasks, calendar, inbox, money and food in one app, on your own server.</p>
    <p style="margin:32px 0 0;font-size:22px;color:#a3a3a3">Self-hosted with Docker · AGPL-3.0</p>
  </div>
  <img alt="" src="data:image/png;base64,${home.toString("base64")}"
    style="position:absolute;left:520px;top:64px;width:880px;border-radius:12px;box-shadow:0 24px 64px rgba(0,0,0,.55)">
</body></html>`);
    await page.screenshot({
      path: path.join(screenshotsDir(), "social-preview.png"),
    });
  });
});
