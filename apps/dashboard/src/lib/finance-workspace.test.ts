import assert from "node:assert/strict";
import { test } from "node:test";

import {
  type Account,
  type Category,
  type CategoryRule,
  type Transaction,
  NO_FILTERS,
  TRANSACTIONS_PAGE_SIZE,
  UNCATEGORIZED,
  accountCurrencies,
  accountDraft,
  activeFilterCount,
  applyResultText,
  bookingDayLabel,
  categoryDraft,
  categoryGroups,
  createAccountBody,
  createCategoryBody,
  createRuleBody,
  createTransactionBody,
  defaultAccount,
  financeErrorMessage,
  importResultText,
  newTransactionDraft,
  readOnlyReason,
  ruleDraft,
  ruleText,
  sourceBadge,
  splitAccounts,
  transactionDraft,
  transactionQuery,
  transactionSubtitle,
  transactionTitle,
  updateAccountBody,
  updateCategoryBody,
  updateRuleBody,
  updateTransactionBody,
  upsert,
} from "./finance-workspace";

const STAMP = "2026-10-01T00:00:00Z";

function account(overrides: Partial<Account> = {}): Account {
  return {
    id: "cash",
    name: "Cash",
    kind: "cash",
    currency: "EUR",
    openingBalance: { amount: 0, currency: "EUR" },
    balance: { amount: 0, currency: "EUR" },
    bookedBalance: { amount: 0, currency: "EUR" },
    pending: { amount: 0, currency: "EUR" },
    balanceAsOf: null,
    bankBalance: null,
    available: null,
    transactionCount: 0,
    lowBalance: false,
    builtIn: true,
    archived: false,
    archivedAt: null,
    source: {
      kind: "manual",
      connectionId: null,
      externalId: null,
      provider: null,
      institution: null,
      ibanMasked: null,
      syncedAt: null,
    },
    createdAt: STAMP,
    updatedAt: STAMP,
    ...overrides,
  };
}

const cash = account();
const giro = account({
  id: "giro",
  name: "Girokonto",
  kind: "bank",
  builtIn: false,
});
const usd = account({
  id: "usd",
  name: "Travel card",
  kind: "card",
  currency: "USD",
  builtIn: false,
});

function transaction(overrides: Partial<Transaction> = {}): Transaction {
  return {
    id: "t1",
    accountId: "cash",
    amount: { amount: -1250, currency: "EUR" },
    bookingDate: "2026-10-09",
    valueDate: null,
    status: "booked",
    counterparty: "Bäckerei Müller",
    description: "Brötchen",
    notes: null,
    categoryId: null,
    categorizedBy: null,
    categoryRuleId: null,
    merchantCategoryCode: null,
    bankReference: null,
    source: { kind: "manual", connectionId: null, externalId: null },
    readOnly: false,
    createdAt: STAMP,
    updatedAt: STAMP,
    ...overrides,
  };
}

function category(overrides: Partial<Category> = {}): Category {
  return {
    id: "groceries",
    name: "Groceries",
    kind: "expense",
    color: null,
    systemKey: null,
    archived: false,
    archivedAt: null,
    createdAt: STAMP,
    updatedAt: STAMP,
    ...overrides,
  };
}

const rule: CategoryRule = {
  id: "r1",
  categoryId: "groceries",
  match: { field: "counterparty", operator: "contains", text: "REWE" },
  priority: 10,
  createdAt: STAMP,
  updatedAt: STAMP,
};

// ---- Filters ----------------------------------------------------------------

test("transactionQuery leaves out empty filters", () => {
  assert.deepEqual(transactionQuery(NO_FILTERS), {
    limit: TRANSACTIONS_PAGE_SIZE,
  });
  assert.deepEqual(transactionQuery({ ...NO_FILTERS, q: "   " }), {
    limit: TRANSACTIONS_PAGE_SIZE,
  });
});

