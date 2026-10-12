import {
  type APIRequestContext,
  expect,
  type Locator,
  type Page,
  test,
} from "./fixtures";
import { datePicker } from "./date-picker";

// The Finance workspace (P13-01): accounts with the built-in Cash account,
// manual transactions added, found, edited, and deleted, categories with
// rules, CSV import into a chosen account, and the monthly summary. Names
// carry a timestamp so runs never collide; what the API allows is cleaned
// up (imported transactions can't be deleted, so their account is archived).

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Money = { amount: number; currency: string };
type Account = {
  id: string;
  name: string;
  builtIn: boolean;
  balance: Money;
  bookedBalance: Money;
  pending: Money;
  currency: string;
};
type Transaction = {
  id: string;
  accountId: string;
  amount: Money;
  categoryId: string | null;
  categorizedBy: string | null;
  notes: string | null;
};
type Category = { id: string; name: string };
type Rule = { id: string; categoryId: string };

const api = "/api/finance/v1";

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

async function accounts(request: APIRequestContext): Promise<Account[]> {
  const response = await request.get(`${api}/accounts`);
  expect(response.ok()).toBeTruthy();
  return (await response.json()).items;
}

async function cash(request: APIRequestContext) {
  const found = (await accounts(request)).find((account) => account.builtIn);
  expect(found, "the built-in Cash account").toBeTruthy();
  return found!;
}

async function transaction(request: APIRequestContext, id: string) {
  const response = await request.get(`${api}/transactions/${id}`);
  return {
    status: response.status(),
    body: response.ok() ? ((await response.json()) as Transaction) : undefined,
  };
}

/** Opens /finance and reads the owner's today the server rendered with. */
async function openFinance(page: Page) {
  await page.goto("/finance");
  const workspace = page.getByTestId("finance-workspace");
  await expect(workspace).toBeVisible();
  return { workspace, today: (await workspace.getAttribute("data-today"))! };
}

function rows(page: Page) {
  return page.getByTestId("finance-transaction");
}

async function search(page: Page, text: string) {
  await page.getByLabel("Search transactions").fill(text);
}

function dialog(page: Page, name: string | RegExp): Locator {
  return page.getByRole("dialog", { name });
}

