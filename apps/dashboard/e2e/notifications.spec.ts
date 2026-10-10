import { type APIRequestContext, expect, type Page, test } from "./fixtures";
import { captureInReview, unactionable } from "./review-setup";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Created = { id: string; title: string; source: string };
type Listed = {
  id: string;
  title: string;
  body?: string;
  importance: string;
  category: string;
  source: string;
  link?: string;
  dedupeKey: string;
  readAt: string | null;
};

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

/** Starts from nothing unread. */
async function readEverything(request: APIRequestContext) {
  const response = await request.post(
    "/api/notifications/v1/notifications/read-all",
    { data: {} },
  );
  expect(response.ok()).toBeTruthy();
}

/**
 * Marks read every unread notification but the spec's `own`. Specs running
 * alongside make Inbox notify about Human Review at any time, so a step that
 * expects the bell's dot to go first clears what is not this spec's.
 */
async function readAllBut(request: APIRequestContext, own: string[]) {
  const response = await request.get(
    "/api/notifications/v1/notifications?unread=true&limit=100",
  );
  expect(response.ok()).toBeTruthy();
  const { items } = (await response.json()) as { items: Listed[] };
  for (const item of items) {
    if (own.includes(item.id)) continue;
    const read = await request.post(
      `/api/notifications/v1/notifications/${item.id}/read`,
    );
    expect(read.ok()).toBeTruthy();
  }
}

/** Whether the notification `id` is marked read. */
async function isRead(request: APIRequestContext, id: string) {
  const response = await request.get(
    `/api/notifications/v1/notifications/${id}`,
  );
  expect(response.ok()).toBeTruthy();
  return ((await response.json()) as Listed).readAt !== null;
}

function bell(page: Page) {
  return page.getByRole("banner").getByRole("button", {
    name: /^Notifications(, \d+ unread)?$/,
  });
}

