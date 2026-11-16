import { existsSync } from "node:fs";

import {
  type BrowserType,
  chromium,
  defineConfig,
  firefox,
  type Project,
  webkit,
} from "@playwright/test";

/**
 * A stranger's first sign-in (tools/smoke-fresh-install): the verification
 * stack with legacy access off, as a new install has it. Chromium makes the
 * first account with the setup code at the address an SSH tunnel gives
 * (http://127.0.0.1); then each browser signs in there and at
 * http://localhost, and keeps its session. WebKit is Safari's engine, which
 * refuses a Secure cookie on plain-HTTP loopback (src/lib/auth/origin.ts,
 * isLoopbackLogin). The browsers Playwright has run; those
 * LIFE_OS_E2E_BROWSERS names (comma-separated) must be there.
 */
const browsers: Record<string, BrowserType> = { chromium, firefox, webkit };
const required = (process.env.LIFE_OS_E2E_BROWSERS ?? "chromium")
  .split(",")
  .map((name) => name.trim())
  .filter(Boolean);
for (const name of required) {
  if (!browsers[name]) throw new Error(`unknown browser ${name}`);
  if (!existsSync(browsers[name].executablePath())) {
    throw new Error(
      `Playwright's ${name} is not installed (npx playwright install ${name}); LIFE_OS_E2E_BROWSERS needs it`,
    );
  }
}

const signIn: Project[] = Object.entries(browsers)
  .filter(([, type]) => existsSync(type.executablePath()))
  .map(([name]) => ({
    name,
    testMatch: /sign-in\.spec\.ts/,
    dependencies: ["first-account"],
    use: { browserName: name as "chromium" | "firefox" | "webkit" },
  }));

export default defineConfig({
  testDir: "./e2e-sign-in",
  // Apart from the browser suite's, which CI uploads alongside.
  outputDir: "./test-results-sign-in",
  reporter: [
    ["list"],
    ["html", { outputFolder: "./playwright-report-sign-in", open: "never" }],
  ],
  timeout: 120_000,
  // One account, one server: the browsers take turns.
  workers: 1,
  use: {
    baseURL: process.env.LIFE_OS_E2E_URL,
    trace: "retain-on-failure",
    timezoneId: "Europe/Berlin",
  },
  projects: [
    {
      name: "first-account",
      testMatch: /first-account\.setup\.ts/,
      use: { browserName: "chromium" },
    },
    ...signIn,
  ],
});
