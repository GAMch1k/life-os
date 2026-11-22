import { defineConfig } from "@playwright/test";

import { STATE } from "./e2e-screenshots/paths";

/**
 * The README's screenshots (tools/screenshots,
 * docs/development/screenshots.md): a new install on the disposable
 * verification stack, with legacy access off. The demo project makes the
 * first account with the setup code and fills Life OS with the fictional
 * demo dataset (e2e-screenshots/demo-data.ts); the shots project then saves
 * the pages as PNG files in LIFE_OS_SCREENSHOTS_DIR. Never run it against a
 * Life OS with real data: it adds the demo data to whatever is there.
 *
 * The session's state (outputDir) holds a session cookie, so this run keeps
 * no trace, video, or HTML report, and CI uploads only the screenshots.
 */
export default defineConfig({
  testDir: "./e2e-screenshots",
  outputDir: "./test-results-screenshots",
  reporter: [["list"]],
  timeout: 180_000,
  // One account, one stack, and the shots follow the data.
  workers: 1,
  use: {
    baseURL: process.env.LIFE_OS_E2E_URL,
    trace: "off",
    screenshot: "off",
    video: "off",
    // The stack's LIFE_OS_TIMEZONE, so the pages render in one zone.
    timezoneId: "Europe/Berlin",
    locale: "en-GB",
    colorScheme: "light",
  },
  projects: [
    {
      name: "demo",
      testMatch: /demo\.setup\.ts/,
      use: { browserName: "chromium" },
    },
    {
      name: "shots",
      testMatch: /shots\.spec\.ts/,
      dependencies: ["demo"],
      use: { browserName: "chromium", storageState: STATE },
    },
  ],
});
