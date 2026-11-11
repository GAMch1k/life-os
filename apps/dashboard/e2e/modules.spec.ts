import { expect, moduleOn, test } from "./fixtures";

/**
 * The optional modules (docs/architecture/modules.md) as the stack under
 * test runs them: each module that is off leaves no trace in the Dashboard
 * (navigation, Home, Status, capture, Calendar, Finance), and its routes
 * answer 404 module_off at once, at the gateway and at the Dashboard's own
 * proxies; each module that is on shows. CI runs the browser suite twice:
 * with every module the verification stack can run, and with the core alone.
 */
test.describe("Optional modules", () => {
  test("the navigation is the core's, whatever runs", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/");
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    for (const label of [
      "Home",
      "Finance",
      "Tasks",
      "Inbox",
      "Calendar",
      "Food",
      "Activity",
      "Status",
    ])
      await expect(nav.getByRole("link", { name: label })).toBeVisible();
  });

  test("voice notes: the recorder and the Voice check show only with the module", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator("#inbox-capture")).toBeVisible();
    await expect(page.getByRole("button", { name: "Record" })).toHaveCount(
      moduleOn("voice") ? 1 : 0,
    );
    await page.goto("/status");
    await expect(
      page.getByRole("region", { name: "Inbox backlog" }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Voice transcription" }),
    ).toHaveCount(moduleOn("voice") ? 1 : 0);
  });

  test("AI processing: its Status check, and Inbox without it", async ({
    page,
  }) => {
    await page.goto("/status");
    await expect(
      page.getByRole("region", { name: "Inbox backlog" }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Automatic processing" }),
    ).toHaveCount(moduleOn("ai") ? 1 : 0);
    const services = page.getByRole("region", { name: "Services" });
    await expect(services).toContainText("Tasks");
    // A module that is off is not even probed: it is not listed.
    for (const [module, name] of [
      ["voice", "Voice"],
      ["banking", "Banking"],
      ["google-calendar", "Google Calendar"],
      ["server-health", "Host health"],
    ] as const) {
      if (!moduleOn(module))
        await expect(services.getByText(name, { exact: true })).toHaveCount(0);
    }
  });

  test("bank sync, Google Calendar, and server health show only with their modules", async ({
    page,
  }) => {
    await page.goto("/finance");
    await expect(
      page.getByRole("heading", { name: "Finance", level: 1 }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Bank connection" }),
    ).toHaveCount(moduleOn("banking") ? 1 : 0);
    await page.goto("/calendar");
    await expect(
      page.getByRole("button", { name: "Reminder defaults" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Google Calendar" }),
    ).toHaveCount(moduleOn("google-calendar") ? 1 : 0);
    await page.goto("/status");
    await expect(page.getByRole("region", { name: "Server" })).toHaveCount(
      moduleOn("server-health") ? 1 : 0,
    );
  });

  test("a module that is off answers 404 module_off at once; one that is on answers", async ({
    request,
  }) => {
    for (const [module, path] of [
      ["voice", "/api/voice/ready"],
      ["ai", "/api/mcp/ready"],
      ["banking", "/api/integrations/banking/ready"],
      ["google-calendar", "/api/integrations/google-calendar/ready"],
    ] as const) {
      const started = Date.now();
      const response = await request.get(path);
      if (moduleOn(module)) {
        expect(response.status(), path).not.toBe(404);
      } else {
        expect(response.status(), path).toBe(404);
        expect((await response.json()).error.code, path).toBe("module_off");
        expect(Date.now() - started, path).toBeLessThan(2_000);
      }
    }
    // The Dashboard's own proxy to the processor, and the modules' pages.
    const ideas = await request.get("/api/processor/ideas");
    if (!moduleOn("ai")) {
      expect(ideas.status()).toBe(404);
      expect((await ideas.json()).error.code).toBe("module_off");
    }
    for (const [module, path] of [
      ["banking", "/finance/bank/callback"],
      ["google-calendar", "/calendar/google/callback"],
    ] as const) {
      if (!moduleOn(module))
        expect((await request.get(path)).status(), path).toBe(404);
    }
  });
});
