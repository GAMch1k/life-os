import { type APIRequestContext, expect, test } from "@playwright/test";

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
  const item = await request.post("/api/inbox/v1/inbox-items", {
    data: { content: `Home review ${stamp}`, source: "manual" },
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
    await createHumanReview(request, stamp);
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
    await expect(
      page.getByRole("heading", { name: "What needs your attention?" }),
    ).toBeVisible();

    const review = page.getByRole("region", { name: "Human Review" });
    await expect(review).toContainText(/waiting for your decision/);
    await expect(review).toContainText(`Home review ${stamp}`);

    // Nine urgent Tasks exist; Home previews five, overdue first.
    const urgent = page.getByRole("region", { name: "Urgent tasks" });
    await expect(urgent).toContainText(/\d+ overdue · \d+\+? due today/);
    await expect(urgent.getByRole("listitem")).toHaveCount(5);
    await expect(urgent.getByRole("listitem").first()).toContainText(
      `Home overdue ${stamp}`,
    );
    await expect(page.getByText(`Home today ${stamp}-7`)).toHaveCount(0);

    const unscheduled = page.getByRole("region", { name: "Unscheduled tasks" });
    await expect(unscheduled.getByRole("listitem")).toHaveCount(3);

    const calendar = page.getByRole("region", {
      name: "Next on your calendar",
    });
    await expect(calendar).toContainText(`Home next event ${stamp}`);

    await review.getByRole("link", { name: "Review in Inbox" }).click();
    await expect(page).toHaveURL(/\/inbox$/);
    await page.goBack();
    await urgent.getByRole("link", { name: "Open Tasks" }).click();
    await expect(page).toHaveURL(/\/tasks$/);
    await page.goBack();
    await calendar.getByRole("link", { name: "Open Calendar" }).click();
    await expect(page).toHaveURL(/\/calendar$/);
  });
});
