import { type APIRequestContext, expect, test } from "./fixtures";

// The AI budget plan on the Budget tab (P13-09). The spec asks for a plan
// two months ahead and leaves the AI's proposal through the Finance API the
// way the processor's job does, as a development tool (dev:e2e: the gateway
// passes no agent: actor, and Finance refuses only the owner there), so no
// Codex run is needed; the verification stack keeps the job itself off
// (LIFE_OS_AI_BUDGET_PLANS). The proposal is built from the month's planning
// data, so it passes Finance's checks whatever else the stack holds: a line
// for every active expense category, and savings no larger than the
// merchants' spending. The card shows the plan as made by AI; the owner
// unticks a line and applies the rest, and only the ticked lines reach the
// month's budget. Names carry a tag made of the run's timestamp in letters.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const api = "/api/finance/v1";

type Category = { id: string; name: string };
type Plan = { id: string; month: string; state: string };
type Merchant = { name: string; monthlyMinor: number };
type PlanningCategory = {
  categoryId: string;
  name: string;
  medianMinor: number | null;
  plannedMinor: number;
  merchants: Merchant[];
};
type PlanningData = {
  currency: string;
  incomeMedianMinor: number | null;
  categories: PlanningCategory[];
};
type BudgetMonth = {
  lines: { categoryId: string; planned: { amount: number } }[];
};

/** The run's timestamp in letters only. */
const tagOf = (stamp: number) =>
  stamp.toString(36).replace(/[0-9]/g, (digit) => "qrstuvwxyz"[+digit]);

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

/** The month `months` after a YYYY-MM month. */
function shiftMonth(month: string, months: number) {
  const date = new Date(`${month}-01T00:00:00Z`);
  date.setUTCMonth(date.getUTCMonth() + months);
  return date.toISOString().slice(0, 7);
}

