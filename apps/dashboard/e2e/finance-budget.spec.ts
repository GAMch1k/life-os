import { type APIRequestContext, expect, type Page, test } from "./fixtures";

// The Finance Budget tab (P13-04): a month's plan made in the editor (for
// this month only, so the spec can return the month to the regular plan
// afterwards), progress that counts booked and pending spending, a
// category opening its transactions, the notice at 80%, and Home's budget
// card with the over-budget attention card. Names carry a timestamp so runs
// never collide; what the API allows is cleaned up.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const api = "/api/finance/v1";

type Category = { id: string; name: string };
type Transaction = { id: string };
type Notification = { title: string; dedupeKey: string; importance: string };

/** The owner's month (YYYY-MM): the stack runs in Europe/Berlin. */
function ownerMonth() {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: "Europe/Berlin",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  })
    .format(new Date())
    .slice(0, 7);
}

function monthLabel(month: string) {
  return new Intl.DateTimeFormat("en-GB", {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(`${month}-01T00:00:00Z`));
}

async function createCategory(request: APIRequestContext, name: string) {
  const response = await request.post(`${api}/categories`, {
    data: { name, kind: "expense" },
  });
  expect(response.status()).toBe(201);
  return (await response.json()) as Category;
}

async function spend(
  request: APIRequestContext,
  categoryId: string,
  amount: number,
  status: "booked" | "pending",
  counterparty: string,
) {
  const response = await request.post(`${api}/transactions`, {
    data: {
      amount: { amount, currency: "EUR" },
      status,
      counterparty,
      categoryId,
    },
  });
  expect(response.status()).toBe(201);
  return (await response.json()) as Transaction;
}

async function openBudget(page: Page) {
  await page.goto("/finance?tab=budget");
  await expect(page.getByTestId("finance-workspace")).toBeVisible();
  await expect(page.getByRole("tab", { name: "Budget" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  return page.getByTestId("finance-budget");
}

test.describe("Finance budget", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  test("plans this month in the editor and shows booked and pending progress", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E Budget Food ${stamp}`;
    const month = ownerMonth();
    const label = monthLabel(month);
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const category = await createCategory(request, name);
      cleanup.push(() =>
        request.patch(`${api}/categories/${category.id}`, {
          data: { archived: true },
        }),
      );
      for (const [amount, status] of [
        [-1250, "booked"],
        [-500, "pending"],
      ] as const) {
        const item = await spend(
          request,
          category.id,
          amount,
          status,
          `E2E Markt ${stamp}`,
        );
        cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));
      }
      cleanup.push(() => request.delete(`${api}/budgets/${month}`));

      const budget = await openBudget(page);
      await expect(page.locator("#finance-budget-title")).toHaveText(label);
      await page
        .getByRole("button", { name: /^(Edit plan|Plan )/ })
        .first()
        .click();
      const editor = page.getByRole("dialog");
      await expect(editor).toContainText(label);
      await editor.getByLabel(name, { exact: true }).fill("20,00");
      await editor.getByRole("radio", { name: `Only ${label}` }).click();
      await expect(editor.getByTestId("budget-editor-totals")).toContainText(
        "Planned",
      );
      await editor.getByRole("button", { name: "Save plan" }).click();
      await expect(editor).toHaveCount(0);

      // 12.50 booked and 5.00 pending of 20.00: 87%, near the limit.
      const line = budget
        .getByTestId("finance-budget-line")
        .filter({ hasText: name });
      await expect(line).toContainText(/17[.,]50 of .?20[.,]00/);
      await expect(line).toContainText("87%");
      await expect(line).toContainText(/incl\. .?5[.,]00 pending/);
      await expect(line).toContainText(/2[.,]50 left/);
      await expect(
        line.getByRole("progressbar", { name: `${name}: 87% of its budget` }),
      ).toBeVisible();
      await expect(budget).toContainText(
        `${label} has its own plan; the regular plan is unchanged.`,
      );
      await expect(page).toHaveURL(/\/finance\?tab=budget$/);

      // Budget notifications are low and sent once at 80%.
      await expect
        .poll(
          async () => {
            const response = await request.get(
              "/api/notifications/v1/notifications?category=finance.budget&limit=100",
            );
            if (!response.ok()) return undefined;
            const items = (await response.json()).items as Notification[];
            return items.find(
              (item) =>
                item.dedupeKey === `finance.budget:${month}:${category.id}:80`,
            );
          },
          { timeout: 30_000 },
        )
        .toMatchObject({ title: `${name} budget at 87%`, importance: "low" });

      // The category opens its transactions of the month, pending ones too.
      await line.getByRole("button", { name }).click();
      await expect(
        page.getByRole("tab", { name: "Transactions" }),
      ).toHaveAttribute("aria-selected", "true");
      await expect(page.getByTestId("finance-transaction")).toHaveCount(2);

      // Back on the Budget tab, the month returns to the regular plan.
      await page.getByRole("tab", { name: "Budget" }).click();
      await page.getByRole("button", { name: "Use the regular plan" }).click();
      await expect(
        page.getByRole("button", { name: "Use the regular plan" }),
      ).toHaveCount(0);
      // The regular plan (if any) has no amount for the new category.
      await expect(
        budget.getByTestId("finance-budget-line").filter({ hasText: name }),
      ).toContainText("Not in the plan");
    } finally {
      for (const step of cleanup.reverse()) await step();
    }
  });

  test("Home shows the month's budget and the categories over it", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E Budget Fun ${stamp}`;
    const month = ownerMonth();
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const category = await createCategory(request, name);
      cleanup.push(() =>
        request.patch(`${api}/categories/${category.id}`, {
          data: { archived: true },
        }),
      );
      const item = await spend(
        request,
        category.id,
        -3000,
        "booked",
        `E2E Kino ${stamp}`,
      );
      cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));
      // This month only, so the month returns to the regular plan after.
      const saved = await request.put(`${api}/budgets/${month}`, {
        data: {
          scope: "month",
          expectedIncomeMinor: 0,
          savingsGoalMinor: 0,
          lines: [{ categoryId: category.id, amountMinor: 2000 }],
        },
      });
      expect(saved.status()).toBe(200);
      cleanup.push(() => request.delete(`${api}/budgets/${month}`));

      await page.goto("/");
      const over = page.getByRole("region", { name: "Over budget" });
      await expect(over).toContainText(name);
      await expect(over).toContainText(/10[.,]00 over/);
      await expect(
        over.getByRole("link", { name: /Open budget/ }),
      ).toHaveAttribute("href", "/finance?tab=budget");
      const card = page.locator("#home-budget");
      await expect(card).toContainText(/30[.,]00 of .?20[.,]00 spent/);
      await expect(card).toContainText(name);
      await expect(card).toContainText("150%");
      await expect(
        card.getByRole("progressbar", { name: "Spent against the plan" }),
      ).toBeVisible();

      await card.getByRole("link", { name: /Open budget/ }).click();
      await expect(page).toHaveURL(/\/finance\?tab=budget/);
      await expect(
        page
          .getByTestId("finance-budget-line")
          .filter({ hasText: name })
          .getByText("Over budget"),
      ).toBeVisible();
    } finally {
      for (const step of cleanup.reverse()) await step();
    }
  });
});
