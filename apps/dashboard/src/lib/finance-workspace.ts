import { financeClient } from "@/lib/api/finance-client";
import type { components, operations } from "@/lib/api/generated/finance";
import {
  amountInput,
  directionOf,
  minorDigits,
  parseAmount,
  signed,
} from "@/lib/finance-money";

// The Finance workspace's data and its pure rules: filters as API queries,
// form drafts as request bodies, and the texts the page shows. Everything
// but the fetch helpers is pure and unit-tested.

type Schemas = components["schemas"];

export type Account = Schemas["Account"];
export type AccountKind = Schemas["AccountKind"];
export type Transaction = Schemas["Transaction"];
export type TransactionDirection = Schemas["TransactionDirection"];
export type TransactionStatus = Schemas["TransactionStatus"];
export type TransactionSourceKind = Schemas["TransactionSourceKind"];
export type Category = Schemas["Category"];
export type CategoryKind = Schemas["CategoryKind"];
export type CategoryRule = Schemas["CategoryRule"];
export type RuleField = Schemas["RuleField"];
export type RuleOperator = Schemas["RuleOperator"];
export type ApplyScope = Schemas["ApplyScope"];
export type ApplyResult = Schemas["ApplyCategoryRulesResult"];
export type RuleMatch = Schemas["RuleMatch"];
export type CsvImport = Schemas["CsvImport"];
export type MonthlySummary = Schemas["MonthlySummary"];
export type CreateTransactionBody = Schemas["CreateManualTransactionRequest"];
export type UpdateTransactionBody = Schemas["UpdateTransactionRequest"];
export type CreateAccountBody = Schemas["CreateAccountRequest"];
export type UpdateAccountBody = Schemas["UpdateAccountRequest"];
export type CreateCategoryBody = Schemas["CreateCategoryRequest"];
export type UpdateCategoryBody = Schemas["UpdateCategoryRequest"];
export type CreateRuleBody = Schemas["CreateCategoryRuleRequest"];
export type UpdateRuleBody = Schemas["UpdateCategoryRuleRequest"];
type TransactionQuery = NonNullable<
  operations["listTransactions"]["parameters"]["query"]
>;

/** Transactions loaded per page and per "Show more". */
export const TRANSACTIONS_PAGE_SIZE = 50;
/** The category filter's value for transactions without a category. */
export const UNCATEGORIZED = "uncategorized";
/** The category filter's "Chosen by AI": every category the AI chose. */
export const BY_AI = "by-ai";
/** A new account's currency when there is no Cash account to copy. */
export const DEFAULT_CURRENCY = "EUR";

export const ACCOUNT_KINDS: { value: AccountKind; label: string }[] = [
  { value: "bank", label: "Bank account" },
  { value: "card", label: "Card" },
  { value: "savings", label: "Savings" },
  { value: "cash", label: "Cash" },
  { value: "other", label: "Other" },
];

export const CATEGORY_KINDS: { value: CategoryKind; label: string }[] = [
  { value: "expense", label: "Expense" },
  { value: "income", label: "Income" },
  { value: "transfer", label: "Transfer" },
];

export const RULE_FIELDS: { value: RuleField; label: string }[] = [
  { value: "counterparty", label: "Counterparty" },
  { value: "description", label: "Description" },
  { value: "any", label: "Counterparty or description" },
];

export const RULE_OPERATORS: { value: RuleOperator; label: string }[] = [
  { value: "contains", label: "contains" },
  { value: "equals", label: "is exactly" },
];

export const SOURCE_LABELS: Record<TransactionSourceKind, string> = {
  manual: "Entered here",
  csv: "CSV import",
  bank: "Bank",
};

/** The colors a category can have, as the API's #rrggbb. */
export const CATEGORY_COLORS: { value: string; label: string }[] = [
  { value: "#ef4444", label: "Red" },
  { value: "#f97316", label: "Orange" },
  { value: "#eab308", label: "Yellow" },
  { value: "#22c55e", label: "Green" },
  { value: "#14b8a6", label: "Teal" },
  { value: "#3b82f6", label: "Blue" },
  { value: "#8b5cf6", label: "Violet" },
  { value: "#ec4899", label: "Pink" },
  { value: "#78716c", label: "Stone" },
];

