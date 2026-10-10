import { type APIRequestContext, expect, type Page, test } from "./fixtures";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Created = { id: string; title: string; source: string };

/**
 * Sends a notification as the spec (`dev:e2e`, so its source is "dev").
 * Notifications has no delete, so every title is unique to this run.
 */
async function notify(
  request: APIRequestContext,
  data: {
    title: string;
    importance: "low" | "medium" | "high";
    body?: string;
    link?: string;
  },
): Promise<Created> {
  const response = await request.post("/api/notifications/v1/notifications", {
    data: {
      category: "test.e2e",
      dedupeKey: `test.e2e:${data.title}`,
      ...data,
    },
  });
  expect(response.status()).toBe(201);
  return response.json();
}

/** Starts from nothing unread, so the bell's dot is this spec's doing. */
async function readEverything(request: APIRequestContext) {
  const response = await request.post(
    "/api/notifications/v1/notifications/read-all",
    { data: {} },
  );
  expect(response.ok()).toBeTruthy();
}

function bell(page: Page) {
  return page.getByRole("banner").getByRole("button", {
    name: /^Notifications(, \d+ unread)?$/,
  });
}

test.describe("Notifications", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("the bell, its popover, and the notifications page", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const linked = `E2E notification linked ${stamp}`;
    const urgent = `E2E notification urgent ${stamp}`;

    await readEverything(request);
    await notify(request, {
      title: linked,
      importance: "medium",
      body: "Opens Tasks when clicked.",
      link: "/tasks",
    });
    await notify(request, {
      title: urgent,
      importance: "high",
      body: "Has no link; clicking only marks it read.",
    });

    // A dot (no number) while anything is unread, tinted for high
    // importance; the count is in the accessible name.
    await page.goto("/");
    await expect(bell(page)).toHaveAccessibleName("Notifications, 2 unread");
    await expect(bell(page)).toHaveAttribute("data-dot", "high");

    // The popover lists the latest notifications, newest first.
    await bell(page).click();
    const popover = page.getByRole("dialog", { name: "Notifications" });
    const latest = popover.getByRole("list", { name: "Latest notifications" });
    await expect(latest.getByRole("listitem").first()).toContainText(urgent);
    await expect(latest).toContainText(linked);
    await expect(latest).toContainText("Developer tool · High");

    // Without a link, clicking marks it read and the popover stays open.
    await latest.getByRole("button", { name: new RegExp(urgent) }).click();
    await expect(bell(page)).toHaveAccessibleName("Notifications, 1 unread");
    await expect(bell(page)).toHaveAttribute("data-dot", "unread");

    // With a link, clicking marks it read and goes there; nothing is unread.
    await latest.getByRole("link", { name: new RegExp(linked) }).click();
    await expect(page).toHaveURL(/\/tasks$/);
    await expect(popover).toBeHidden();
    await expect(bell(page)).toHaveAccessibleName("Notifications");
    await expect(bell(page)).toHaveAttribute("data-dot", "none");

    // "All notifications" opens the full list.
    await bell(page).click();
    await popover.getByRole("link", { name: "All notifications" }).click();
    await expect(page).toHaveURL(/\/notifications$/);
    await expect(
      page.getByRole("heading", { name: "Notifications", level: 1 }),
    ).toBeVisible();
    const urgentRow = page.getByRole("article", { name: urgent });
    const linkedRow = page.getByRole("article", { name: linked });
    await expect(urgentRow).toBeVisible();
    await expect(linkedRow).toBeVisible();
    await expect(urgentRow).toContainText("High");
    await expect(urgentRow).toContainText("Has no link");
    await expect(urgentRow).toContainText("test.e2e");
    await expect(urgentRow).toContainText(/Shown in Life OS only|ntfy: /);
    await expect(urgentRow.getByText("Unread", { exact: true })).toHaveCount(0);
    await expect(
      linkedRow.getByRole("link", { name: `Open ${linked}` }),
    ).toHaveAttribute("href", "/tasks");
    await expect(
      page.getByRole("heading", { name: "Today", level: 2 }),
    ).toBeVisible();

    // Filters: importance.
    const filters = page.getByRole("form", { name: "Filter notifications" });
    await filters
      .getByRole("combobox", { name: "Importance" })
      .selectOption("high");
    await filters.getByRole("button", { name: "Apply" }).click();
    await expect(page).toHaveURL(/importance=high/);
    await expect(page.getByRole("article", { name: urgent })).toBeVisible();
    await expect(page.getByRole("article", { name: linked })).toHaveCount(0);

    // Filters: unread only, with a new notification.
    const fresh = `E2E notification fresh ${stamp}`;
    await notify(request, { title: fresh, importance: "low" });
    await page.goto("/notifications");
    await filters.getByRole("checkbox", { name: "Unread only" }).check();
    await filters.getByRole("button", { name: "Apply" }).click();
    await expect(page).toHaveURL(/unread=true/);
    const freshRow = page.getByRole("article", { name: fresh });
    await expect(freshRow).toBeVisible();
    await expect(freshRow.getByText("Unread", { exact: true })).toBeVisible();
    await expect(page.getByRole("article", { name: urgent })).toHaveCount(0);

    // Mark all read: everything shown is read and the bell's dot goes.
    await expect(bell(page)).toHaveAttribute("data-dot", "unread");
    await page.getByRole("button", { name: "Mark all read" }).click();
    await expect(freshRow.getByText("Unread", { exact: true })).toHaveCount(0);
    await expect(page.getByText("All read", { exact: true })).toBeVisible();
    await expect(bell(page)).toHaveAttribute("data-dot", "none");

    // A test notification from the page arrives at the top of the list.
    await page.goto("/notifications");
    const trial = page.getByRole("region", { name: "Try notifications" });
    await trial
      .getByRole("combobox", { name: "Test importance" })
      .selectOption("high");
    await trial.getByRole("button", { name: "Send test notification" }).click();
    await expect(trial.getByRole("status")).toHaveText(
      "Sent “Test notification (high)”.",
    );
    await expect(page.getByRole("article").first()).toHaveAccessibleName(
      "Test notification (high)",
    );
    await expect(page.getByRole("article").first()).toContainText("Unread");
    await expect(bell(page)).toHaveAttribute("data-dot", "high");

    const newest = await request.get(
      "/api/notifications/v1/notifications?limit=1",
    );
    const [sent] = (await newest.json()).items;
    expect(sent).toMatchObject({
      title: "Test notification (high)",
      importance: "high",
      category: "test.manual",
      source: "dashboard",
      link: "/notifications",
      readAt: null,
    });
  });
});
