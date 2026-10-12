import { type APIRequestContext, expect, type Page, test } from "./fixtures";

// The AI's categories in the Finance workspace (P13-08). A category the AI
// chose shows a subtle "by AI" mark; when the owner corrects one, the page
// offers "Always put <counterparty> in <category>?", and Create rule makes
// the rule and applies it, so the other transactions the AI put elsewhere
// follow. The spec leaves the AI's choices through the Finance API the way
// the processor's job does, as a development tool (dev:e2e: the gateway
// passes no agent: actor, and Finance refuses only the owner there), so no
// Codex run is needed; the verification stack keeps the job itself off
// (LIFE_OS_VERIFY_AI_CATEGORIES). Names carry a tag made of the run's
// timestamp in letters, since a rule drops trailing numbers from its text.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Transaction = {
  id: string;
  categoryId: string | null;
  categorizedBy: string | null;
};
type Category = { id: string; name: string };
type Rule = {
  id: string;
  categoryId: string;
  match: { field: string; operator: string; text: string };
};

const api = "/api/finance/v1";

/** The run's timestamp in letters only. */
const tagOf = (stamp: number) =>
  stamp.toString(36).replace(/[0-9]/g, (digit) => "qrstuvwxyz"[+digit]);

async function created<T>(
  response: Awaited<ReturnType<APIRequestContext["post"]>>,
) {
  expect(response.status(), await response.text()).toBe(201);
  return (await response.json()) as T;
}

/** Leaves the AI's category on transactions, as the processor's job does. */
async function chooseAsTheAI(
  request: APIRequestContext,
  choices: { transactionId: string; categoryId: string }[],
) {
  const response = await request.post(`${api}/categories/ai/choices`, {
    data: { choices },
  });
  expect(response.status(), await response.text()).toBe(200);
  const result = await response.json();
  expect(result.categorized).toBe(choices.length);
}

async function transaction(request: APIRequestContext, id: string) {
  return (await (
    await request.get(`${api}/transactions/${id}`)
  ).json()) as Transaction;
}

async function rulesFor(request: APIRequestContext, text: string) {
  const rules = (await (await request.get(`${api}/categories/rules`)).json())
    .items as Rule[];
  return rules.filter((rule) => rule.match.text === text);
}

async function openFinance(page: Page, search: string) {
  await page.goto("/finance");
  await expect(page.getByTestId("finance-workspace")).toBeVisible();
  await page.getByLabel("Search transactions").fill(search);
}