export function label<T extends string>(
  options: { value: T; label: string }[],
  value: T,
) {
  return options.find((option) => option.value === value)?.label ?? value;
}

// ---- Errors -----------------------------------------------------------------

/** Plain words for the Finance error codes the owner can run into. */
const ERROR_TEXT: Record<string, string> = {
  account_name_taken: "Another account already has this name.",
  account_has_transactions:
    "The currency can't change once the account has transactions.",
  built_in_account:
    "The Cash account is built in: it can be renamed, but not archived or given another kind.",
  account_archived:
    "This account is archived. Restore it to add or import transactions.",
  category_archived: "This category is archived. Restore it to use it again.",
  category_exists: "A category of this kind already has this name.",
  currency_mismatch: "The amount must be in the account's currency.",
  managed_by_source:
    "This transaction was imported: only its category and notes can change, and it can't be deleted.",
  idempotency_conflict:
    "This transaction was already sent with other details. Close the dialog and add it again.",
  unknown_account: "That account no longer exists. Reload the page.",
  unknown_category: "That category no longer exists. Reload the page.",
  account_required: "Choose the account to import into.",
};

function sentence(message: string) {
  const text = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

/**
 * What went wrong, from the API's error envelope: plain words for a known
 * code, else the service's message, else `fallback` (for a network failure
 * or a body without an envelope).
 */
export function financeErrorMessage(error: unknown, fallback: string) {
  const body =
    typeof error === "object" && error !== null && "error" in error
      ? (error as { error?: { code?: unknown; message?: unknown } }).error
      : undefined;
  const code = typeof body?.code === "string" ? body.code : undefined;
  if (code && ERROR_TEXT[code]) return ERROR_TEXT[code];
  return typeof body?.message === "string" && body.message.trim()
    ? sentence(body.message.trim())
    : fallback;
}

/** An Idempotency-Key; crypto.randomUUID needs a secure context. */
export function idempotencyKey() {
  return `dashboard-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

// ---- Loading ----------------------------------------------------------------

/** A request's data, or the message saying why there is none. */
export type Loaded<T> =
  { data: T; error?: undefined } | { data?: undefined; error: string };

/** A request's data, or the error's plain words (`fallback` without any). */
export async function load<T>(
  call: () => Promise<{ data?: T; error?: unknown }>,
  fallback: string,
): Promise<Loaded<T>> {
  try {
    const { data, error } = await call();
    if (error !== undefined || data === undefined) {
      return { error: financeErrorMessage(error, fallback) };
    }
    return { data };
  } catch {
    return { error: fallback };
  }
}

/** Every account, archived ones too (old transactions name them). */
export function fetchAccounts() {
  return load(
    () =>
      financeClient
        .GET("/v1/accounts", { params: { query: { includeArchived: true } } })
        .then(({ data, error }) => ({ data: data?.items, error })),
    "Accounts could not be loaded.",
  );
}

/** Every category, archived ones too (transactions keep them). */
export function fetchCategories() {
  return load(
    () =>
      financeClient
        .GET("/v1/categories", { params: { query: { includeArchived: true } } })
        .then(({ data, error }) => ({ data: data?.items, error })),
    "Categories could not be loaded.",
  );
}

export function fetchRules() {
  return load(
    () =>
      financeClient
        .GET("/v1/categories/rules")
        .then(({ data, error }) => ({ data: data?.items, error })),
    "Category rules could not be loaded.",
  );
}

/** A run of transactions in the API's order; `nextCursor` continues it. */
export type TransactionPage = {
  items: Transaction[];
  nextCursor: string | null;
};

/** One page of the transactions that match the filters. */
export function fetchTransactions(
  filters: TransactionFilters,
  cursor?: string,
  signal?: AbortSignal,
) {
  return load<TransactionPage>(
    () =>
      financeClient
        .GET("/v1/transactions", {
          params: { query: transactionQuery(filters, cursor) },
          signal,
        })
        .then(({ data, error }) => ({
          data: data && {
            items: data.items,
            nextCursor: data.nextCursor ?? null,
          },
          error,
        })),
    "Transactions could not be loaded.",
  );
}

/** The month's summary in one currency (by default the Cash account's). */
export function fetchSummary(month: string, currency?: string) {
  return load(
    () =>
      financeClient.GET("/v1/summaries/monthly", {
        params: { query: { month, ...(currency ? { currency } : {}) } },
      }),
    "The monthly summary could not be loaded.",
  );
}

/** A month's budget (see finance-budget.ts). */
export function fetchBudget(month: string) {
  return load(
    () =>
      financeClient.GET("/v1/budgets/{month}", {
        params: { path: { month } },
      }),
    "The budget could not be loaded.",
  );
}

/** Every subscription, with the monthly total of the confirmed ones (see finance-subscriptions.ts). */
export function fetchSubscriptions() {
  return load(
    () => financeClient.GET("/v1/subscriptions"),
    "Subscriptions could not be loaded.",
  );
}

/** The alert settings (see finance-subscriptions.ts). */
export function fetchSettings() {
  return load(
    () => financeClient.GET("/v1/settings"),
    "The alert settings could not be loaded.",
  );
}

export type FinanceWorkspaceState =
  | {
      kind: "ready";
      accounts: Account[];
      categories: Loaded<Category[]>;
      rules: Loaded<CategoryRule[]>;
      transactions: Loaded<TransactionPage>;
      summary: Loaded<MonthlySummary>;
      budget: Loaded<Schemas["BudgetMonth"]>;
      subscriptions: Loaded<Schemas["SubscriptionList"]>;
      settings: Loaded<Schemas["FinanceSettings"]>;
    }
  | { kind: "unavailable"; error: string };

/**
 * Everything the workspace shows first: accounts, categories and rules, the
 * latest transactions, this month's summary, the budget of `budgetMonth`
 * (this month unless the URL names another), the subscriptions, and the
 * alert settings. Without accounts Finance is unavailable; any other part
 * that fails says so in its own place.
 */
export async function getFinanceWorkspace(
  month: string,
  budgetMonth: string = month,
): Promise<FinanceWorkspaceState> {
  const [
    accounts,
    categories,
    rules,
    transactions,
    summary,
    budget,
    subscriptions,
    settings,
  ] = await Promise.all([
    fetchAccounts(),
    fetchCategories(),
    fetchRules(),
    fetchTransactions(NO_FILTERS),
    fetchSummary(month),
    fetchBudget(budgetMonth),
    fetchSubscriptions(),
    fetchSettings(),
  ]);
  if (accounts.error !== undefined) {
    return { kind: "unavailable", error: accounts.error };
  }
  return {
    kind: "ready",
    accounts: accounts.data,
    categories,
    rules,
    transactions,
    summary,
    budget,
    subscriptions,
    settings,
  };
}

// ---- Filters ----------------------------------------------------------------

/** The transaction list's filters; "" is "any". */
export type TransactionFilters = {
  q: string;
  accountId: string;
  /** A category id, UNCATEGORIZED, BY_AI, or "". */
  categoryId: string;
  /** Earliest booking day (YYYY-MM-DD). */
  from: string;
  /** Latest booking day (YYYY-MM-DD). */
  to: string;
  direction: "" | TransactionDirection;
  status: "" | TransactionStatus;
  source: "" | TransactionSourceKind;
};

export const NO_FILTERS: TransactionFilters = {
  q: "",
  accountId: "",
  categoryId: "",
  from: "",
  to: "",
  direction: "",
  status: "",
  source: "",
};

/** The API's search accepts at most this many characters. */
const MAX_QUERY = 200;

/**
 * The filters as `GET /v1/transactions` query parameters: empty filters are
 * left out, the search is trimmed, and a date range picked backwards is
 * turned around.
 */
export function transactionQuery(
  filters: TransactionFilters,
  cursor?: string,
): TransactionQuery {
  const query: TransactionQuery = { limit: TRANSACTIONS_PAGE_SIZE };
  const q = filters.q.trim().slice(0, MAX_QUERY);
  if (q) query.q = q;
  if (filters.accountId) query.accountId = filters.accountId;
  if (filters.categoryId === BY_AI) query.categorizedBy = "ai";
  else if (filters.categoryId) query.categoryId = filters.categoryId;
  const [from, to] =
    filters.from && filters.to && filters.from > filters.to
      ? [filters.to, filters.from]
      : [filters.from, filters.to];
  if (from) query.from = from;
  if (to) query.to = to;
  if (filters.direction) query.direction = filters.direction;
  if (filters.status) query.status = filters.status;
  if (filters.source) query.source = filters.source;
  if (cursor) query.cursor = cursor;
  return query;
}

/** How many filters are set (the search counts when it has words). */
export function activeFilterCount(filters: TransactionFilters) {
  return (Object.keys(filters) as (keyof TransactionFilters)[]).filter(
    (key) => filters[key].trim() !== "",
  ).length;
}

// ---- Accounts and categories ------------------------------------------------

/** Accounts that take transactions, and archived ones, in the API's order. */
export function splitAccounts(accounts: Account[]) {
  return {
    active: accounts.filter((account) => !account.archived),
    archived: accounts.filter((account) => account.archived),
  };
}

/** Where a transaction goes unless the owner picks: the Cash account. */
export function defaultAccount(accounts: Account[]) {
  const { active } = splitAccounts(accounts);
  return active.find((account) => account.builtIn) ?? active[0];
}

/** The currencies of the accounts, the default account's first. */
export function accountCurrencies(accounts: Account[]) {
  const first = defaultAccount(accounts)?.currency;
  const all = new Set(accounts.map((account) => account.currency));
  return [...all].sort((a, b) =>
    a === first ? -1 : b === first ? 1 : a.localeCompare(b),
  );
}

export function byId<T extends { id: string }>(items: T[]) {
  return new Map(items.map((item) => [item.id, item]));
}

/** Replaces the item with the same id, or appends it. */
export function upsert<T extends { id: string }>(items: T[], item: T) {
  return items.some((current) => current.id === item.id)
    ? items.map((current) => (current.id === item.id ? item : current))
    : [...items, item];
}

/**
 * Categories grouped by kind for a picker, by name within a kind. Archived
 * categories are left out, except `keepId` (a transaction's current one).
 */
export function categoryGroups(categories: Category[], keepId?: string | null) {
  return CATEGORY_KINDS.map(({ value, label: kindLabel }) => ({
    kind: value,
    label: kindLabel,
    items: categories
      .filter(
        (category) =>
          category.kind === value &&
          (!category.archived || category.id === keepId),
      )
      .sort((a, b) => a.name.localeCompare(b.name)),
  })).filter((group) => group.items.length > 0);
}

// ---- Transactions -----------------------------------------------------------

/**
 * A transaction's headline: who, else what for, else its notes. A pending
 * bank transaction with none of them (a card hold, which the bank names only
 * once it is booked) is a "Card payment", or an "Incoming payment"; the row's
 * Pending badge says it is pending.
 */
export function transactionTitle(transaction: Transaction) {
  return (
    transaction.counterparty?.trim() ||
    transaction.description?.trim() ||
    transaction.notes?.trim() ||
    (transaction.status === "pending" && transaction.source.kind === "bank"
      ? transaction.amount.amount < 0
        ? "Card payment"
        : "Incoming payment"
      : "Transaction")
  );
}

/** The line under the headline: what for, when the headline is who. */
export function transactionSubtitle(transaction: Transaction) {
  const title = transactionTitle(transaction);
  const description = transaction.description?.trim();
  return description && description !== title ? description : undefined;
}

/** Why an imported transaction can't be edited, or undefined for manual. */
export function readOnlyReason(transaction: Transaction) {
  if (!transaction.readOnly) return undefined;
  const from =
    transaction.source.kind === "bank"
      ? "your bank"
      : transaction.source.kind === "csv"
        ? "a CSV file"
        : "another source";
  return `Imported from ${from}: only the category and notes can change, and it can't be deleted.`;
}

/** The AI chose the category shown, and nobody changed it since. */
export function chosenByAI(transaction: Transaction) {
  return transaction.categorizedBy === "ai" && transaction.categoryId !== null;
}

/** The short badge for where an imported transaction came from. */
export function sourceBadge(transaction: Transaction) {
  if (transaction.source.kind === "csv") return "CSV";
  if (transaction.source.kind === "bank") return "Bank";
  return undefined;
}

const dayFormats = new Map<string, Intl.DateTimeFormat>();

/**
 * A booking day in `locale` (the browser's): "Fri 9 Oct" in en-GB, with the
 * year when it is not the current one.
 */
export function bookingDayLabel(day: string, today: string, locale: string) {
  const withYear = day.slice(0, 4) !== today.slice(0, 4);
  const id = `${locale}|${withYear}`;
  let format = dayFormats.get(id);
  if (!format) {
    const options: Intl.DateTimeFormatOptions = {
      weekday: "short",
      day: "numeric",
      month: "short",
      ...(withYear ? { year: "numeric" } : {}),
      timeZone: "UTC",
    };
    try {
      format = new Intl.DateTimeFormat(locale, options);
    } catch {
      format = new Intl.DateTimeFormat("en-GB", options);
    }
    dayFormats.set(id, format);
  }
  const date = new Date(`${day}T00:00:00Z`);
  return Number.isNaN(date.getTime()) ? day : format.format(date);
}

/** The add and edit form's fields, as typed. */
export type TransactionDraft = {
  direction: TransactionDirection;
  /** The amount's size as typed; the direction gives the sign. */
  amount: string;
  accountId: string;
  bookingDate: string;
  pending: boolean;
  counterparty: string;
  description: string;
  /** A category id; "" lets the rules choose (new) or clears it (edit). */
  categoryId: string;
  notes: string;
};

export type DraftErrors = Partial<
  Record<"amount" | "accountId" | "bookingDate", string>
>;

/** A new transaction: money out of the Cash account, today. */
export function newTransactionDraft(
  accounts: Account[],
  today: string,
  accountId?: string,
): TransactionDraft {
  const { active } = splitAccounts(accounts);
  return {
    direction: "out",
    amount: "",
    accountId: active.some((account) => account.id === accountId)
      ? (accountId as string)
      : (defaultAccount(accounts)?.id ?? ""),
    bookingDate: today,
    pending: false,
    counterparty: "",
    description: "",
    categoryId: "",
    notes: "",
  };
}

/** A saved transaction as the edit form shows it. */
export function transactionDraft(
  transaction: Transaction,
  locale: string,
): TransactionDraft {
  return {
    direction: directionOf(transaction.amount.amount),
    amount: amountInput(
      transaction.amount.amount,
      transaction.amount.currency,
      locale,
    ),
    accountId: transaction.accountId,
    bookingDate: transaction.bookingDate,
    pending: transaction.status === "pending",
    counterparty: transaction.counterparty ?? "",
    description: transaction.description ?? "",
    categoryId: transaction.categoryId ?? "",
    notes: transaction.notes ?? "",
  };
}

/** The signed amount in minor units, or the form's errors. */
function checkDraft(draft: TransactionDraft, account: Account | undefined) {
  const errors: DraftErrors = {};
  if (!account) errors.accountId = "Choose an account.";
  if (!draft.bookingDate) errors.bookingDate = "Choose a date.";
  const currency = account?.currency ?? DEFAULT_CURRENCY;
  const size = draft.amount.trim()
    ? parseAmount(draft.amount, currency)
    : undefined;
  if (!draft.amount.trim()) errors.amount = "Enter an amount.";
  else if (size === undefined) {
    const digits = minorDigits(currency);
    errors.amount = digits
      ? `Enter an amount like 12.50, with at most ${digits} decimals.`
      : "Enter a whole amount, like 1200.";
  } else if (size === 0) errors.amount = "Enter an amount above zero.";
  const amount =
    size === undefined ? undefined : signed(Math.abs(size), draft.direction);
  return { errors, amount, currency };
}

const text = (value: string) => value.trim();

/** The request that records a new manual transaction, or the errors. */
export function createTransactionBody(
  draft: TransactionDraft,
  account: Account | undefined,
): { body?: CreateTransactionBody; errors: DraftErrors } {
  const { errors, amount, currency } = checkDraft(draft, account);
  if (Object.keys(errors).length || amount === undefined || !account) {
    return { errors };
  }
  const body: CreateTransactionBody = {
    amount: { amount, currency },
    accountId: account.id,
    bookingDate: draft.bookingDate,
    status: draft.pending ? "pending" : "booked",
  };
  if (text(draft.counterparty)) body.counterparty = text(draft.counterparty);
  if (text(draft.description)) body.description = text(draft.description);
  if (text(draft.notes)) body.notes = text(draft.notes);
  if (draft.categoryId) body.categoryId = draft.categoryId;
  return { body, errors };
}

/** "" as null, for clearing a nullable field. */
const orNull = (value: string) => text(value) || null;

/**
 * The PATCH that turns `transaction` into the draft: only the fields that
 * changed, with null clearing one. An imported transaction sends only its
 * category and notes. `body` is undefined (without errors) when nothing
 * changed.
 */
export function updateTransactionBody(
  transaction: Transaction,
  draft: TransactionDraft,
  account: Account | undefined,
): { body?: UpdateTransactionBody; errors: DraftErrors } {
  const body: UpdateTransactionBody = {};
  if ((draft.categoryId || null) !== transaction.categoryId) {
    body.categoryId = draft.categoryId || null;
  }
  if (orNull(draft.notes) !== (transaction.notes ?? null)) {
    body.notes = orNull(draft.notes);
  }
  if (!transaction.readOnly) {
    const { errors, amount, currency } = checkDraft(draft, account);
    if (Object.keys(errors).length || amount === undefined || !account) {
      return { errors };
    }
    if (account.id !== transaction.accountId) body.accountId = account.id;
    if (
      amount !== transaction.amount.amount ||
      currency !== transaction.amount.currency
    ) {
      body.amount = { amount, currency };
    }
    if (draft.bookingDate !== transaction.bookingDate) {
      body.bookingDate = draft.bookingDate;
    }
    const status = draft.pending ? "pending" : "booked";
    if (status !== transaction.status) body.status = status;
    if (orNull(draft.counterparty) !== (transaction.counterparty ?? null)) {
      body.counterparty = orNull(draft.counterparty);
    }
    if (orNull(draft.description) !== (transaction.description ?? null)) {
      body.description = orNull(draft.description);
    }
  }
  return { body: Object.keys(body).length ? body : undefined, errors: {} };
}

// ---- Account form -----------------------------------------------------------

export type AccountDraft = {
  name: string;
  kind: AccountKind;
  currency: string;
  /** Signed, as typed: "-50,00" for an overdrawn start. */
  openingBalance: string;
};

export type AccountErrors = Partial<
  Record<"name" | "currency" | "openingBalance", string>
>;

/** An amount as signed editable text: "-50.00"; "" for zero. */
function signedInput(minor: number, currency: string, locale: string) {
  if (minor === 0) return "";
  return `${minor < 0 ? "-" : ""}${amountInput(minor, currency, locale)}`;
}

/** The account form for `account`, or for a new bank account. */
export function accountDraft(
  account: Account | undefined,
  accounts: Account[],
  locale: string,
): AccountDraft {
  if (!account) {
    return {
      name: "",
      kind: "bank",
      currency: defaultAccount(accounts)?.currency ?? DEFAULT_CURRENCY,
      openingBalance: "",
    };
  }
  return {
    name: account.name,
    kind: account.kind,
    currency: account.currency,
    openingBalance: signedInput(
      account.openingBalance.amount,
      account.currency,
      locale,
    ),
  };
}

function checkAccount(draft: AccountDraft) {
  const errors: AccountErrors = {};
  const name = draft.name.trim();
  const currency = draft.currency.trim().toUpperCase();
  if (!name) errors.name = "Enter a name.";
  else if (name.length > 100) errors.name = "Use at most 100 characters.";
  if (!/^[A-Z]{3}$/.test(currency)) {
    errors.currency = "Enter a three-letter currency code, like EUR.";
  }
  const opening = draft.openingBalance.trim()
    ? parseAmount(draft.openingBalance, currency)
    : 0;
  if (opening === undefined) {
    errors.openingBalance = "Enter an amount like 250.00, or leave it empty.";
  }
  return { errors, name, currency, opening: opening ?? 0 };
}

/** The request that creates an account from the form, or the errors. */
export function createAccountBody(draft: AccountDraft): {
  body?: CreateAccountBody;
  errors: AccountErrors;
} {
  const { errors, name, currency, opening } = checkAccount(draft);
  if (Object.keys(errors).length) return { errors };
  return {
    body: {
      name,
      kind: draft.kind,
      currency,
      ...(opening ? { openingBalanceMinor: opening } : {}),
    },
    errors,
  };
}

/** The PATCH with the account form's changes; undefined when none. */
export function updateAccountBody(
  account: Account,
  draft: AccountDraft,
): { body?: UpdateAccountBody; errors: AccountErrors } {
  const { errors, name, currency, opening } = checkAccount(draft);
  if (Object.keys(errors).length) return { errors };
  const body: UpdateAccountBody = {};
  if (name !== account.name) body.name = name;
  if (draft.kind !== account.kind) body.kind = draft.kind;
  if (currency !== account.currency) body.currency = currency;
  if (opening !== account.openingBalance.amount) {
    body.openingBalanceMinor = opening;
  }
  return { body: Object.keys(body).length ? body : undefined, errors };
}

// ---- Categories and rules ---------------------------------------------------

export type CategoryDraft = { name: string; kind: CategoryKind; color: string };

export function categoryDraft(category?: Category): CategoryDraft {
  return {
    name: category?.name ?? "",
    kind: category?.kind ?? "expense",
    color: category?.color ?? "",
  };
}

function checkCategoryName(name: string) {
  if (!name) return "Enter a name.";
  if (name.length > 100) return "Use at most 100 characters.";
  return undefined;
}

export function createCategoryBody(draft: CategoryDraft): {
  body?: CreateCategoryBody;
  error?: string;
} {
  const name = draft.name.trim();
  const error = checkCategoryName(name);
  if (error) return { error };
  return {
    body: {
      name,
      kind: draft.kind,
      ...(draft.color ? { color: draft.color } : {}),
    },
  };
}

/** The PATCH with the category form's changes; undefined when none. */
export function updateCategoryBody(
  category: Category,
  draft: CategoryDraft,
): { body?: UpdateCategoryBody; error?: string } {
  const name = draft.name.trim();
  const error = checkCategoryName(name);
  if (error) return { error };
  const body: UpdateCategoryBody = {};
  if (name !== category.name) body.name = name;
  if (draft.kind !== category.kind) body.kind = draft.kind;
  if ((draft.color || null) !== category.color)
    body.color = draft.color || null;
  return { body: Object.keys(body).length ? body : undefined };
}

export type RuleDraft = {
  categoryId: string;
  field: RuleField;
  operator: RuleOperator;
  text: string;
  /** Optional, as typed; lower is tried first. */
  priority: string;
};

export type RuleErrors = Partial<
  Record<"categoryId" | "text" | "priority", string>
>;

export function ruleDraft(rule?: CategoryRule): RuleDraft {
  return {
    categoryId: rule?.categoryId ?? "",
    field: rule?.match.field ?? "counterparty",
    operator: rule?.match.operator ?? "contains",
    text: rule?.match.text ?? "",
    priority: rule ? String(rule.priority) : "",
  };
}

function checkRule(draft: RuleDraft) {
  const errors: RuleErrors = {};
  const matchText = draft.text.trim();
  if (!draft.categoryId) errors.categoryId = "Choose a category.";
  if (!matchText) errors.text = "Enter the text to look for.";
  else if (matchText.length > 200) errors.text = "Use at most 200 characters.";
  const priorityText = draft.priority.trim();
  const priority = priorityText ? Number(priorityText) : undefined;
  if (priority !== undefined && !Number.isSafeInteger(priority)) {
    errors.priority = "Enter a whole number, or leave it empty.";
  }
  return {
    errors,
    match: { field: draft.field, operator: draft.operator, text: matchText },
    priority,
  };
}

export function createRuleBody(draft: RuleDraft): {
  body?: CreateRuleBody;
  errors: RuleErrors;
} {
  const { errors, match, priority } = checkRule(draft);
  if (Object.keys(errors).length) return { errors };
  return {
    body: {
      categoryId: draft.categoryId,
      match,
      ...(priority !== undefined ? { priority } : {}),
    },
    errors,
  };
}

/** The PATCH with the rule form's changes; undefined when none. */
export function updateRuleBody(
  rule: CategoryRule,
  draft: RuleDraft,
): { body?: UpdateRuleBody; errors: RuleErrors } {
  const { errors, match, priority } = checkRule(draft);
  if (Object.keys(errors).length) return { errors };
  const body: UpdateRuleBody = {};
  if (draft.categoryId !== rule.categoryId) body.categoryId = draft.categoryId;
  if (
    match.field !== rule.match.field ||
    match.operator !== rule.match.operator ||
    match.text !== rule.match.text
  ) {
    body.match = match;
  }
  if (priority !== undefined && priority !== rule.priority) {
    body.priority = priority;
  }
  return { body: Object.keys(body).length ? body : undefined, errors };
}

/** A rule as a sentence: 'Counterparty contains "REWE"'. */
export function ruleText(rule: Pick<CategoryRule, "match">) {
  const { field, operator, text: matchText } = rule.match;
  return `${label(RULE_FIELDS, field)} ${label(RULE_OPERATORS, operator)} “${matchText}”`;
}

const count = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`;

/** What applying the rules did, in one line. */
export function applyResultText(result: ApplyResult) {
  const parts = [`${result.categorized} categorized`];
  if (result.recategorized) parts.push(`${result.recategorized} recategorized`);
  if (result.cleared) parts.push(`${result.cleared} cleared`);
  parts.push(`${result.unchanged} unchanged`);
  return `Checked ${count(result.examined, "transaction")}: ${parts.join(", ")}.`;
}

// ---- AI categories ----------------------------------------------------------

/** Shorter names occur inside other words ("dm" in "Admiral"). */
const MIN_CONTAINS_LETTERS = 5;

/**
 * The safer rule for a counterparty: "contains" its name, without the
 * store numbers, dates, and references at the end that change from payment
 * to payment ("SMYTHS TOYS 0423" → contains "SMYTHS TOYS"); "equals" the
 * whole counterparty when the name is too short to stand alone ("dm",
 * "IKEA"), as Finance's own default rules do. Undefined without a
 * counterparty.
 */
export function ruleMatchFor(
  counterparty: string | null,
): RuleMatch | undefined {
  const text = (counterparty ?? "").trim().replace(/\s+/g, " ").slice(0, 200);
  if (!text) return undefined;
  const words = text.split(" ");
  while (words.length > 1 && /\d/.test(words[words.length - 1])) words.pop();
  const name = words.join(" ");
  if (name.replace(/[^\p{L}\p{N}]/gu, "").length < MIN_CONTAINS_LETTERS) {
    return { field: "counterparty", operator: "equals", text };
  }
  return { field: "counterparty", operator: "contains", text: name };
}

/** "Always put Kneipe41 in Eating out?", offered after a correction. */
export type RuleOffer = {
  counterparty: string;
  categoryId: string;
  categoryName: string;
  match: RuleMatch;
};

/**
 * What to offer after the owner saved a transaction: when they changed a
 * category the AI chose to another one, a rule for its counterparty, so
 * the correction sticks. Never when the AI's category stays, the category
 * was cleared, or there is no counterparty to match.
 */
export function ruleOffer(
  before: Transaction | undefined,
  after: Transaction,
  categories: Category[],
): RuleOffer | undefined {
  if (!before || !chosenByAI(before) || after.categorizedBy !== "manual")
    return undefined;
  if (!after.categoryId || after.categoryId === before.categoryId)
    return undefined;
  const match = ruleMatchFor(after.counterparty);
  const category = categories.find((item) => item.id === after.categoryId);
  if (!match || !category) return undefined;
  return {
    counterparty: after.counterparty!.trim().replace(/\s+/g, " "),
    categoryId: category.id,
    categoryName: category.name,
    match,
  };
}

/** The offer as a question. */
export function ruleOfferText(offer: RuleOffer) {
  return `Always put “${offer.counterparty}” in ${offer.categoryName}?`;
}

/**
 * What creating the offered rule and applying the rules did: the rule, then
 * what applying changed (every rule's work, as Apply rules says it).
 */
export function ruleCreatedText(offer: RuleOffer, result: ApplyResult) {
  return `Rule created: ${ruleText(offer)} → ${offer.categoryName}. ${applyResultText(result)}`;
}

/** What a CSV import did, in one line (row errors are listed apart). */
export function importResultText(result: CsvImport) {
  const parts = [
    `Imported ${result.imported}`,
    `${count(result.duplicates, "duplicate")} skipped`,
    `${result.failed} failed`,
  ];
  const categorized = result.categorized
    ? ` ${result.categorized} got a category from the rules.`
    : "";
  return `${parts.join(", ")}.${categorized}`;
}