test.describe("Notifications", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  // Specs running alongside add notifications at any time (Inbox notifies
  // about every capture sent to Human Review), so this spec asserts on its
  // own notifications and never on an exact unread count.
  test("the bell, its popover, and the notifications page", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const linked = `E2E notification linked ${stamp}`;
    const urgent = `E2E notification urgent ${stamp}`;

    await readEverything(request);
    const linkedOne = await notify(request, {
      title: linked,
      importance: "medium",
      body: "Opens Tasks when clicked.",
      link: "/tasks",
    });
    const urgentOne = await notify(request, {
      title: urgent,
      importance: "high",
      body: "Has no link; clicking only marks it read.",
    });

    // A dot (no number) while anything is unread, tinted for high
    // importance; the count is in the accessible name.
    await page.goto("/");
    await expect(bell(page)).toHaveAccessibleName(
      /^Notifications, \d+ unread$/,
    );
    await expect(bell(page)).toHaveAttribute("data-dot", "high");

    // The popover lists the latest notifications, newest first.
    await bell(page).click();
    const popover = page.getByRole("dialog", { name: "Notifications" });
    const latest = popover.getByRole("list", { name: "Latest notifications" });
    await expect(latest).toContainText(urgent);
    await expect(latest).toContainText(linked);
    const shown = await latest.getByRole("listitem").allTextContents();
    expect(shown.findIndex((text) => text.includes(urgent))).toBeLessThan(
      shown.findIndex((text) => text.includes(linked)),
    );
    await expect(
      latest.getByRole("listitem").filter({ hasText: urgent }),
    ).toContainText("Developer tool · High");

    // Without a link, clicking marks it read and the popover stays open.
    // The dot loses its tint: what is still unread (the linked one, and any
    // Inbox notification) is not high.
    await latest.getByRole("button", { name: new RegExp(urgent) }).click();
    await expect.poll(() => isRead(request, urgentOne.id)).toBe(true);
    await expect(bell(page)).toHaveAttribute("data-dot", "unread");
    await expect(bell(page)).toHaveAccessibleName(
      /^Notifications, \d+ unread$/,
    );
    await expect(popover).toBeVisible();

    // With a link, clicking marks it read and goes there. With everything
    // else read just before, nothing is unread and the dot goes.
    await readAllBut(request, [linkedOne.id]);
    await latest.getByRole("link", { name: new RegExp(linked) }).click();
    await expect(page).toHaveURL(/\/tasks$/);
    await expect(popover).toBeHidden();
    await expect.poll(() => isRead(request, linkedOne.id)).toBe(true);
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
    const freshOne = await notify(request, { title: fresh, importance: "low" });
    await page.goto("/notifications");
    await filters.getByRole("checkbox", { name: "Unread only" }).check();
    await filters.getByRole("button", { name: "Apply" }).click();
    await expect(page).toHaveURL(/unread=true/);
    const freshRow = page.getByRole("article", { name: fresh });
    await expect(freshRow).toBeVisible();
    await expect(freshRow.getByText("Unread", { exact: true })).toBeVisible();
    await expect(page.getByRole("article", { name: urgent })).toHaveCount(0);

    // Mark all read: everything shown is read and, with what arrived since
    // the page was shown read just before, the bell's dot goes.
    await expect(bell(page)).toHaveAttribute("data-dot", "unread");
    await readAllBut(request, [freshOne.id]);
    await page.getByRole("button", { name: "Mark all read" }).click();
    await expect(freshRow.getByText("Unread", { exact: true })).toHaveCount(0);
    await expect(page.getByText("All read", { exact: true })).toBeVisible();
    await expect(bell(page)).toHaveAttribute("data-dot", "none");

    // A test notification from the page arrives in the list, unread.
    await page.goto("/notifications");
    const trial = page.getByRole("region", { name: "Try notifications" });
    await trial
      .getByRole("combobox", { name: "Test importance" })
      .selectOption("high");
    await trial.getByRole("button", { name: "Send test notification" }).click();
    await expect(trial.getByRole("status")).toHaveText(
      "Sent “Test notification (high)”.",
    );
    // Every earlier test notification is read by now, so the newest one
    // with this title is the one just sent; other notifications may have
    // arrived above it.
    await expect(
      page.getByRole("article", { name: "Test notification (high)" }).first(),
    ).toContainText("Unread");
    await expect(bell(page)).toHaveAttribute("data-dot", "high");

    const newest = await request.get(
      "/api/notifications/v1/notifications?category=test.manual&limit=1",
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

  test("a capture waiting in Human Review notifies the owner", async ({
    request,
  }) => {
    const content = unactionable(Date.now(), "notification ");
    const itemId = await captureInReview(request, content);

    // Inbox sends it in the background right after storing the review. The
    // processor may also have sent the capture to review: any round counts.
    let sent: Listed | undefined;
    await expect
      .poll(async () => {
        const response = await request.get(
          "/api/notifications/v1/notifications?category=inbox.human_review&limit=100",
        );
        expect(response.ok()).toBeTruthy();
        const { items } = (await response.json()) as { items: Listed[] };
        sent = items.find((item) =>
          item.dedupeKey.startsWith(`inbox.human_review:${itemId}:`),
        );
        return Boolean(sent);
      })
      .toBe(true);
    expect(sent).toMatchObject({
      title: "Needs your review",
      importance: "medium",
      category: "inbox.human_review",
      source: "inbox",
      link: "/inbox#human-review",
    });
    expect(sent?.body).toContain(content);
    expect(sent?.body).toContain("Nothing safe to do with this capture.");

    // Leave nothing behind for the other specs.
    expect(
      (
        await request.post(
          `/api/notifications/v1/notifications/${sent?.id}/read`,
        )
      ).ok(),
    ).toBeTruthy();
    expect(
      (await request.post(`/api/inbox/v1/inbox-items/${itemId}/discard`)).ok(),
    ).toBeTruthy();
  });
});