function monthLabel(month: string) {
  return new Intl.DateTimeFormat("en-GB", {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(`${month}-01T00:00:00Z`));
}

async function created<T>(
  response: Awaited<ReturnType<APIRequestContext["post"]>>,
  status = 201,
) {
  expect(response.status(), await response.text()).toBe(status);
  return (await response.json()) as T;
}

test.describe("AI budget plan", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  test("the AI's plan is shown, and only the ticked lines are applied", async ({
    page,
    request,
  }) => {
    const tag = tagOf(Date.now());
    const current = ownerMonth();
    const month = shiftMonth(current, 2);
    const headline = `Eat out less in ${monthLabel(month)} (${tag})`;
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const account = await created<{ id: string }>(
        await request.post(`${api}/accounts`, {
          data: { name: `E2E AI Plan ${tag}`, kind: "bank", currency: "EUR" },
        }),
      );
      cleanup.push(() =>
        request.patch(`${api}/accounts/${account.id}`, {
          data: { archived: true },
        }),
      );
      const category = async (name: string) => {
        const item = await created<Category>(
          await request.post(`${api}/categories`, {
            data: { name, kind: "expense" },
          }),
        );
        cleanup.push(() =>
          request.patch(`${api}/categories/${item.id}`, {
            data: { archived: true },
          }),
        );
        return item;
      };
      const eating = await category(`E2E AI Eating ${tag}`);
      const groceries = await category(`E2E AI Groceries ${tag}`);
      // Booked spending in the two complete months before this one.
      const spend = async (
        amount: number,
        bookingDate: string,
        counterparty: string,
        categoryId: string,
      ) => {
        const item = await created<{ id: string }>(
          await request.post(`${api}/transactions`, {
            data: {
              accountId: account.id,
              amount: { amount, currency: "EUR" },
              bookingDate,
              counterparty,
              categoryId,
            },
          }),
        );
        cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));
      };
      for (const back of [1, 2]) {
        const earlier = shiftMonth(current, -back);
        await spend(-4200, `${earlier}-05`, `E2E Pizzeria ${tag}`, eating.id);
        await spend(-1800, `${earlier}-12`, `E2E Imbiss ${tag}`, eating.id);
        await spend(-9000, `${earlier}-08`, `E2E Markt ${tag}`, groceries.id);
      }

      // Ask for the plan, as the owner's "Plan my budget" does.
      const asked = await request.post(`${api}/ai-budget-plans`, {
        data: { month },
      });
      expect([200, 202], await asked.text()).toContain(asked.status());
      const plan = (await asked.json()) as Plan;
      cleanup.push(() => request.delete(`${api}/budgets/${month}`));
      cleanup.push(() =>
        request.post(`${api}/ai-budget-plans/${plan.id}/dismiss`),
      );

      // The AI's proposal, from the month's planning data.
      const data = (await (
        await request.get(`${api}/budgets/${month}/planning-data`)
      ).json()) as PlanningData;
      const mine = data.categories.find(
        (item) => item.categoryId === eating.id,
      );
      const theirs = data.categories.find(
        (item) => item.categoryId === groceries.id,
      );
      expect(mine?.merchants.length).toBe(2);
      expect(theirs).toBeDefined();
      const lines = data.categories.map((item) => ({
        categoryId: item.categoryId,
        proposedMinor:
          item.categoryId === eating.id
            ? Math.floor(((item.medianMinor ?? 0) * 3) / 4)
            : (item.medianMinor ?? item.plannedMinor),
        reason: `Keeps ${item.name} near what it usually costs.`,
      }));
      const eatingProposed = lines.find(
        (line) => line.categoryId === eating.id,
      )!.proposedMinor;
      const saving = (merchant: Merchant) =>
        Math.max(1, Math.floor(merchant.monthlyMinor / 2));
      const [pizzeria, imbiss] = mine!.merchants;
      const total = lines.reduce((sum, line) => sum + line.proposedMinor, 0);
      const income = Math.max(0, data.incomeMedianMinor ?? 0);
      const ready = await request.put(
        `${api}/ai-budget-plans/${plan.id}/result`,
        {
          data: {
            currency: data.currency,
            headline,
            lines,
            suggestions: [
              {
                title: "Cook instead of ordering",
                detail: `Cook at home twice a week instead of ${pizzeria.name}.`,
                categoryId: eating.id,
                merchants: [pizzeria.name],
                monthlySavingMinor: saving(pizzeria),
                link: "food_recipes",
              },
              {
                title: "Fewer quick lunches",
                detail: `Take lunch from home instead of ${imbiss.name}.`,
                categoryId: eating.id,
                merchants: [imbiss.name],
                monthlySavingMinor: saving(imbiss),
                link: null,
              },
            ],
            projectedTotalMinor: total,
            incomeEstimateMinor: income,
            projectedSavingsMinor: income - total,
          },
        },
      );
      expect(ready.status(), await ready.text()).toBe(200);

      // Nothing changed the budget yet.
      const before = (await (
        await request.get(`${api}/budgets/${month}`)
      ).json()) as BudgetMonth;
      const plannedBefore = (id: string) =>
        before.lines.find((line) => line.categoryId === id)?.planned.amount ??
        0;
      expect(plannedBefore(eating.id)).toBe(mine!.plannedMinor);

      await page.goto(`/finance?tab=budget&month=${month}`);
      await expect(page.getByTestId("finance-workspace")).toBeVisible();
      await expect(page.locator("#finance-budget-title")).toHaveText(
        monthLabel(month),
      );
      const card = page.getByTestId("finance-ai-plan");
      await expect(card).toContainText(
        `AI budget plan for ${monthLabel(month)}`,
      );
      await expect(card).toContainText("Made by AI");
      await expect(card).toContainText(headline);
      const suggestion = card
        .getByTestId("finance-ai-plan-suggestion")
        .filter({ hasText: "Cook instead of ordering" });
      await expect(suggestion).toContainText("≈");
      await expect(suggestion).toContainText("/month");
      await expect(suggestion).toContainText(pizzeria.name);
      await expect(
        suggestion.getByRole("link", { name: "Find recipes" }),
      ).toHaveAttribute("href", "/food?tab=recipes");
      const row = card
        .getByTestId("finance-ai-plan-line")
        .filter({ hasText: groceries.name });
      await expect(row).toHaveCount(1);

      // The owner unticks groceries and applies the rest.
      await card
        .getByRole("checkbox", { name: `Apply ${groceries.name}` })
        .click();
      await expect(
        card.getByRole("checkbox", { name: `Apply ${groceries.name}` }),
      ).not.toBeChecked();
      await card.getByRole("button", { name: "Apply plan" }).click();
      await expect(card).toContainText(
        new RegExp(
          `Applied ${data.categories.length - 1} of ${data.categories.length} categor`,
        ),
      );

      const after = (await (
        await request.get(`${api}/budgets/${month}`)
      ).json()) as BudgetMonth;
      const plannedAfter = (id: string) =>
        after.lines.find((line) => line.categoryId === id)?.planned.amount ?? 0;
      expect(plannedAfter(eating.id)).toBe(eatingProposed);
      // Groceries keeps what the plan had for it: nothing, as a new
      // category.
      expect(plannedAfter(groceries.id)).toBe(theirs!.plannedMinor);
      const applied = (await (
        await request.get(`${api}/ai-budget-plans/${plan.id}`)
      ).json()) as Plan;
      expect(applied.state).toBe("partly_applied");
      // The month's budget on the page follows.
      await expect(
        page
          .getByTestId("finance-budget-line")
          .filter({ hasText: eating.name }),
      ).toBeVisible();
    } finally {
      for (const step of cleanup.reverse()) await step().catch(() => {});
    }
  });
});
