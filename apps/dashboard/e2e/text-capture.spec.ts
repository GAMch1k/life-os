import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

const isCapturePost = (method: string, url: string) =>
  method === "POST" && url.includes("/api/inbox/v1/inbox-items");

test.describe("Dashboard capture → automatic processor", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("persists a browser capture and automatically creates a Task @codex", async ({
    page,
    request,
  }) => {
    const title = `P7 browser capture ${Date.now()}`;
    await page.goto("/");
    await page.locator("#inbox-capture").fill(`Create a task titled ${title}.`);
    const created = page.waitForResponse((response) =>
      isCapturePost(response.request().method(), response.url()),
    );
    await page.getByRole("button", { name: "Capture" }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const item = (await response.json()) as { id: string };
    // The capture is listed under the composer with its live state.
    await expect(page.getByText(title)).toBeVisible();

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
    // The row under the composer follows the capture to its outcome.
    await expect(
      page
        .getByRole("list", { name: "Just captured" })
        .getByRole("listitem")
        .filter({ hasText: title }),
    ).toContainText("Processed", { timeout: 30_000 });
  });

  test("Enter captures exactly once; Shift+Enter adds a line instead", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const first = `Create a task titled E2E composer ${stamp}.`;
    const second = `It has no due date.`;
    const content = `${first}\n${second}`;
    const posts: string[] = [];
    page.on("request", (sent) => {
      if (isCapturePost(sent.method(), sent.url())) {
        posts.push(sent.postData() ?? "");
      }
    });

    await page.goto("/");
    const composer = page.locator("#inbox-capture");
    await composer.click();
    await composer.pressSequentially(first);
    await composer.press("Shift+Enter");
    await composer.pressSequentially(second);
    await expect(composer).toHaveValue(content);
    expect(posts).toHaveLength(0);

    const created = page.waitForResponse((response) =>
      isCapturePost(response.request().method(), response.url()),
    );
    await composer.press("Enter");
    expect((await created).status()).toBe(201);
    await expect(composer).toHaveValue("");
    await expect(composer).toBeFocused();
    await expect(
      page.locator("#capture").getByText("Captured.", { exact: true }),
    ).toBeVisible();

    // Enter on the now-empty composer captures nothing more.
    await composer.press("Enter");
    await page.waitForTimeout(1_000);
    expect(posts).toHaveLength(1);

    const list = await (
      await request.get("/api/inbox/v1/inbox-items?limit=100")
    ).json();
    expect(
      list.items.filter(
        (item: { content: string }) => item.content === content,
      ),
    ).toHaveLength(1);
    await expect(page.locator("#capture").getByText(first)).toBeVisible();
  });
});