test("transactionQuery maps every filter and the cursor", () => {
  assert.deepEqual(
    transactionQuery(
      {
        q: "  bäckerei  ",
        accountId: "cash",
        categoryId: UNCATEGORIZED,
        from: "2026-10-01",
        to: "2026-10-31",
        direction: "out",
        status: "pending",
        source: "csv",
      },
      "next-page",
    ),
    {
      limit: TRANSACTIONS_PAGE_SIZE,
      q: "bäckerei",
      accountId: "cash",
      categoryId: "uncategorized",
      from: "2026-10-01",
      to: "2026-10-31",
      direction: "out",
      status: "pending",
      source: "csv",
      cursor: "next-page",
    },
  );
});

test("transactionQuery turns a backwards date range around", () => {
  const query = transactionQuery({
    ...NO_FILTERS,
    from: "2026-10-31",
    to: "2026-10-01",
  });
  assert.equal(query.from, "2026-10-01");
  assert.equal(query.to, "2026-10-31");
});

test("transactionQuery keeps the search within the API's limit", () => {
  const query = transactionQuery({ ...NO_FILTERS, q: "x".repeat(300) });
  assert.equal(query.q?.length, 200);
});

test("activeFilterCount counts set filters", () => {
  assert.equal(activeFilterCount(NO_FILTERS), 0);
  assert.equal(activeFilterCount({ ...NO_FILTERS, q: " " }), 0);
  assert.equal(
    activeFilterCount({ ...NO_FILTERS, q: "rewe", direction: "in" }),
    2,
  );
});

// ---- Errors -----------------------------------------------------------------

test("financeErrorMessage explains known codes in plain words", () => {
  assert.equal(
    financeErrorMessage(
      { error: { code: "managed_by_source", message: "x" }, request_id: "r" },
      "fallback",
    ),
    "This transaction was imported: only its category and notes can change, and it can't be deleted.",
  );
});

test("financeErrorMessage uses the service's message, else the fallback", () => {
  assert.equal(
    financeErrorMessage(
      { error: { code: "invalid_csv", message: "missing column date" } },
      "fallback",
    ),
    "Missing column date.",
  );
  assert.equal(financeErrorMessage(undefined, "fallback"), "fallback");
  assert.equal(financeErrorMessage("Bad Gateway", "fallback"), "fallback");
  assert.equal(
    financeErrorMessage({ error: { code: "x", message: " " } }, "fallback"),
    "fallback",
  );
});

// ---- Accounts and categories ------------------------------------------------

test("defaultAccount is the built-in Cash account while it is active", () => {
  assert.equal(defaultAccount([giro, cash])?.id, "cash");
  assert.equal(defaultAccount([account({ archived: true }), giro])?.id, "giro");
  assert.equal(defaultAccount([]), undefined);
});

test("splitAccounts keeps the API's order", () => {
  const archived = account({ id: "old", archived: true, builtIn: false });
  assert.deepEqual(splitAccounts([cash, archived, giro]), {
    active: [cash, giro],
    archived: [archived],
  });
});

test("accountCurrencies lists the default account's currency first", () => {
  assert.deepEqual(accountCurrencies([usd, cash, giro]), ["EUR", "USD"]);
  assert.deepEqual(accountCurrencies([]), []);
});

test("categoryGroups groups by kind and hides archived categories", () => {
  const groups = categoryGroups([
    category({ id: "salary", name: "Salary", kind: "income" }),
    category({ id: "rent", name: "Rent" }),
    category(),
    category({ id: "old", name: "Old", archived: true }),
  ]);
  assert.deepEqual(
    groups.map((group) => [group.label, group.items.map((item) => item.id)]),
    [
      ["Expense", ["groceries", "rent"]],
      ["Income", ["salary"]],
    ],
  );
  const kept = categoryGroups([category({ id: "old", archived: true })], "old");
  assert.deepEqual(
    kept[0].items.map((item) => item.id),
    ["old"],
  );
});

test("upsert replaces by id or appends", () => {
  const renamed = category({ name: "Food" });
  assert.deepEqual(upsert([category()], renamed), [renamed]);
  assert.equal(upsert([category()], category({ id: "new" })).length, 2);
});

