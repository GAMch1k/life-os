import { test as base } from "@playwright/test";

export * from "@playwright/test";

/**
 * A spec's own API calls (setup, cleanup, checks) are development-tool
 * writes, recorded as `dev:e2e` and never as the owner. Browser pages keep the
 * Dashboard's own `user` actor: `extraHTTPHeaders` in the config would also
 * apply to page requests and override the header the page sets, so only the
 * `request` fixture changes. Specs that write through `request` import `test`
 * from here.
 */
export const test = base.extend({
  request: async ({ playwright }, provide) => {
    const request = await playwright.request.newContext({
      extraHTTPHeaders: { "X-Life-OS-Actor": "dev:e2e" },
    });
    await provide(request);
    await request.dispose();
  },
});
