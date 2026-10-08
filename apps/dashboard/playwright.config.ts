import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 180_000,
  use: { baseURL: process.env.LIFE_OS_E2E_URL, trace: "retain-on-failure" },
});