// ---- Transactions -----------------------------------------------------------

test("transactionTitle prefers the counterparty, then the description", () => {
  assert.equal(transactionTitle(transaction()), "Bäckerei Müller");
  assert.equal(transactionSubtitle(transaction()), "Brötchen");
  const noCounterparty = transaction({ counterparty: null });
  assert.equal(transactionTitle(noCounterparty), "Brötchen");
  assert.equal(transactionSubtitle(noCounterparty), undefined);
  assert.equal(
    transactionTitle(
      transaction({ counterparty: " ", description: null, notes: null }),
    ),
    "Transaction",
  );
});

test("a pending bank transaction without any text is a card payment", () => {
  const hold = transaction({
    status: "pending",
    counterparty: null,
    description: null,
    source: { kind: "bank", connectionId: "c1", externalId: "h-1" },
  });
  assert.equal(transactionTitle(hold), "Card payment");
  assert.equal(transactionSubtitle(hold), undefined);
  assert.equal(
    transactionTitle({ ...hold, amount: { amount: 500, currency: "EUR" } }),
    "Incoming payment",
  );
  assert.equal(
    transactionTitle({ ...hold, notes: "Hotel deposit" }),
    "Hotel deposit",
  );
  // Booked, or entered by hand, it stays a plain transaction.
  assert.equal(transactionTitle({ ...hold, status: "booked" }), "Transaction");
  assert.equal(
    transactionTitle({
      ...hold,
      source: { kind: "manual", connectionId: null, externalId: null },
    }),
    "Transaction",
  );
});

test("imported transactions say why they are read-only", () => {
  const csv = transaction({
    readOnly: true,
    source: { kind: "csv", connectionId: null, externalId: "row-1" },
  });
  assert.equal(sourceBadge(csv), "CSV");
  assert.match(readOnlyReason(csv) ?? "", /^Imported from a CSV file: only/);
  assert.equal(sourceBadge(transaction()), undefined);
  assert.equal(readOnlyReason(transaction()), undefined);
});

test("bookingDayLabel shows the year only for other years", () => {
  assert.equal(
    bookingDayLabel("2026-10-09", "2026-10-10", "en-GB"),
    "Fri 9 Oct",
  );
  assert.equal(
    bookingDayLabel("2025-12-31", "2026-10-10", "en-GB"),
    "Wed, 31 Dec 2025",
  );
  assert.equal(
    bookingDayLabel("not-a-day", "2026-10-10", "en-GB"),
    "not-a-day",
  );
});

test("a new transaction is money out of Cash, today", () => {
  assert.deepEqual(newTransactionDraft([giro, cash], "2026-10-10"), {
    direction: "out",
    amount: "",
    accountId: "cash",
    bookingDate: "2026-10-10",
    pending: false,
    counterparty: "",
    description: "",
    categoryId: "",
    notes: "",
  });
  assert.equal(
    newTransactionDraft([giro, cash], "2026-10-10", "giro").accountId,
    "giro",
  );
});

test("createTransactionBody signs the amount by direction", () => {
  const draft = {
    ...newTransactionDraft([cash], "2026-10-10"),
    amount: "12,50",
    counterparty: " Bäckerei Müller ",
  };
  assert.deepEqual(createTransactionBody(draft, cash), {
    body: {
      amount: { amount: -1250, currency: "EUR" },
      accountId: "cash",
      bookingDate: "2026-10-10",
      status: "booked",
      counterparty: "Bäckerei Müller",
    },
    errors: {},
  });
  const income = createTransactionBody(
    { ...draft, direction: "in", amount: "-2.000,00", pending: true },
    cash,
  );
  assert.deepEqual(income.body?.amount, { amount: 200000, currency: "EUR" });
  assert.equal(income.body?.status, "pending");
});

