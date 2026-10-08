import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

test.describe("Dashboard capture → automatic processor", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("persists a browser capture and automatically creates a Task", async ({
    page,
    request,
  }) => {
    const title = `P7 browser capture ${Date.now()}`;
    await page.goto("/");
    await page.locator("#inbox-capture").fill(`Create a task titled ${title}.`);
    await page.getByRole("button", { name: "Capture" }).click();
    await expect(page.getByText(title)).toBeVisible();

    const inbox = await request.get(
      "/api/inbox/v1/inbox-items?query=state=raw&limit=100",
    );
    expect(inbox.ok()).toBeTruthy();
    const item = (await inbox.json()).items.find((entry: { content: string }) =>
      entry.content.includes(title),
    );
    expect(item).toBeTruthy();

    await expect
      .poll(
        async () => {
          const attempts = await request.get(
            `/api/inbox/v1/inbox-items/${item.id}/processing-attempts`,
          );
          const data = await attempts.json();
          return data.items?.[0]?.status;
        },
        { timeout: 150_000 },
      )
      .toBe("processed");

    await expect
      .poll(
        async () => {
          const tasks = await request.get(
            `/api/tasks/v1/tasks?query=${encodeURIComponent(title)}`,
          );
          const data = await tasks.json();
          return data.items?.some((entry: { title: string }) =>
            entry.title.startsWith(title),
          );
        },
        { timeout: 30_000 },
      )
      .toBeTruthy();
  });
});
