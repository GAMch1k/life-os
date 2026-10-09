import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

test.describe("Home attention surface", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test("keeps large task sets compact and links to their workspaces", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const dueAt = new Date().toISOString();
    for (let index = 0; index < 6; index++) {
      const response = await request.post("/api/tasks/v1/tasks", {
        data: { title: `P8 home urgent ${stamp}-${index}`, dueAt },
      });
      expect(response.status()).toBe(201);
    }
    await page.goto("/");
    await expect(
      page.getByRole("heading", { name: "What needs your attention?" }),
    ).toBeVisible();
    await expect(page.getByText(`P8 home urgent ${stamp}-0`)).toBeVisible();
    await expect(page.getByText(`P8 home urgent ${stamp}-5`)).not.toBeVisible();
    await expect(
      page.getByRole("link", { name: "Open workspace" }).first(),
    ).toBeVisible();
    await expect(page.locator('a[href="/tasks"]')).toHaveCount(2);
  });
});
