import { expect, test } from "@playwright/test";

const accountId = process.env.LIFE_OS_E2E_LIVE_FINANCE_ACCOUNT_ID;
const enabled = Boolean(process.env.LIFE_OS_E2E_URL && accountId);

type Transaction = {
  accountId: string;
  counterparty: string | null;
  description: string | null;
  notes: string | null;
};

test.describe("Dashboard Finance capture", () => {
  test.skip(
    !enabled,
    "set LIFE_OS_E2E_URL and LIFE_OS_E2E_LIVE_FINANCE_ACCOUNT_ID for deployed acceptance",
  );

  test("captures an explicit permitted transaction in the browser and persists it once", async ({
    page,
    request,
  }) => {
    const stamp = `P7-${Date.now()}`;
    const note = `P7 live finance acceptance ${stamp}`;
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

    // The AI may put the text in the counterparty, description, or notes;
    // Finance's search looks in all three.
    await expect
      .poll(
        async () => {
          const transactions = await request.get(
            `/api/finance/v1/transactions?accountId=${accountId}&q=${encodeURIComponent(stamp)}&limit=100`,
          );
          const data = await transactions.json();
          return data.items?.filter((transaction: Transaction) =>
            [
              transaction.counterparty,
              transaction.description,
              transaction.notes,
            ]
              .join(" ")
              .toLocaleLowerCase()
              .includes(stamp.toLocaleLowerCase()),
          ).length;
        },
        { timeout: 30_000 },
      )
      .toBe(1);

    // The Finance workspace finds it too.
    await page.goto("/finance");
    await page.getByLabel("Search transactions").fill(stamp);
    await expect(page.getByTestId("finance-transaction")).toHaveCount(1);
  });
});