test("createTransactionBody explains a missing or bad amount", () => {
  const draft = newTransactionDraft([cash], "2026-10-10");
  assert.deepEqual(createTransactionBody(draft, cash).errors, {
    amount: "Enter an amount.",
  });
  assert.equal(
    createTransactionBody({ ...draft, amount: "1.999" }, cash).errors.amount,
    undefined,
  );
  assert.equal(
    createTransactionBody({ ...draft, amount: "1.2345" }, cash).errors.amount,
    "Enter an amount like 12.50, with at most 2 decimals.",
  );
  assert.equal(
    createTransactionBody({ ...draft, amount: "0" }, cash).errors.amount,
    "Enter an amount above zero.",
  );
  assert.equal(
    createTransactionBody({ ...draft, amount: "5" }, undefined).errors
      .accountId,
    "Choose an account.",
  );
});

test("transactionDraft shows a saved transaction for editing", () => {
  assert.deepEqual(
    transactionDraft(transaction({ notes: "Sunday" }), "de-DE"),
    {
      direction: "out",
      amount: "12,50",
      accountId: "cash",
      bookingDate: "2026-10-09",
      pending: false,
      counterparty: "Bäckerei Müller",
      description: "Brötchen",
      categoryId: "",
      notes: "Sunday",
    },
  );
});

test("updateTransactionBody sends only what changed, null clearing", () => {
  const saved = transaction();
  const draft = transactionDraft(saved, "en-GB");
  assert.deepEqual(updateTransactionBody(saved, draft, cash), {
    body: undefined,
    errors: {},
  });
  assert.deepEqual(
    updateTransactionBody(
      saved,
      {
        ...draft,
        amount: "13",
        description: "",
        categoryId: "groceries",
        accountId: "giro",
      },
      giro,
    ).body,
    {
      categoryId: "groceries",
      accountId: "giro",
      amount: { amount: -1300, currency: "EUR" },
      description: null,
    },
  );
});

test("updateTransactionBody moves to another currency's account", () => {
  const saved = transaction();
  const body = updateTransactionBody(
    saved,
    { ...transactionDraft(saved, "en-GB"), accountId: "usd" },
    usd,
  ).body;
  assert.deepEqual(body, {
    accountId: "usd",
    amount: { amount: -1250, currency: "USD" },
  });
});

test("updateTransactionBody clears a category by hand", () => {
  const saved = transaction({ categoryId: "groceries", categorizedBy: "rule" });
  assert.deepEqual(
    updateTransactionBody(
      saved,
      { ...transactionDraft(saved, "en-GB"), categoryId: "" },
      cash,
    ).body,
    { categoryId: null },
  );
});

test("an imported transaction sends only its category and notes", () => {
  const imported = transaction({
    readOnly: true,
    source: { kind: "csv", connectionId: null, externalId: "row-1" },
  });
  const draft = {
    ...transactionDraft(imported, "en-GB"),
    amount: "999",
    counterparty: "Changed",
    notes: " Shared with Anna ",
    categoryId: "groceries",
  };
  assert.deepEqual(updateTransactionBody(imported, draft, cash), {
    body: { categoryId: "groceries", notes: "Shared with Anna" },
    errors: {},
  });
});

// ---- Account form -----------------------------------------------------------

test("a new account copies the Cash account's currency", () => {
  assert.deepEqual(accountDraft(undefined, [usd, cash], "en-GB"), {
    name: "",
    kind: "bank",
    currency: "EUR",
    openingBalance: "",
  });
  assert.equal(accountDraft(undefined, [], "en-GB").currency, "EUR");
});

test("createAccountBody checks and normalizes the form", () => {
  assert.deepEqual(
    createAccountBody({
      name: " Girokonto ",
      kind: "bank",
      currency: "eur",
      openingBalance: "-50,00",
    }),
    {
      body: {
        name: "Girokonto",
        kind: "bank",
        currency: "EUR",
        openingBalanceMinor: -5000,
      },
      errors: {},
    },
  );
  assert.deepEqual(
    createAccountBody({
      name: "",
      kind: "bank",
      currency: "euro",
      openingBalance: "abc",
    }).errors,
    {
      name: "Enter a name.",
      currency: "Enter a three-letter currency code, like EUR.",
      openingBalance: "Enter an amount like 250.00, or leave it empty.",
    },
  );
});

