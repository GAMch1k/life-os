import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

test.describe("Human Review queue", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("shows retained candidates and submits a chosen Task", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const item = await request.post("/api/inbox/v1/inbox-items", {
      data: { content: `Complete P8 review ${stamp}`, source: "manual" },
    });
    expect(item.status()).toBe(201);
    const inbox = await item.json();
    const attempt = await request.post(
      `/api/inbox/v1/inbox-items/${inbox.id}/processing-attempts`,
      {
        data: {
          idempotencyKey: `review-${stamp}`,
          agentName: "e2e",
          modelProfile: "e2e",
        },
      },
    );
    expect(attempt.status()).toBe(201);
    const started = await attempt.json();
    const candidate = await request.post("/api/tasks/v1/tasks", {
      data: { title: `P8 review candidate ${stamp}` },
    });
    expect(candidate.status()).toBe(201);
    const task = await candidate.json();
    const review = await request.post(
      `/api/inbox/v1/inbox-items/${inbox.id}/human-review`,
      {
        data: {
          attemptId: started.id,
          proposedAction: {
            tool: "task_complete",
            arguments: { taskQuery: `P8 review ${stamp}` },
          },
          candidates: [{ id: task.id, title: task.title }],
          errorSummary: "ambiguous Task selection",
        },
      },
    );
    expect(review.ok()).toBeTruthy();

    await page.goto("/inbox");
    await expect(page.getByText("Human Review", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: task.title }).click();
    await expect(
      page.getByText("Human Review", { exact: true }),
    ).not.toBeVisible();
    await expect
      .poll(
        async () =>
          (
            await (
              await request.get(`/api/inbox/v1/inbox-items/${inbox.id}`)
            ).json()
          ).state !== "human_review",
      )
      .toBe(true);
  });
});
