import { expect, test as setup } from "@playwright/test";

import { seed } from "./demo-data";
import { STATE } from "./paths";

/**
 * A new install's first account, made with the setup code as a stranger
 * makes it (tools/screenshots takes the code from the Dashboard's
 * container), then the demo dataset, written with that account's session.
 */
const code = process.env.LIFE_OS_E2E_SETUP_CODE ?? "";
const password = process.env.LIFE_OS_E2E_PASSWORD ?? "";

setup(
  "the demo account and the demo data",
  async ({ page, context, baseURL, playwright }) => {
    setup.skip(
      !baseURL || !code || !password,
      "tools/screenshots sets the stack, the setup code, and the password",
    );
    await page.goto("/setup");
    await page.getByLabel("Setup code").fill(code);
    await page.getByLabel("Username").fill("demo");
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Password again").fill(password);
    await page.getByRole("button", { name: "Create the account" }).click();
    await expect(page).toHaveURL(/\/settings\/security\?welcome=1$/);
    await context.storageState({ path: STATE });

    // A write with the session cookie needs Life OS's own Origin and, on
    // loopback, Sec-Fetch-Site (the CSRF rule, src/lib/auth/origin.ts), as
    // a page of Life OS sends them.
    const request = await playwright.request.newContext({
      baseURL,
      storageState: STATE,
      extraHTTPHeaders: {
        origin: new URL(baseURL!).origin,
        "sec-fetch-site": "same-origin",
        "x-life-os-actor": "user",
      },
    });
    try {
      await seed(request);
    } finally {
      await request.dispose();
    }
  },
);
