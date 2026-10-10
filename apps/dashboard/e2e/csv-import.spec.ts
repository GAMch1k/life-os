import { type APIRequestContext, expect, type Page, test } from "./fixtures";

// The Finance CSV import: the owner picks the account in the import dialog,
// chooses a file, and imports it. Each test makes its own account (accounts
// can't be deleted; it is archived afterwards) and checks what Finance
// stored through the API.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Account = { id: string; name: string };
type Transaction = {
  id: string;
  accountId: string;
  amount: { amount: number; currency: string };
  bookingDate: string;
  counterparty: string | null;
  description: string | null;
  notes: string | null;
  source: { kind: string };
  readOnly: boolean;
};

async function createAccount(request: APIRequestContext, name: string) {
  const response = await request.post("/api/finance/v1/accounts", {
    data: { name, kind: "bank", currency: "EUR" },
  });
  expect(response.status()).toBe(201);
  return (await response.json()) as Account;
}

async function archiveAccount(request: APIRequestContext, account: Account) {
  await request.patch(`/api/finance/v1/accounts/${account.id}`, {
    data: { archived: true },
  });
}

async function accountTransactions(
  request: APIRequestContext,
  account: Account,
) {
  const response = await request.get(
    `/api/finance/v1/transactions?accountId=${account.id}&limit=100`,
  );
  expect(response.ok()).toBeTruthy();
  return ((await response.json()) as { items: Transaction[] }).items;
}

/** Imports `csv` into `account` through the dialog; returns the dialog. */
async function importCsv(page: Page, account: Account, csv: string) {
  await page.getByRole("button", { name: "Import CSV" }).click();
  const dialog = page.getByRole("dialog", { name: "Import CSV" });
  await dialog
    .getByLabel("Account", { exact: true })
    .selectOption({ label: `${account.name} (EUR)` });
  await dialog.locator('input[type="file"]').setInputFiles({
    name: "bank-export.csv",
    mimeType: "text/csv",
    buffer: Buffer.from(csv),
  });
  await expect(dialog.getByTestId("finance-import-file")).toHaveText(
    "bank-export.csv",
  );
  await dialog
    .getByRole("button", { name: `Import into ${account.name}` })
    .click();
  return dialog;
}

test.describe("Dashboard CSV import", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  test("imports into the chosen account, reports the counts, and skips duplicates", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const account = await createAccount(request, `E2E CSV ${stamp}`);
    try {
      const csv = [
        "date,amount,counterparty,description,category,notes,external_id",
        `2026-10-08,-1.25,E2E Kiosk ${stamp},Zeitung,,Browser CSV ${stamp},browser-csv-${stamp}`,
      ].join("\n");
      await page.goto("/finance");
      const dialog = await importCsv(page, account, csv);
      await expect(dialog.getByRole("status")).toHaveText(
        "Imported 1, 0 duplicates skipped, 0 failed.",
      );

      const [stored] = await accountTransactions(request, account);
      expect(stored).toMatchObject({
        accountId: account.id,
        amount: { amount: -125, currency: "EUR" },
        bookingDate: "2026-10-08",
        counterparty: `E2E Kiosk ${stamp}`,
        description: "Zeitung",
        notes: `Browser CSV ${stamp}`,
        source: { kind: "csv" },
        readOnly: true,
      });

      // The same file again: the row is already there.
      await dialog
        .locator("[data-slot=dialog-footer]")
        .getByRole("button", { name: "Close" })
        .click();
      const again = await importCsv(page, account, csv);
      await expect(again.getByRole("status")).toHaveText(
        "Imported 0, 1 duplicate skipped, 0 failed.",
      );
      expect(await accountTransactions(request, account)).toHaveLength(1);
      await again
        .locator("[data-slot=dialog-footer]")
        .getByRole("button", { name: "Close" })
        .click();

      // The account's card shows the new balance.
      await expect(
        page.getByTestId("finance-account").filter({ hasText: account.name }),
      ).toContainText(/1[.,]25/);
    } finally {
      await archiveAccount(request, account);
    }
  });

  test("reads a semicolon-separated file with German dates and decimals, reporting bad rows", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const account = await createAccount(request, `E2E CSV DE ${stamp}`);
    try {
      // A Deutsche Bank style export, its columns renamed to Life OS's.
      const csv = [
        "date;amount;counterparty;description;notes;external_id",
        `09.10.2026;-1.234,56;E2E Möbelhaus ${stamp};Regal;;de-${stamp}-1`,
        `10.10.2026;2.000,00;E2E Arbeitgeber ${stamp};Gehalt;;de-${stamp}-2`,
        `not a date;-5,00;E2E Broken ${stamp};;;de-${stamp}-3`,
      ].join("\n");
      await page.goto("/finance");
      const dialog = await importCsv(page, account, csv);
      const status = dialog.getByRole("status");
      await expect(status).toContainText(
        "Imported 2, 0 duplicates skipped, 1 failed.",
      );
      // The header is row 1, so the third data row is row 4.
      await expect(status.getByRole("listitem")).toHaveCount(1);
      await expect(status.getByRole("listitem")).toContainText(/^Row 4: /);

      const stored = await accountTransactions(request, account);
      expect(
        stored
          .map((item) => [item.bookingDate, item.amount.amount] as const)
          .sort(([a], [b]) => a.localeCompare(b)),
      ).toEqual([
        ["2026-10-09", -123456],
        ["2026-10-10", 200000],
      ]);
    } finally {
      await archiveAccount(request, account);
    }
  });
});