test.describe("AI categories", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  test("an AI category is marked, and correcting it offers and creates a rule", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const tag = tagOf(stamp);
    const counterparty = `Kneipe Zum ${tag}`;
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const account = await created<{ id: string }>(
        await request.post(`${api}/accounts`, {
          data: { name: `E2E AI ${stamp}`, kind: "bank", currency: "EUR" },
        }),
      );
      cleanup.push(() =>
        request.patch(`${api}/accounts/${account.id}`, {
          data: { archived: true },
        }),
      );
      const bars = await created<Category>(
        await request.post(`${api}/categories`, {
          data: { name: `E2E Bars ${tag}`, kind: "expense" },
        }),
      );
      const fun = await created<Category>(
        await request.post(`${api}/categories`, {
          data: { name: `E2E Fun ${tag}`, kind: "expense" },
        }),
      );
      for (const category of [bars, fun]) {
        cleanup.push(() =>
          request.patch(`${api}/categories/${category.id}`, {
            data: { archived: true },
          }),
        );
      }
      const spend = async (amount: number, bookingDate: string) => {
        const item = await created<Transaction>(
          await request.post(`${api}/transactions`, {
            data: {
              accountId: account.id,
              amount: { amount, currency: "EUR" },
              bookingDate,
              counterparty,
            },
          }),
        );
        cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));
        expect(item.categoryId).toBeNull();
        return item;
      };
      const tonight = await spend(-2350, "2026-10-10");
      const lastWeek = await spend(-1800, "2026-10-03");
      // The AI puts both in the wrong category.
      await chooseAsTheAI(request, [
        { transactionId: tonight.id, categoryId: fun.id },
        { transactionId: lastWeek.id, categoryId: fun.id },
      ]);

      await openFinance(page, tag);
      const rows = page.getByTestId("finance-transaction");
      await expect(rows).toHaveCount(2);
      for (const row of await rows.all()) {
        await expect(row).toContainText(fun.name);
        await expect(row.getByTestId("finance-ai-mark")).toBeVisible();
        await expect(row.getByTestId("finance-ai-mark")).toHaveAttribute(
          "title",
          /Chosen by AI/,
        );
      }
      // The category filter finds what the AI chose.
      await page
        .getByLabel("Category", { exact: true })
        .selectOption({ label: "Chosen by AI" });
      await expect(rows).toHaveCount(2);

      // The owner corrects the latest one.
      await rows
        .first()
        .getByRole("button", { name: new RegExp(counterparty) })
        .click();
      const edit = page.getByRole("dialog", { name: "Edit transaction" });
      await expect(edit).toContainText("Chosen by AI");
      await edit
        .getByLabel("Category", { exact: true })
        .selectOption({ label: bars.name });
      await expect(edit).not.toContainText("Chosen by AI");
      await edit.getByRole("button", { name: "Save" }).click();
      await expect(edit).toHaveCount(0);
      expect(await transaction(request, tonight.id)).toMatchObject({
        categoryId: bars.id,
        categorizedBy: "manual",
      });

      // Offered, never made silently.
      const offer = page.getByRole("region", { name: "Make a rule" });
      await expect(offer).toContainText(
        `Always put “${counterparty}” in ${bars.name}?`,
      );
      expect(await rulesFor(request, counterparty)).toHaveLength(0);
      await offer.getByRole("button", { name: "Create rule" }).click();
      // Applying the rules moved the other one (other specs' work may be
      // in the counts too).
      await expect(offer.getByRole("status")).toContainText(
        `Rule created: Counterparty contains “${counterparty}” → ${bars.name}. Checked`,
      );
      await expect(offer.getByRole("status")).toContainText(
        /[1-9]\d* recategorized/,
      );
      const [rule] = await rulesFor(request, counterparty);
      expect(rule).toMatchObject({
        categoryId: bars.id,
        match: { field: "counterparty", operator: "contains" },
      });
      cleanup.push(() => request.delete(`${api}/categories/rules/${rule.id}`));
      // The other one follows the rule; the owner's own stays theirs.
      expect(await transaction(request, lastWeek.id)).toMatchObject({
        categoryId: bars.id,
        categorizedBy: "rule",
      });
      expect(await transaction(request, tonight.id)).toMatchObject({
        categorizedBy: "manual",
      });
      await offer.getByRole("button", { name: "Close" }).click();
      await expect(offer).toHaveCount(0);
    } finally {
      for (const step of cleanup.reverse()) await step().catch(() => {});
    }
  });

  test("the rule offer can be dismissed", async ({ page, request }) => {
    const stamp = Date.now();
    const tag = tagOf(stamp);
    const counterparty = `Blumenladen ${tag}`;
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const gifts = await created<Category>(
        await request.post(`${api}/categories`, {
          data: { name: `E2E Gifts ${tag}`, kind: "expense" },
        }),
      );
      const home = await created<Category>(
        await request.post(`${api}/categories`, {
          data: { name: `E2E Home ${tag}`, kind: "expense" },
        }),
      );
      for (const category of [gifts, home]) {
        cleanup.push(() =>
          request.patch(`${api}/categories/${category.id}`, {
            data: { archived: true },
          }),
        );
      }
      // In the Cash account, by its default.
      const item = await created<Transaction>(
        await request.post(`${api}/transactions`, {
          data: { amount: { amount: -1999, currency: "EUR" }, counterparty },
        }),
      );
      cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));
      await chooseAsTheAI(request, [
        { transactionId: item.id, categoryId: home.id },
      ]);

      await openFinance(page, tag);
      const row = page.getByTestId("finance-transaction");
      await expect(row.getByTestId("finance-ai-mark")).toBeVisible();
      await row.getByRole("button", { name: new RegExp(counterparty) }).click();
      const edit = page.getByRole("dialog", { name: "Edit transaction" });
      await edit
        .getByLabel("Category", { exact: true })
        .selectOption({ label: gifts.name });
      await edit.getByRole("button", { name: "Save" }).click();
      await expect(edit).toHaveCount(0);
      // The owner's category carries no mark.
      await expect(row).toContainText(gifts.name);
      await expect(row.getByTestId("finance-ai-mark")).toHaveCount(0);

      const offer = page.getByRole("region", { name: "Make a rule" });
      await expect(offer).toContainText(
        `Always put “${counterparty}” in ${gifts.name}?`,
      );
      await offer.getByRole("button", { name: "Not now" }).click();
      await expect(offer).toHaveCount(0);
      expect(await rulesFor(request, counterparty)).toHaveLength(0);
    } finally {
      for (const step of cleanup.reverse()) await step().catch(() => {});
    }
  });
});
