import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

import { expect, test } from "@playwright/test";

/**
 * Each browser signs in where a stranger first opens Life OS, through an
 * SSH tunnel: http://127.0.0.1 and http://localhost, plain HTTP on this
 * machine's own loopback (playwright.sign-in.config.ts). The session must
 * stick in every engine, WebKit (Safari's) too, which refuses a Secure
 * cookie there: on loopback the session cookie is not Secure
 * (src/lib/auth/origin.ts, isLoopbackLogin).
 */
const username = process.env.LIFE_OS_E2E_USERNAME ?? "owner";
const password = process.env.LIFE_OS_E2E_PASSWORD ?? "";

for (const host of ["127.0.0.1", "localhost"]) {
  test(`signs in at http://${host} and keeps the session`, async ({
    page,
    context,
    baseURL,
  }) => {
    test.skip(
      !baseURL || !password,
      "tools/smoke-fresh-install sets the stack and the password",
    );
    const url = new URL(baseURL!);
    url.hostname = host;
    const base = url.origin;

    await page.goto(`${base}/settings/security`);
    await expect(page).toHaveURL(/\/login\?next=%2Fsettings%2Fsecurity$/);
    await expect(
      page.getByText("Signing in over plain HTTP is off here"),
    ).toHaveCount(0);
    await page.getByLabel("Username").fill(username);
    await page.getByLabel("Password").fill(password);
    // Better Auth allows an address 3 sign-ins in 10 seconds, and every
    // browser here signs in from the same one, the runner's: a 429 waits
    // the window out and signs in again, as a person would.
    for (let attempt = 1; ; attempt += 1) {
      const answer = page.waitForResponse(
        (response) =>
          response.url().endsWith("/api/auth/sign-in/username") &&
          response.request().method() === "POST",
      );
      await page.getByRole("button", { name: "Sign in" }).click();
      if ((await answer).status() !== 429 || attempt === 3) break;
      await page.waitForTimeout(11_000);
    }

    // Settings → Security opens only with a real session: legacy access is
    // off, so a cookie the browser refused would end on the login again.
    const heading = page.getByRole("heading", { name: "Security", level: 1 });
    await expect(page).toHaveURL(`${base}/settings/security`);
    await expect(heading).toBeVisible();
    await page.reload();
    await expect(heading).toBeVisible();
    const cookies = await context.cookies(base);
    const session = cookies.find((cookie) => cookie.name === "lifeos.session");
    expect(
      session,
      `cookies: ${cookies.map((cookie) => cookie.name).join(", ")}`,
    ).toBeTruthy();
    expect(session!.secure).toBe(false);
    expect(cookies.some((cookie) => cookie.name.startsWith("__Host-"))).toBe(
      false,
    );
    // And the services' routes, through the gateway, with the cookie.
    const tasks = await page.request.get(`${base}/api/tasks/v1/tasks?limit=1`);
    expect(tasks.status()).toBe(200);
  });
}

/**
 * Not a check of Life OS: whether this engine keeps a Secure `__Host-`
 * cookie that plain-HTTP loopback sets, as Life OS's session cookie was
 * before. It only reports, in the test's annotations and the log, so that
 * a change in an engine shows (docs/architecture/multi-user.md#browser-security).
 */
test("reports whether a Secure cookie survives plain-HTTP loopback", async ({
  page,
  context,
  browserName,
}) => {
  const server = createServer((_, response) => {
    response.setHeader(
      "set-cookie",
      "__Host-probe=1; Secure; Path=/; SameSite=Lax; HttpOnly",
    );
    response.setHeader("content-type", "text/html");
    response.end("<p>probe</p>");
  });
  await new Promise<void>((resolve) =>
    server.listen(0, "127.0.0.1", () => resolve()),
  );
  try {
    const { port } = server.address() as AddressInfo;
    const probe = `http://127.0.0.1:${port}/`;
    await page.goto(probe);
    const kept = (await context.cookies(probe)).some(
      (cookie) => cookie.name === "__Host-probe",
    );
    const result = kept ? "kept" : "refused";
    test.info().annotations.push({
      type: "secure-cookie-on-plain-http-loopback",
      description: `${browserName}: ${result}`,
    });
    console.log(
      `${browserName} ${result} a Secure __Host- cookie from http://127.0.0.1`,
    );
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
