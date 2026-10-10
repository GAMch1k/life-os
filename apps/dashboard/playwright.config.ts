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

export default defineConfig({
  testDir: "./e2e",
  timeout: 180_000,
  use: { baseURL: process.env.LIFE_OS_E2E_URL, trace: "retain-on-failure" },
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
