import { test as base } from "@playwright/test";

import { MODULES } from "../src/config/modules.generated";

export * from "@playwright/test";

const OPTIONAL: ReadonlySet<string> = new Set(
  MODULES.filter((entry) => entry.install === "optional").map(
    (entry) => entry.id,
  ),
);

/**
 * Whether the stack under test runs an optional module
 * (docs/architecture/modules.md): LIFE_OS_E2E_MODULES, which
 * tools/browser-e2e reads from the Dashboard's LIFE_OS_MODULES. Unset, every
 * module runs, as on the verification stack by default.
 */
export function moduleOn(id: string): boolean {
  const modules = process.env.LIFE_OS_E2E_MODULES;
  if (modules === undefined) return true;
  return modules
    .split(",")
    .map((name) => name.trim())
    .includes(id);
}

/**
 * A spec's own API calls (setup, cleanup, checks) are development-tool
 * writes, recorded as `dev:e2e` and never as the owner. Browser pages keep the
 * Dashboard's own `user` actor: `extraHTTPHeaders` in the config would also
 * apply to page requests and override the header the page sets, so only the
 * `request` fixture changes. Specs that write through `request` import `test`
 * from here.
 */
export const test = base.extend<{ modules: void }>({
  // A test tagged with an optional module (@voice, @ai, …) is skipped while
  // that module is off: CI's core-only run leaves them out.
  modules: [
    async ({}, provide, testInfo) => {
      const off = testInfo.tags
        .map((tag) => tag.slice(1))
        .filter((id) => OPTIONAL.has(id) && !moduleOn(id));
      testInfo.skip(off.length > 0, `the ${off.join(", ")} module is off`);
      await provide();
    },
    { auto: true },
  ],
  request: async ({ playwright, baseURL }, provide) => {
    const request = await playwright.request.newContext({
      baseURL,
      extraHTTPHeaders: { "X-Life-OS-Actor": "dev:e2e" },
    });
    await provide(request);
    await request.dispose();
  },
});
