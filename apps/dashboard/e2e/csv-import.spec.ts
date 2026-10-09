import { expect, test } from "@playwright/test";

test.describe("Dashboard CSV import", () => {
  test.skip(
    !process.env.LIFE_OS_E2E_URL || !process.env.LIFE_OS_E2E_ACCOUNT_ID,
    "requires isolated gateway and seeded account",
  );

  test("uploads through the browser file chooser and reports persisted import counts", async ({
    page,
    request,
  }) => {
    const externalId = `browser-csv-${Date.now()}`;
    const csv = [
      "account_id,occurred_on,amount_minor,currency,external_id,note",
      `${process.env.LIFE_OS_E2E_ACCOUNT_ID},2026-10-08,-125,USD,${externalId},Browser CSV`,
    ].join("\n");
    await page.goto("/finance");
    await page.locator('input[type="file"]').setInputFiles({
      name: "browser-import.csv",
      mimeType: "text/csv",
      buffer: Buffer.from(csv),
    });
    await expect(page.getByRole("status")).toContainText(
      "Imported 1; skipped 0; failed 0.",
    );
    const response = await request.get(
      `/api/finance/v1/transactions?limit=100`,
    );
    expect(response.ok()).toBeTruthy();
    const data = await response.json();
    expect(
      data.items.some((item: { note: string }) => item.note === "Browser CSV"),
    ).toBeTruthy();
  });
});
