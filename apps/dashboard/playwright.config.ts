import { existsSync } from "node:fs";

import { defineConfig, firefox } from "@playwright/test";

// Every spec runs in Chromium. The voice spec also runs in Firefox, whose
// MediaRecorder produces Ogg/Opus, when Playwright's Firefox is installed
// (`npx playwright install firefox`).
const firefoxInstalled = existsSync(firefox.executablePath());
if (!firefoxInstalled && !process.env.TEST_WORKER_INDEX) {
  console.warn(
    "Playwright Firefox is not installed; skipping the Firefox voice capture project.",
  );
}

// Tests tagged @codex in their title wait for the stack's processor to act
// with the authenticated Codex CLI. CI has no Codex sign-in and runs the
// others: tools/browser-e2e --grep-invert @codex (docs/operations/ci.md).
export default defineConfig({
  testDir: "./e2e",
  timeout: 180_000,
  use: {
    baseURL: process.env.LIFE_OS_E2E_URL,
    trace: "retain-on-failure",
    // The owner's browser zone, matching the stack's LIFE_OS_TIMEZONE, so a
    // first page load is not re-rendered for the browser's zone mid-test.
    // Specs that test other zones set their own timezoneId.
    timezoneId: "Europe/Berlin",
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    ...(firefoxInstalled
      ? [
          {
            name: "firefox",
            use: { browserName: "firefox" as const },
            testMatch: /voice-capture\.spec\.ts/,
          },
        ]
      : []),
  ],
});
