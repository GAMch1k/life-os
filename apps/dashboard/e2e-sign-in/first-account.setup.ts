import { expect, test as setup } from "@playwright/test";

/**
 * The first account on a new install (playwright.sign-in.config.ts): legacy
 * access is off, so every page goes to the login, which points to the
 * setup page; the setup code from the server (tools/smoke-fresh-install
 * prints it as ./setup.sh setup-code does) creates the owner, at the
 * address an SSH tunnel gives.
 */
const code = process.env.LIFE_OS_E2E_SETUP_CODE ?? "";
const username = process.env.LIFE_OS_E2E_USERNAME ?? "owner";
const password = process.env.LIFE_OS_E2E_PASSWORD ?? "";

setup(
  "the first account, with the setup code, at the SSH tunnel's address",
  async ({ page, context, baseURL }) => {
    setup.skip(
      !baseURL || !code || !password,
      "tools/smoke-fresh-install sets the stack, the setup code, and the password",
    );
    // Without a sign-in, a page goes to the login, which has no account yet.
    await page.goto("/");
    await expect(page).toHaveURL(/\/login\?next=/);
    await expect(page.getByText("No account yet")).toBeVisible();
    await page.getByRole("link", { name: "Set up the first account" }).click();
    await expect(page).toHaveURL(/\/setup$/);
    // Loopback signs in over plain HTTP: the form works, with no notice.
    await expect(
      page.getByText("Signing in over plain HTTP is off here"),
    ).toHaveCount(0);
    await page.getByLabel("Setup code").fill(code);
    await page.getByLabel("Username").fill(username);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Password again").fill(password);
    await page.getByRole("button", { name: "Create the account" }).click();
    await expect(page).toHaveURL(/\/settings\/security\?welcome=1$/);
    await expect(
      page.getByRole("heading", { name: "Security", level: 1 }),
    ).toBeVisible();
    await expect(page.getByText("Your account is ready")).toBeVisible();
    // The session cookie of loopback: host-only, HttpOnly, without Secure.
    const cookies = await context.cookies();
    const session = cookies.find((cookie) => cookie.name === "lifeos.session");
    expect(
      session,
      `cookies: ${cookies.map((cookie) => cookie.name).join(", ")}`,
    ).toBeTruthy();
    expect(session!.secure).toBe(false);
    expect(session!.httpOnly).toBe(true);
    expect(session!.sameSite).toBe("Lax");
  },
);
