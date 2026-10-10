import { type APIRequestContext, expect, type Page, test } from "./fixtures";

// Finance subscriptions and alerts (P13-05): a recurring payment Finance
// detects is confirmed, edited, and ignored on the Subscriptions tab; a
// transaction is marked "This is a subscription" in its dialog; the Alerts
// dialog saves its thresholds; and Home shows a renewal within the lead
// days and a low balance. Names carry a timestamp so runs never collide;
// everything the spec creates is removed or archived afterwards, and the
// alert settings are put back.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const api = "/api/finance/v1";

type Account = { id: string; name: string };
type Transaction = { id: string };
type Subscription = { id: string; name: string; state: string };
type Settings = {
  largeDebit: { enabled: boolean; threshold: { amount: number } };
  lowBalance: { enabled: boolean; threshold: { amount: number } };
  renewals: { enabled: boolean; leadDays: number };
};

/** The owner's today (YYYY-MM-DD): the stack runs in Europe/Berlin. */
function ownerToday() {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: "Europe/Berlin",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date());
}

function addDays(day: string, days: number) {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** A month earlier, the 31st becoming the month's last day. */
function monthBefore(day: string) {
  const [year, month, date] = day.split("-").map(Number);
  const first = new Date(Date.UTC(year, month - 2, 1));
  const last = new Date(Date.UTC(year, month - 1, 0)).getUTCDate();
  first.setUTCDate(Math.min(date, last));
  return first.toISOString().slice(0, 10);
}

async function createAccount(
  request: APIRequestContext,
  name: string,
  kind: string,
  openingBalanceMinor: number,
) {
  const response = await request.post(`${api}/accounts`, {
    data: { name, kind, currency: "EUR", openingBalanceMinor },
  });
  expect(response.status()).toBe(201);
  return (await response.json()) as Account;
}

async function pay(
  request: APIRequestContext,
  accountId: string,
  amount: number,
  bookingDate: string,
  counterparty: string,
) {
  const response = await request.post(`${api}/transactions`, {
    data: {
      accountId,
      amount: { amount, currency: "EUR" },
      bookingDate,
      counterparty,
    },
  });
  expect(response.status()).toBe(201);
  return (await response.json()) as Transaction;
}

async function subscriptions(request: APIRequestContext) {
  const response = await request.get(`${api}/subscriptions`);
  expect(response.ok()).toBe(true);
  return ((await response.json()) as { items: Subscription[] }).items;
}

/** Detection runs in the background: wait until it suggested `name`. */
async function waitForSubscription(request: APIRequestContext, name: string) {
  let found: Subscription | undefined;
  await expect(async () => {
    found = (await subscriptions(request)).find((item) => item.name === name);
    expect(found).toBeDefined();
  }).toPass({ timeout: 20_000 });
  return found as Subscription;
}

async function openSubscriptions(page: Page) {
  await page.goto("/finance?tab=subscriptions");
  await expect(page.getByTestId("finance-workspace")).toBeVisible();
  await expect(
    page.getByRole("tab", { name: "Subscriptions" }),
  ).toHaveAttribute("aria-selected", "true");
  return page.getByTestId("finance-subscriptions");
}

test.describe("Finance subscriptions and alerts", () => {
  test.skip(!enabled, "requires LIFE_OS_E2E_URL (an isolated stack)");

  test("confirms, edits, and ignores a detected subscription", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const counterparty = `E2E Streaming ${stamp}`;
    const today = ownerToday();
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const account = await createAccount(
        request,
        `E2E Subs ${stamp}`,
        "bank",
        500000,
      );
      cleanup.push(() =>
        request.patch(`${api}/accounts/${account.id}`, {
          data: { archived: true },
        }),
      );
      // Three monthly payments, the last yesterday.
      const last = addDays(today, -1);
      for (const day of [
        monthBefore(monthBefore(last)),
        monthBefore(last),
        last,
      ]) {
        const item = await pay(request, account.id, -1299, day, counterparty);
        cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));
      }
      const detected = await waitForSubscription(request, counterparty);
      cleanup.push(() => request.delete(`${api}/subscriptions/${detected.id}`));
      expect(detected.state).toBe("suggested");

      const tab = await openSubscriptions(page);
      const suggested = tab.getByTestId("finance-subscriptions-suggested");
      const row = suggested
        .getByTestId("finance-subscription")
        .filter({ hasText: counterparty });
      await expect(row).toContainText("€12.99 / month");
      await expect(row).toContainText("3 payments");
      await row
        .getByRole("button", { name: `Confirm ${counterparty}` })
        .click();

      const confirmed = tab
        .getByTestId("finance-subscriptions-confirmed")
        .getByTestId("finance-subscription")
        .filter({ hasText: counterparty });
      await expect(confirmed).toContainText("Next on");
      await expect(
        tab.getByTestId("finance-subscriptions-total"),
      ).toContainText("confirmed");

      // Edit: a new name and price stay the owner's.
      await confirmed
        .getByRole("button", { name: `Edit ${counterparty}` })
        .click();
      const editor = page.getByRole("dialog", { name: "Edit subscription" });
      const renamed = `E2E Video ${stamp}`;
      await editor.getByLabel("Name", { exact: true }).fill(renamed);
      await editor.getByLabel("Amount", { exact: true }).fill("13,99");
      await editor
        .getByLabel("Repeats", { exact: true })
        .selectOption("yearly");
      await editor.getByRole("button", { name: "Save" }).click();
      await expect(editor).toBeHidden();
      const edited = tab
        .getByTestId("finance-subscription")
        .filter({ hasText: renamed });
      await expect(edited).toContainText("€13.99 / year");
      await expect(edited).toContainText("a month");

      // Ignore: it leaves the list, under Ignored (n).
      await edited.getByRole("button", { name: `Ignore ${renamed}` }).click();
      await expect(
        tab.getByTestId("finance-subscriptions-confirmed").getByText(renamed),
      ).toHaveCount(0);
      await tab.getByRole("button", { name: /^Ignored \(\d+\)$/ }).click();
      await expect(
        tab.getByTestId("finance-subscriptions-ignored").getByText(renamed),
      ).toBeVisible();
    } finally {
      for (const step of cleanup.reverse()) await step();
    }
  });

  test("marks a transaction as a subscription from its dialog", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const counterparty = `E2E Gym ${stamp}`;
    const cleanup: (() => Promise<unknown>)[] = [];
    try {
      const account = await createAccount(
        request,
        `E2E Gym Account ${stamp}`,
        "bank",
        500000,
      );
      cleanup.push(() =>
        request.patch(`${api}/accounts/${account.id}`, {
          data: { archived: true },
        }),
      );
      const item = await pay(
        request,
        account.id,
        -2999,
        ownerToday(),
        counterparty,
      );
      cleanup.push(() => request.delete(`${api}/transactions/${item.id}`));

      await page.goto("/finance");
      await page.getByLabel("Search transactions").fill(counterparty);
      await page
        .getByTestId("finance-transaction")
        .filter({ hasText: counterparty })
        .getByRole("button", { name: new RegExp(counterparty) })
        .click();
      const transaction = page.getByRole("dialog", {
        name: "Edit transaction",
      });
      await transaction
        .getByRole("button", { name: "This is a subscription" })
        .click();
      await transaction
        .getByLabel("Repeats", { exact: true })
        .selectOption("monthly");
      await transaction
        .getByRole("button", { name: "Add subscription" })
        .click();
      await expect(transaction.getByRole("status")).toHaveText(
        `Added “${counterparty}” to your subscriptions (monthly).`,
      );
      const created = (await subscriptions(request)).find(
        (sub) => sub.name === counterparty,
      );
      expect(created?.state).toBe("confirmed");
      if (created) {
        cleanup.push(() =>
          request.delete(`${api}/subscriptions/${created.id}`),
        );
      }
      await transaction.getByRole("button", { name: "Cancel" }).click();

      const tab = await openSubscriptions(page);
      await expect(
        tab
          .getByTestId("finance-subscriptions-confirmed")
          .getByText(counterparty),
      ).toBeVisible();
    } finally {
      for (const step of cleanup.reverse()) await step();
    }
  });

  test("saves the alert settings and shows renewals and low balances on Home", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const cleanup: (() => Promise<unknown>)[] = [];
    const before = (await (
      await request.get(`${api}/settings`)
    ).json()) as Settings;
    try {
      cleanup.push(() =>
        request.put(`${api}/settings`, {
          data: {
            largeDebit: {
              enabled: before.largeDebit.enabled,
              thresholdMinor: before.largeDebit.threshold.amount,
            },
            lowBalance: {
              enabled: before.lowBalance.enabled,
              thresholdMinor: before.lowBalance.threshold.amount,
            },
            renewals: before.renewals,
          },
        }),
      );
      await page.goto("/finance");
      await page.getByRole("button", { name: "Alerts" }).click();
      const alerts = page.getByRole("dialog", { name: "Alerts" });
      await alerts.getByLabel("Large debits from").fill("300,00");
      await alerts.getByLabel("Low balance below").fill("100,00");
      await alerts.getByLabel("Renewal reminders, remind me").fill("3");
      await alerts.getByRole("button", { name: "Save alerts" }).click();
      await expect(alerts).toBeHidden();
      const saved = (await (
        await request.get(`${api}/settings`)
      ).json()) as Settings;
      expect(saved.largeDebit.threshold.amount).toBe(30000);
      expect(saved.renewals.leadDays).toBe(3);

      // A wallet below €100 and a subscription renewing in two days.
      const wallet = await createAccount(
        request,
        `E2E Wallet ${stamp}`,
        "cash",
        5000,
      );
      cleanup.push(() =>
        request.patch(`${api}/accounts/${wallet.id}`, {
          data: { archived: true },
        }),
      );
      const spent = await pay(
        request,
        wallet.id,
        -500,
        ownerToday(),
        `E2E Kiosk ${stamp}`,
      );
      cleanup.push(() => request.delete(`${api}/transactions/${spent.id}`));
      const name = `E2E Renewal ${stamp}`;
      const paid = await pay(
        request,
        wallet.id,
        -999,
        monthBefore(addDays(ownerToday(), 2)),
        name,
      );
      cleanup.push(() => request.delete(`${api}/transactions/${paid.id}`));
      const marked = await request.post(`${api}/subscriptions`, {
        data: { transactionId: paid.id, cadence: "monthly" },
      });
      expect(marked.status()).toBe(201);
      const subscription = (await marked.json()) as Subscription;
      cleanup.push(() =>
        request.delete(`${api}/subscriptions/${subscription.id}`),
      );

      await page.goto("/");
      const renewals = page.locator("#attention-renewals");
      await expect(renewals).toContainText("Renewing soon");
      await expect(renewals).toContainText(name);
      await expect(renewals).toContainText("in 2 days");
      const low = page.locator("#attention-low-balance");
      await expect(low).toContainText("Low balance");
      await expect(low).toContainText(`E2E Wallet ${stamp}`);
      await low.getByRole("link", { name: /Open Finance/ }).click();
      await expect(
        page
          .getByTestId("finance-account")
          .filter({ hasText: `E2E Wallet ${stamp}` })
          .getByText("Low balance"),
      ).toBeVisible();
    } finally {
      for (const step of cleanup.reverse()) await step();
    }
  });
});