test.describe("Finance workspace", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  test("lists accounts with Cash first and adds, then archives, an account", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E Giro ${stamp}`;
    await openFinance(page);
    await expect(
      page.getByRole("heading", { name: "Finance", level: 1 }),
    ).toBeVisible();
    const cards = page.getByTestId("finance-account");
    await expect(cards.first()).toContainText("Cash");
    await expect(cards.first()).toContainText("Built-in cash");
    await expect(
      page.getByRole("button", { name: /^Edit account / }).first(),
    ).toHaveAccessibleName(`Edit account ${(await cash(request)).name}`);

    await page.getByRole("button", { name: "Add account" }).click();
    const form = dialog(page, "Add account");
    await form.getByLabel("Name", { exact: true }).fill(name);
    await form
      .getByLabel("Kind", { exact: true })
      .selectOption({ label: "Bank account" });
    await expect(form.getByLabel("Currency", { exact: true })).toHaveValue(
      (await cash(request)).currency,
    );
    await form.getByLabel("Opening balance", { exact: true }).fill("100,00");
    await form.getByRole("button", { name: "Add account" }).click();
    await expect(form).toHaveCount(0);

    const card = cards.filter({ hasText: name });
    await expect(card).toContainText(/100[.,]00/);
    const created = (await accounts(request)).find(
      (account) => account.name === name,
    );
    expect(created?.balance.amount).toBe(10000);

    // A second account with the same name is refused in plain words.
    await page.getByRole("button", { name: "Add account" }).click();
    const duplicate = dialog(page, "Add account");
    await duplicate.getByLabel("Name", { exact: true }).fill(name);
    await duplicate.getByRole("button", { name: "Add account" }).click();
    await expect(duplicate.getByRole("alert")).toHaveText(
      "Another account already has this name.",
    );
    await duplicate.getByRole("button", { name: "Cancel" }).click();

    await page.getByRole("button", { name: `Edit account ${name}` }).click();
    const edit = dialog(page, `Edit ${name}`);
    await edit.getByRole("button", { name: "Archive account" }).click();
    await expect(edit).toHaveCount(0);
    await expect(card).toHaveCount(0);
    // Cash can't be archived: its dialog has no Archive button.
    await page
      .getByRole("button", {
        name: `Edit account ${(await cash(request)).name}`,
      })
      .click();
    const cashDialog = page.getByRole("dialog");
    await expect(
      cashDialog.getByRole("button", { name: "Archive account" }),
    ).toHaveCount(0);
    await cashDialog.getByRole("button", { name: "Cancel" }).click();
  });

  test("shows a balance with its pending money, and what is booked and pending under it", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E Pending ${stamp}`;
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const created = await request.post(`${api}/accounts`, {
        data: {
          name,
          kind: "bank",
          currency: "EUR",
          openingBalanceMinor: 10000,
        },
      });
      expect(created.status()).toBe(201);
      const account = (await created.json()) as Account;
      cleanup.push(() =>
        request.patch(`${api}/accounts/${account.id}`, {
          data: { archived: true },
        }),
      );
      // 100.00 opening, 12.50 booked out, and a 45.35 card hold pending.
      for (const [amount, status] of [
        [-1250, "booked"],
        [-4535, "pending"],
      ] as const) {
        const response = await request.post(`${api}/transactions`, {
          data: {
            accountId: account.id,
            amount: { amount, currency: "EUR" },
            status,
            counterparty: `E2E Shop ${stamp}`,
          },
        });
        expect(response.status()).toBe(201);
        const { id } = (await response.json()) as Transaction;
        cleanup.push(() => request.delete(`${api}/transactions/${id}`));
      }
      const seeded = (await accounts(request)).find(
        (item) => item.id === account.id,
      );
      expect(seeded).toMatchObject({
        balance: { amount: 4215 },
        bookedBalance: { amount: 8750 },
        pending: { amount: -4535 },
      });

      // The card shows what there is to spend, and the split quietly.
      const { today } = await openFinance(page);
      const card = page
        .getByTestId("finance-account")
        .filter({ hasText: name });
      await expect(card).toContainText(/42[.,]15/);
      await expect(card.getByTestId("balance-breakdown")).toHaveText(
        /^Booked .*87[.,]50.* · Pending -.*45[.,]35/,
      );
      // An account without pending money has no such line.
      await expect(
        page
          .getByTestId("finance-account")
          .first()
          .getByTestId("balance-breakdown"),
      ).toHaveCount((await cash(request)).pending.amount === 0 ? 0 : 1);

      // The summary's balance now has the pending money too.
      const summary = await (
        await request.get(`${api}/summaries/monthly?month=${today.slice(0, 7)}`)
      ).json();
      expect(summary.totalBalance.amount).toBe(
        summary.totalBookedBalance.amount + summary.totalPending.amount,
      );
      await page.getByRole("tab", { name: "Summary" }).click();
      const breakdown = page
        .getByTestId("finance-summary")
        .getByTestId("balance-breakdown");
      if (summary.totalPending.amount !== 0) {
        await expect(breakdown).toHaveText(/^Booked .* · Pending /);
      }
    } finally {
      for (const step of cleanup.reverse()) await step().catch(() => {});
    }
  });

  test("adds a manual cash expense, finds it, edits it, and deletes it", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const counterparty = `E2E Bäckerei ${stamp}`;
    const before = (await cash(request)).balance.amount;
    const { today } = await openFinance(page);

    await page.getByRole("button", { name: "Add transaction" }).click();
    const add = dialog(page, "Add transaction");
    await expect(
      add.getByRole("button", { name: "Money out" }),
    ).toHaveAttribute("aria-pressed", "true");
    await expect(add.getByLabel("Account", { exact: true })).toHaveValue(
      (await cash(request)).id,
    );
    await expect(datePicker(add, "Date")).toHaveAttribute("data-value", today);
    await add.getByLabel("Amount", { exact: true }).fill("12,50");
    await add.getByLabel("Counterparty", { exact: true }).fill(counterparty);
    await add.getByLabel("Description", { exact: true }).fill("Brötchen");
    await add.getByLabel("Notes", { exact: true }).fill(`e2e ${stamp}`);
    await add.getByRole("button", { name: "Add transaction" }).click();
    await expect(add).toHaveCount(0);

    // Search finds it; the amount is money out.
    await search(page, String(stamp));
    await expect(rows(page)).toHaveCount(1);
    const row = rows(page).first();
    await expect(row).toContainText(counterparty);
    await expect(row).toContainText("Brötchen");
    await expect(row).toContainText(/-.?12[.,]50/);
    await expect
      .poll(async () => (await cash(request)).balance.amount)
      .toBe(before - 1250);

    // Filters combine with the search.
    await page.getByLabel("Direction", { exact: true }).selectOption("in");
    await expect(
      page.getByText("No transactions match these filters."),
    ).toBeVisible();
    await page.getByLabel("Direction", { exact: true }).selectOption("out");
    await expect(rows(page)).toHaveCount(1);
    await page.getByLabel("Source", { exact: true }).selectOption("csv");
    await expect(rows(page)).toHaveCount(0);
    await page.getByLabel("Source", { exact: true }).selectOption("manual");
    await page
      .getByLabel("Category", { exact: true })
      .selectOption("uncategorized");
    await expect(rows(page)).toHaveCount(1);
    // The Cash card filters to Cash.
    const cashAccount = await cash(request);
    await page
      .getByTestId("finance-account")
      .first()
      .getByRole("button", { name: new RegExp(`^${escape(cashAccount.name)}`) })
      .click();
    await expect(page.getByLabel("Account", { exact: true })).toHaveValue(
      (await cash(request)).id,
    );
    await expect(rows(page)).toHaveCount(1);

    // Edit: a manual transaction changes in full.
    await row.getByRole("button", { name: new RegExp(counterparty) }).click();
    const edit = dialog(page, "Edit transaction");
    await edit.getByLabel("Amount", { exact: true }).fill("13");
    await edit
      .getByLabel("Description", { exact: true })
      .fill("Brötchen und Kaffee");
    await edit.getByRole("button", { name: "Save" }).click();
    await expect(edit).toHaveCount(0);
    await expect(row).toContainText("Brötchen und Kaffee");
    await expect(row).toContainText(/13[.,]00/);
    const listed = await request.get(
      `${api}/transactions?q=${encodeURIComponent(counterparty)}`,
    );
    const [saved] = (await listed.json()).items as Transaction[];
    expect(saved.amount).toEqual({ amount: -1300, currency: "EUR" });

    // Delete, after a confirmation.
    await row.getByRole("button", { name: new RegExp(counterparty) }).click();
    const remove = dialog(page, "Edit transaction");
    await remove.getByRole("button", { name: "Delete transaction" }).click();
    await expect(remove.getByText("Delete this transaction?")).toBeVisible();
    await remove.getByRole("button", { name: "Delete", exact: true }).click();
    await expect(remove).toHaveCount(0);
    await expect(
      page.getByText("No transactions match these filters."),
    ).toBeVisible();
    expect((await transaction(request, saved.id)).status).toBe(404);
    await expect
      .poll(async () => (await cash(request)).balance.amount)
      .toBe(before);
  });

  test("categorizes with a rule, imports a CSV into a chosen account, and summarizes by category", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const categoryName = `E2E Groceries ${stamp}`;
    const marker = `E2E-MARKT-${stamp}`;
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const { today } = await openFinance(page);
      const giroResponse = await request.post(`${api}/accounts`, {
        data: { name: `E2E Import ${stamp}`, kind: "bank", currency: "EUR" },
      });
      expect(giroResponse.status()).toBe(201);
      const giro = (await giroResponse.json()) as Account;
      cleanup.push(() =>
        request.patch(`${api}/accounts/${giro.id}`, {
          data: { archived: true },
        }),
      );
      // Recorded before the rule exists, so it has no category yet.
      const manualResponse = await request.post(`${api}/transactions`, {
        data: {
          amount: { amount: -999, currency: (await cash(request)).currency },
          bookingDate: today,
          counterparty: `${marker} Filiale`,
        },
      });
      expect(manualResponse.status()).toBe(201);
      const manual = (await manualResponse.json()) as Transaction;
      cleanup.push(() => request.delete(`${api}/transactions/${manual.id}`));
      expect(manual.categoryId).toBeNull();

      await page.reload();
      await page.getByRole("tab", { name: "Categories & rules" }).click();

      await page.getByRole("button", { name: "Add category" }).click();
      const categoryForm = dialog(page, "Add category");
      await categoryForm.getByLabel("Name", { exact: true }).fill(categoryName);
      await categoryForm
        .getByLabel("Kind", { exact: true })
        .selectOption("expense");
      await categoryForm.getByRole("button", { name: "Green" }).click();
      await categoryForm.getByRole("button", { name: "Add category" }).click();
      await expect(categoryForm).toHaveCount(0);
      await expect(
        page.getByTestId("finance-category").filter({ hasText: categoryName }),
      ).toBeVisible();
      const category = (
        (await (await request.get(`${api}/categories`)).json())
          .items as Category[]
      ).find((item) => item.name === categoryName)!;
      cleanup.push(() =>
        request.patch(`${api}/categories/${category.id}`, {
          data: { archived: true },
        }),
      );

      // Matches ignore case.
      await page.getByRole("button", { name: "Add rule" }).click();
      const ruleForm = dialog(page, "Add rule");
      await ruleForm
        .getByLabel("When", { exact: true })
        .selectOption("counterparty");
      await ruleForm
        .getByLabel("Match", { exact: true })
        .selectOption("contains");
      await ruleForm
        .getByLabel("Text", { exact: true })
        .fill(marker.toLowerCase());
      await ruleForm
        .getByLabel("Category", { exact: true })
        .selectOption({ label: categoryName });
      await ruleForm.getByRole("button", { name: "Add rule" }).click();
      await expect(ruleForm).toHaveCount(0);
      const ruleRow = page
        .getByTestId("finance-rule")
        .filter({ hasText: marker.toLowerCase() });
      await expect(ruleRow).toContainText(
        `Counterparty contains “${marker.toLowerCase()}”`,
      );
      await expect(ruleRow).toContainText(categoryName);
      const rule = (
        (await (await request.get(`${api}/categories/rules`)).json())
          .items as Rule[]
      ).find((item) => item.categoryId === category.id)!;
      cleanup.push(() => request.delete(`${api}/categories/rules/${rule.id}`));

      await page.getByLabel("Apply the rules to").selectOption("uncategorized");
      await page.getByRole("button", { name: "Apply rules" }).click();
      // At least ours; leftovers of an earlier failed run may match too.
      await expect(
        page.getByRole("region", { name: "Rules" }).getByRole("status"),
      ).toContainText(/: [1-9]\d* categorized/);
      expect((await transaction(request, manual.id)).body).toMatchObject({
        categoryId: category.id,
        categorizedBy: "rule",
      });

      // A CSV row without a category gets one from the rule.
      await page.getByRole("button", { name: "Import CSV" }).click();
      const importer = dialog(page, "Import CSV");
      await importer
        .getByLabel("Account", { exact: true })
        .selectOption({ label: `${giro.name} (${giro.currency})` });
      await importer.locator('input[type="file"]').setInputFiles({
        name: "giro.csv",
        mimeType: "text/csv",
        buffer: Buffer.from(
          [
            "date,amount,counterparty,description,category,notes,external_id",
            `${today},-25.00,${marker} Online,Lieferung,,,e2e-${stamp}`,
          ].join("\n"),
        ),
      });
      await importer
        .getByRole("button", { name: `Import into ${giro.name}` })
        .click();
      await expect(importer.getByRole("status")).toHaveText(
        "Imported 1, 0 duplicates skipped, 0 failed. 1 got a category from the rules.",
      );
      await importer
        .locator("[data-slot=dialog-footer]")
        .getByRole("button", { name: "Close" })
        .click();

      // Both are listed; the imported one is read-only but takes notes.
      await page.getByRole("tab", { name: "Transactions" }).click();
      await search(page, marker);
      await expect(rows(page)).toHaveCount(2);
      const imported = rows(page).filter({ hasText: `${marker} Online` });
      await expect(imported.getByText("CSV", { exact: true })).toBeVisible();
      await imported
        .getByRole("button", { name: new RegExp(`${marker} Online`) })
        .click();
      const details = dialog(page, "Imported transaction");
      await expect(details).toContainText(
        "Imported from a CSV file: only the category and notes can change",
      );
      await expect(details.getByLabel("Amount", { exact: true })).toHaveCount(
        0,
      );
      await expect(
        details.getByRole("button", { name: "Delete transaction" }),
      ).toHaveCount(0);
      await expect(details.getByLabel("Category", { exact: true })).toHaveValue(
        category.id,
      );
      await details
        .getByLabel("Notes", { exact: true })
        .fill(`checked ${stamp}`);
      await details.getByRole("button", { name: "Save" }).click();
      await expect(details).toHaveCount(0);
      const importedList = await request.get(
        `${api}/transactions?accountId=${giro.id}`,
      );
      expect((await importedList.json()).items[0]).toMatchObject({
        notes: `checked ${stamp}`,
        categoryId: category.id,
        readOnly: true,
      });

      // The month's summary shows the category with both amounts.
      await page.getByRole("tab", { name: "Summary" }).click();
      const summaryRow = page
        .getByTestId("finance-summary-row")
        .filter({ hasText: categoryName });
      await expect(summaryRow).toContainText(/34[.,]99/);
      await expect(summaryRow).toContainText("2 transactions");
      // Its name opens those transactions.
      await summaryRow.getByRole("button", { name: categoryName }).click();
      await expect(page.getByLabel("Category", { exact: true })).toHaveValue(
        category.id,
      );
      await expect(rows(page)).toHaveCount(2);
    } finally {
      for (const step of cleanup.reverse()) await step().catch(() => {});
    }
  });
});
