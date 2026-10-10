import {
  type APIRequestContext,
  expect,
  type Page,
  test,
} from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

async function createTask(
  request: APIRequestContext,
  title: string,
  dueAt?: string,
) {
  const response = await request.post("/api/tasks/v1/tasks", {
    data: dueAt ? { title, dueAt } : { title },
  });
  expect(response.status()).toBe(201);
  return response.json();
}

async function createHumanReview(request: APIRequestContext, stamp: number) {
  const content = `Home review ${stamp}`;
  const item = await request.post("/api/inbox/v1/inbox-items", {
    data: { content, source: "manual" },
  });
  expect(item.status()).toBe(201);
  const inbox = await item.json();
  const attempt = await request.post(
    `/api/inbox/v1/inbox-items/${inbox.id}/processing-attempts`,
    {
      data: {
        idempotencyKey: `home-review-${stamp}`,
        agentName: "e2e",
        modelProfile: "e2e",
      },
    },
  );
  expect(attempt.status()).toBe(201);
  const started = await attempt.json();
  const candidate = await createTask(request, `Home candidate ${stamp}`);
  const review = await request.post(
    `/api/inbox/v1/inbox-items/${inbox.id}/human-review`,
    {
      data: {
        attemptId: started.id,
        proposedAction: {
          tool: "task_complete",
          arguments: { taskQuery: `Home ${stamp}` },
        },
        candidates: [{ id: candidate.id, title: candidate.title }],
        errorSummary: "ambiguous Task selection",
      },
    },
  );
  expect(review.ok()).toBeTruthy();
  return { id: inbox.id as string, content };
}

/** How far the page scrolls sideways; 0 means nothing overflows. */
function horizontalOverflow(page: Page) {
  return page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
}

test.describe("Home attention surface", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("keeps large datasets compact and links to the dedicated workspaces", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const startOfDay = new Date();
    startOfDay.setUTCHours(0, 0, 0, 0);
    const yesterday = new Date(startOfDay.getTime() - 12 * 3_600_000);
    const today = new Date(startOfDay.getTime() + 12 * 3_600_000);

    await createTask(request, `Home overdue ${stamp}`, yesterday.toISOString());
    for (let index = 0; index < 8; index++) {
      await createTask(
        request,
        `Home today ${stamp}-${index}`,
        today.toISOString(),
      );
    }
    for (let index = 0; index < 5; index++) {
      await createTask(request, `Home undated ${stamp}-${index}`);
    }
    const reviewItem = await createHumanReview(request, stamp);
    const eventStart = new Date(Date.now() + 10 * 60_000);
    const event = await request.post("/api/calendar/v1/events", {
      data: {
        title: `Home next event ${stamp}`,
        allDay: false,
        startAt: eventStart.toISOString(),
        endAt: new Date(eventStart.getTime() + 30 * 60_000).toISOString(),
      },
    });
    expect(event.status()).toBe(201);

    await page.goto("/");
    await expect(page.locator("#inbox-capture")).toBeVisible();

    // Other specs share this stack, so assert caps and ordering rather than
    // exactly which rows are shown.
    const review = page.getByRole("region", { name: "Human Review" });
    await expect(review).toContainText(/waiting for your decision/);
    expect(await review.getByRole("listitem").count()).toBeLessThanOrEqual(2);

    // At least nine urgent Tasks exist; Home previews five, overdue first.
    const urgent = page.getByRole("region", { name: "Urgent tasks" });
    await expect(urgent).toContainText(/[1-9]\d* overdue · \d+\+? due today/);
    await expect(urgent.getByRole("listitem")).toHaveCount(5);
    await expect(urgent.getByRole("listitem").first()).toContainText("Overdue");
    expect(
      await page.getByText(`Home today ${stamp}-`).count(),
    ).toBeLessThanOrEqual(4);

    const unscheduled = page.getByRole("region", { name: "Unscheduled tasks" });
    await expect(unscheduled.getByRole("listitem")).toHaveCount(3);

    const calendar = page.getByRole("region", {
      name: "Next on your calendar",
    });
    await expect(calendar).toContainText(/Home next event \d+/);

    await review.getByRole("link", { name: "Review in Inbox" }).click();
    await expect(page).toHaveURL(/\/inbox$/);
    await page.goBack();
    await urgent.getByRole("link", { name: "Open Tasks" }).click();
    await expect(page).toHaveURL(/\/tasks$/);
    await page.goBack();
    await calendar.getByRole("link", { name: "Open Calendar" }).click();
    await expect(page).toHaveURL(/\/calendar$/);
    await request.post(`/api/inbox/v1/inbox-items/${reviewItem.id}/discard`);
  });

  test("capture comes first and fits desktop and phone screens", async ({
    page,
  }) => {
    for (const viewport of [
      { width: 1280, height: 800 },
      { width: 390, height: 844 },
      { width: 360, height: 740 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/");
      // Type or dictate right away: composer, Capture and Record are all on
      // screen without scrolling.
      for (const control of [
        page.locator("#inbox-capture"),
        page.getByRole("button", { name: "Capture" }),
        page.getByRole("button", { name: "Record" }),
      ]) {
        await expect(control).toBeInViewport({ ratio: 1 });
      }
      const capture = await page.locator("#capture").boundingBox();
      const review = await page.locator("#attention-review").boundingBox();
      expect(capture!.y).toBeLessThan(review!.y);
      expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);

      await page.goto("/inbox");
      await expect(page.locator("#inbox-capture")).toBeInViewport();
      expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
    }
  });

  test("a Human Review capture is counted once and never as pending", async ({
    page,
    request,
  }) => {
    const { id, content } = await createHumanReview(request, Date.now());
    try {
      await page.goto("/inbox");
      // Listed exactly once on /inbox, in Human Review (not Pending/Failed).
      await expect(
        page.locator("#human-review").getByText(content, { exact: true }),
      ).toHaveCount(1);
      await expect(page.getByText(content, { exact: true })).toHaveCount(1);

      // Home counts it once, in Human Review, with Inbox's true count; the
      // capture card counts only automatic work (raw or processing).
      await expect(async () => {
        const backlog = await (
          await request.get("/api/inbox/v1/backlog")
        ).json();
        await page.goto("/");
        await expect(page.locator("#attention-review")).toContainText(
          new RegExp(
            `(^|\\D)${backlog.humanReviewCount} captures? waiting for your decision`,
          ),
          { timeout: 2_000 },
        );
        const capture = page.locator("#capture");
        if (backlog.pendingCount > 0) {
          await expect(capture).toContainText(
            new RegExp(`(^|\\D)${backlog.pendingCount} pending`),
            { timeout: 2_000 },
          );
        } else {
          await expect(capture).not.toContainText("pending", {
            timeout: 2_000,
          });
        }
      }).toPass({ timeout: 30_000 });
      await expect(page.locator("#capture")).not.toContainText(content);
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${id}/discard`);
    }
  });
});
