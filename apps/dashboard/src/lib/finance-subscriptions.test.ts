import assert from "node:assert/strict";
import test from "node:test";

import {
  type FinanceSettings,
  type Subscription,
  type SubscriptionList,
  daysUntil,
  evidenceText,
  homeRenewals,
  inDays,
  leadDaysText,
  lowBalanceAccounts,
  monthlyText,
  pricePerCadence,
  renewalText,
  settingsBody,
  settingsDraft,
  subscriptionDraft,
  subscriptionGroups,
  subscriptionOf,
  totalNote,
  updateSubscriptionBody,
  withSubscription,
} from "./finance-subscriptions";
import type { Account } from "./finance-workspace";

const STAMP = "2026-10-01T00:00:00Z";
const eur = (amount: number) => ({ amount, currency: "EUR" });

function subscription(overrides: Partial<Subscription> = {}): Subscription {
  return {
    id: "netflix",
    name: "Netflix",
    state: "confirmed",
    cadence: "monthly",
    amount: eur(1399),
    monthlyAmount: eur(1399),
    categoryId: null,
    accountId: "giro",
    lastDate: "2026-09-15",
    nextDate: "2026-10-15",
    renewal: "scheduled",
    confidence: "high",
    source: "detected",
    transactionIds: ["t3", "t2", "t1"],
    createdAt: STAMP,
    updatedAt: STAMP,
    ...overrides,
  };
}

function list(items: Subscription[], monthly = 0): SubscriptionList {
  return {
    items,
    totals: {
      currency: "EUR",
      monthly: eur(monthly),
      confirmed: 1,
      excluded: 0,
    },
  };
}

const TODAY = "2026-10-11";

test("texts say how often, how much, and when the next payment is", () => {
  const netflix = subscription();
  assert.equal(pricePerCadence(netflix, "en-GB"), "€13.99 / month");
  assert.equal(monthlyText(netflix, "en-GB"), "");
  const adac = subscription({
    name: "ADAC",
    cadence: "yearly",
    amount: eur(9400),
    monthlyAmount: eur(783),
    confidence: "low",
    state: "suggested",
    renewal: null,
    transactionIds: ["a2", "a1"],
    lastDate: "2026-03-02",
    nextDate: "2027-03-02",
  });
  assert.equal(pricePerCadence(adac, "en-GB"), "€94.00 / year");
  assert.equal(monthlyText(adac, "en-GB"), "≈ €7.83 a month");
  assert.equal(
    renewalText(adac, TODAY, "en-GB"),
    "Next expected Tue, 2 Mar 2027",
  );
  assert.equal(
    evidenceText(adac, TODAY, "en-GB"),
    "2 payments, last on Mon 2 Mar (seen twice, a year apart: less sure)",
  );
  assert.equal(renewalText(netflix, TODAY, "en-GB"), "Next on Thu 15 Oct");
  assert.equal(
    renewalText(
      subscription({ renewal: "upcoming", nextDate: "2026-10-13" }),
      TODAY,
      "en-GB",
    ),
    "Renews in 2 days",
  );
  assert.equal(
    renewalText(
      subscription({ renewal: "upcoming", nextDate: TODAY }),
      TODAY,
      "en-GB",
    ),
    "Renews today",
  );
  assert.equal(
    renewalText(
      subscription({ renewal: "overdue", nextDate: "2026-10-08" }),
      TODAY,
      "en-GB",
    ),
    "Expected Thu 8 Oct, not seen yet",
  );
  assert.equal(
    renewalText(
      subscription({ renewal: "missed", nextDate: "2026-09-15" }),
      TODAY,
      "en-GB",
    ),
    "Missed: expected Tue 15 Sept",
  );
  assert.equal(daysUntil("2026-11-01", TODAY), 21);
  assert.equal(inDays(1), "tomorrow");
  assert.equal(leadDaysText(0), "on the day");
  assert.equal(leadDaysText(1), "1 day before");
  assert.equal(
    totalNote({
      items: [],
      totals: { currency: "EUR", monthly: eur(0), confirmed: 2, excluded: 1 },
    }),
    "2 confirmed subscriptions; 1 in other currencies isn't counted (totals use EUR)",
  );
});

test("the tab groups suggestions, confirmed ones by next payment, missed, and ignored", () => {
  const groups = subscriptionGroups([
    subscription({ id: "a", name: "Spotify", nextDate: "2026-10-20" }),
    subscription({ id: "b", name: "Netflix", nextDate: "2026-10-15" }),
    subscription({
      id: "c",
      name: "Gym",
      renewal: "missed",
      nextDate: "2026-09-01",
    }),
    subscription({ id: "d", name: "FitX", state: "suggested", renewal: null }),
    subscription({ id: "e", name: "Old", state: "ignored", renewal: null }),
  ]);
  assert.deepEqual(
    groups.suggested.map((item) => item.id),
    ["d"],
  );
  assert.deepEqual(
    groups.confirmed.map((item) => item.id),
    ["b", "a"],
  );
  assert.deepEqual(
    groups.missed.map((item) => item.id),
    ["c"],
  );
  assert.deepEqual(
    groups.ignored.map((item) => item.id),
    ["e"],
  );
});