test("updateAccountBody sends only what changed", () => {
  const saved = account({
    id: "giro",
    name: "Girokonto",
    kind: "bank",
    builtIn: false,
    openingBalance: { amount: -5000, currency: "EUR" },
  });
  const draft = accountDraft(saved, [saved], "en-GB");
  assert.equal(draft.openingBalance, "-50.00");
  assert.deepEqual(updateAccountBody(saved, draft), {
    body: undefined,
    errors: {},
  });
  assert.deepEqual(
    updateAccountBody(saved, { ...draft, name: "Giro", openingBalance: "" })
      .body,
    { name: "Giro", openingBalanceMinor: 0 },
  );
});

// ---- Categories and rules ---------------------------------------------------

test("category bodies send a color only when one is chosen", () => {
  assert.deepEqual(
    createCategoryBody({ name: " Groceries ", kind: "expense", color: "" }),
    { body: { name: "Groceries", kind: "expense" } },
  );
  assert.deepEqual(createCategoryBody({ ...categoryDraft(), name: "" }), {
    error: "Enter a name.",
  });
  const saved = category({ color: "#22c55e" });
  assert.deepEqual(
    updateCategoryBody(saved, { ...categoryDraft(saved), color: "" }),
    { body: { color: null } },
  );
  assert.deepEqual(updateCategoryBody(saved, categoryDraft(saved)), {
    body: undefined,
  });
});

test("rule bodies carry the match and an optional priority", () => {
  assert.deepEqual(
    createRuleBody({
      ...ruleDraft(),
      categoryId: "groceries",
      text: " REWE ",
    }),
    {
      body: {
        categoryId: "groceries",
        match: { field: "counterparty", operator: "contains", text: "REWE" },
      },
      errors: {},
    },
  );
  assert.deepEqual(createRuleBody({ ...ruleDraft(), priority: "1.5" }).errors, {
    categoryId: "Choose a category.",
    text: "Enter the text to look for.",
    priority: "Enter a whole number, or leave it empty.",
  });
  assert.deepEqual(updateRuleBody(rule, ruleDraft(rule)), {
    body: undefined,
    errors: {},
  });
  assert.deepEqual(
    updateRuleBody(rule, {
      ...ruleDraft(rule),
      operator: "equals",
      priority: "5",
    }).body,
    {
      match: { field: "counterparty", operator: "equals", text: "REWE" },
      priority: 5,
    },
  );
});

test("ruleText reads as a sentence", () => {
  assert.equal(ruleText(rule), "Counterparty contains “REWE”");
  assert.equal(
    ruleText({ match: { field: "any", operator: "equals", text: "Miete" } }),
    "Counterparty or description is exactly “Miete”",
  );
});

test("applyResultText names only the changes that happened", () => {
  assert.equal(
    applyResultText({
      scope: "uncategorized",
      examined: 12,
      categorized: 3,
      recategorized: 0,
      cleared: 0,
      unchanged: 9,
    }),
    "Checked 12 transactions: 3 categorized, 9 unchanged.",
  );
  assert.equal(
    applyResultText({
      scope: "all",
      examined: 1,
      categorized: 0,
      recategorized: 1,
      cleared: 2,
      unchanged: 0,
    }),
    "Checked 1 transaction: 0 categorized, 1 recategorized, 2 cleared, 0 unchanged.",
  );
});

test("importResultText reports imported, duplicate, and failed rows", () => {
  const result = {
    id: "i1",
    accountId: "giro",
    status: "completed" as const,
    imported: 2,
    duplicates: 1,
    failed: 0,
    categorized: 0,
    errors: [],
  };
  assert.equal(
    importResultText(result),
    "Imported 2, 1 duplicate skipped, 0 failed.",
  );
  assert.equal(
    importResultText({ ...result, duplicates: 0, categorized: 2 }),
    "Imported 2, 0 duplicates skipped, 0 failed. 2 got a category from the rules.",
  );
});
