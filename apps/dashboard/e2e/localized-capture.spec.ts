import { expect, test } from "@playwright/test";

const enabled = Boolean(
  process.env.LIFE_OS_E2E_URL && process.env.LIFE_OS_E2E_LIVE_LOCALIZED,
);

test.describe("Dashboard localized text capture", () => {
  test.skip(
    !enabled,
    "set LIFE_OS_E2E_URL and LIFE_OS_E2E_LIVE_LOCALIZED for deployed acceptance",
  );
  test.setTimeout(240_000);

  for (const [locale, instruction] of [
    ["Russian", (title: string) => `Создай задачу с названием ${title}.`],
    [
      "German",
      (title: string) => `Erstelle eine Aufgabe mit dem Titel ${title}.`,
    ],
  ] as const) {
    test(`${locale} capture reaches a persisted Task through the automatic pipeline`, async ({
      page,
      request,
    }) => {
      const title = `P7 ${locale} acceptance ${Date.now()}`;
      const content = instruction(title);
      await page.goto("/");
      await page.locator("#inbox-capture").fill(content);
      const created = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.url().includes("/api/inbox/v1/inbox-items"),
      );
      await page.getByRole("button", { name: "Capture" }).click();
      const response = await created;
      expect(response.status()).toBe(201);
      const item = (await response.json()) as { id: string; content: string };
      expect(item.content).toBe(content);

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
              entry.title.includes(title),
            );
          },
          { timeout: 30_000 },
        )
        .toBeTruthy();
    });
  }
});