test("a transaction knows its subscription, confirmed first, never an ignored one", () => {
  const items = [
    subscription({ id: "ignored", state: "ignored", transactionIds: ["t1"] }),
    subscription({
      id: "suggested",
      state: "suggested",
      transactionIds: ["t1", "t2"],
    }),
    subscription({ id: "confirmed", transactionIds: ["t2"] }),
  ];
  assert.equal(subscriptionOf(items, "t1")?.id, "suggested");
  assert.equal(subscriptionOf(items, "t2")?.id, "confirmed");
  assert.equal(subscriptionOf(items, "t9"), undefined);
  const updated = withSubscription(
    list(items),
    subscription({ id: "confirmed", name: "Renamed" }),
  );
  assert.equal(updated.items[2].name, "Renamed");
  assert.equal(
    withSubscription(list(items), subscription({ id: "new" })).items.length,
    4,
  );
});

test("the edit form sends only what changed", () => {
  const netflix = subscription({ categoryId: "streaming" });
  const draft = subscriptionDraft(netflix, "de-DE");
  assert.deepEqual(draft, {
    name: "Netflix",
    cadence: "monthly",
    amount: "13,99",
    categoryId: "streaming",
  });
  assert.deepEqual(updateSubscriptionBody(netflix, draft), {
    body: undefined,
    errors: {},
  });
  assert.deepEqual(
    updateSubscriptionBody(netflix, {
      name: " Netflix Premium ",
      cadence: "yearly",
      amount: "179,88",
      categoryId: "",
    }).body,
    {
      name: "Netflix Premium",
      cadence: "yearly",
      amountMinor: 17988,
      categoryId: null,
    },
  );
  assert.deepEqual(
    updateSubscriptionBody(netflix, { ...draft, name: " ", amount: "0" })
      .errors,
    {
      name: "Enter a name of 1 to 100 characters.",
      amount: "Enter an amount like 12.99, with at most 2 decimals.",
    },
  );
});

test("the alert settings become a PUT body, with the fields checked", () => {
  const settings: FinanceSettings = {
    currency: "EUR",
    largeDebit: { enabled: true, threshold: eur(25000) },
    lowBalance: { enabled: false, threshold: eur(10000) },
    renewals: { enabled: true, leadDays: 3 },
    updatedAt: STAMP,
  };
  const draft = settingsDraft(settings, "en-GB");
  assert.deepEqual(draft, {
    largeDebitEnabled: true,
    largeDebit: "250.00",
    lowBalanceEnabled: false,
    lowBalance: "100.00",
    renewalsEnabled: true,
    leadDays: "3",
  });
  assert.deepEqual(
    settingsBody(
      { ...draft, largeDebit: "300", lowBalance: "0", leadDays: "0" },
      "EUR",
    ),
    {
      body: {
        largeDebit: { enabled: true, thresholdMinor: 30000 },
        lowBalance: { enabled: false, thresholdMinor: 0 },
        renewals: { enabled: true, leadDays: 0 },
      },
      errors: {},
    },
  );
  assert.deepEqual(
    settingsBody(
      { ...draft, largeDebit: "0", lowBalance: "-5", leadDays: "31" },
      "EUR",
    ).errors,
    {
      largeDebit: "Enter an amount like 250.00, with at most 2 decimals.",
      lowBalance: "Enter an amount like 100.00, with at most 2 decimals.",
      leadDays: "Enter a whole number of days from 0 to 30.",
    },
  );
  assert.equal(
    settingsBody({ ...draft, leadDays: "2.5" }, "EUR").errors.leadDays,
    "Enter a whole number of days from 0 to 30.",
  );
});

test("Home lists upcoming renewals soonest first and low accounts", () => {
  const renewals = homeRenewals(
    list([
      subscription({
        id: "later",
        name: "Spotify",
        renewal: "upcoming",
        nextDate: "2026-10-14",
      }),
      subscription({
        id: "soon",
        name: "Netflix",
        renewal: "upcoming",
        nextDate: "2026-10-12",
      }),
      subscription({
        id: "scheduled",
        renewal: "scheduled",
        nextDate: "2026-10-30",
      }),
      subscription({
        id: "suggested",
        state: "suggested",
        renewal: "upcoming",
        nextDate: "2026-10-12",
      }),
      subscription({ id: "a", renewal: "upcoming", nextDate: "2026-10-13" }),
      subscription({ id: "b", renewal: "upcoming", nextDate: "2026-10-13" }),
    ]),
    TODAY,
  );
  assert.deepEqual(
    renewals.items.map((item) => [item.id, item.days]),
    [
      ["soon", 1],
      ["a", 2],
      ["b", 2],
    ],
  );
  assert.equal(renewals.more, true);
  const bank = { kind: "bank" };
  const manual = { kind: "manual" };
  const accounts = [
    {
      id: "low",
      lowBalance: true,
      archived: false,
      builtIn: false,
      source: bank,
    },
    {
      id: "fine",
      lowBalance: false,
      archived: false,
      builtIn: false,
      source: bank,
    },
    // Never Cash or a manual account, even if marked.
    {
      id: "cash",
      lowBalance: true,
      archived: false,
      builtIn: true,
      source: manual,
    },
    {
      id: "manual",
      lowBalance: true,
      archived: false,
      builtIn: false,
      source: manual,
    },
  ] as Account[];
  assert.deepEqual(
    lowBalanceAccounts(accounts).map((account) => account.id),
    ["low"],
  );
});
