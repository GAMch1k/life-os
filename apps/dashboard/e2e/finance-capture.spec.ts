import { expect, test } from "@playwright/test";

const accountId = process.env.LIFE_OS_E2E_LIVE_FINANCE_ACCOUNT_ID;
const enabled = Boolean(process.env.LIFE_OS_E2E_URL && accountId);

test.describe("Dashboard Finance capture", () => {
  test.skip(
    !enabled,
    "set LIFE_OS_E2E_URL and LIFE_OS_E2E_LIVE_FINANCE_ACCOUNT_ID for deployed acceptance",
  );

  test("captures an explicit permitted transaction in the browser and persists it once", async ({
    page,
    request,
  }) => {
    const note = `P7 live finance acceptance ${Date.now()}`;
    const content = `Record a finance transaction for account ${accountId}: spent 1 USD at ${note} on 2026-10-08.`;
    await page.goto("/");
    await page.locator("#inbox-capture").fill(content);
    await page.getByRole("button", { name: "Capture" }).click();

    const inbox = await request.get(
      "/api/inbox/v1/inbox-items?query=state=raw&limit=100",
    );
    expect(inbox.ok()).toBeTruthy();
    const item = (await inbox.json()).items.find(
      (entry: { content: string }) => entry.content === content,
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
          const transactions = await request.get(
            "/api/finance/v1/transactions?limit=100",
          );
          const data = await transactions.json();
          return data.items?.filter(
            (transaction: { accountId: string; note: string }) =>
              transaction.accountId === accountId && transaction.note === note,
          ).length;
        },
        { timeout: 30_000 },
      )
      .toBe(1);
  });
});
