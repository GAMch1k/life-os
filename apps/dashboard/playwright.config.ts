import { defineConfig } from "@playwright/test";

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
});
