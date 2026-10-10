import type { components } from "@/lib/api/generated/finance";
import {
  type Money,
  amountInput,
  formatMoney,
  minorDigits,
  parseAmount,
} from "@/lib/finance-money";
import { type Account, bookingDayLabel } from "@/lib/finance-workspace";

// Subscriptions (recurring payments Finance detects or the owner marks) and
// the alert settings: the data, the texts the Subscriptions tab, the Alerts
// dialog, and Home show, and the forms' request bodies. Everything but the
// fetch helpers is pure and unit-tested.

type Schemas = components["schemas"];

export type Subscription = Schemas["Subscription"];
export type SubscriptionList = Schemas["SubscriptionList"];
export type SubscriptionCadence = Schemas["SubscriptionCadence"];
export type SubscriptionRenewal = Schemas["SubscriptionRenewal"];
export type FinanceSettings = Schemas["FinanceSettings"];
export type UpdateSettingsBody = Schemas["UpdateFinanceSettingsRequest"];
export type UpdateSubscriptionBody = Schemas["UpdateSubscriptionRequest"];

export const CADENCES: { value: SubscriptionCadence; label: string }[] = [
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
  { value: "quarterly", label: "Every 3 months" },
  { value: "yearly", label: "Yearly" },
];

const PER: Record<SubscriptionCadence, string> = {
  weekly: "week",
  monthly: "month",
  quarterly: "quarter",
  yearly: "year",
};

/** The most renewal reminders' lead days the API takes. */
export const MAX_LEAD_DAYS = 30;
/** How many upcoming renewals Home lists. */
export const HOME_RENEWALS = 3;

// ---- Texts ------------------------------------------------------------------

export function cadenceLabel(cadence: SubscriptionCadence) {
  return CADENCES.find((item) => item.value === cadence)?.label ?? cadence;
}

/** "€13.99 / month". */
export function pricePerCadence(sub: Subscription, locale: string) {
  return `${formatMoney(sub.amount, locale)} / ${PER[sub.cadence]}`;
}

/** Whole days from `today` to `day` (both YYYY-MM-DD); negative when past. */
export function daysUntil(day: string, today: string) {
  return Math.round(
    (Date.parse(`${day}T00:00:00Z`) - Date.parse(`${today}T00:00:00Z`)) /
      86_400_000,
  );
}

/** "today", "tomorrow", "in 3 days". */
export function inDays(days: number) {
  if (days <= 0) return "today";
  return days === 1 ? "tomorrow" : `in ${days} days`;
}

/**
 * Where the next payment stands: "Renews in 3 days", "Next on Sun 1 Nov",
 * "Expected Tue 13 Oct, not seen yet", "Missed: expected Tue 13 Oct", or
 * for a suggestion "Next expected Thu 15 Oct".
 */
export function renewalText(sub: Subscription, today: string, locale: string) {
  if (!sub.nextDate) return "";
  const day = bookingDayLabel(sub.nextDate, today, locale);
  switch (sub.renewal) {
    case "upcoming": {
      const when = inDays(daysUntil(sub.nextDate, today));
      return `Renews ${when}`;
    }
    case "overdue":
      return `Expected ${day}, not seen yet`;
    case "missed":
      return `Missed: expected ${day}`;
    case "scheduled":
      return `Next on ${day}`;
  }
  return `Next expected ${day}`;
}

/**
 * How detection found it: "3 payments, last on Tue 15 Sep", with "seen
 * twice a year apart" for a yearly one it is less sure about.
 */
export function evidenceText(sub: Subscription, today: string, locale: string) {
  const count = sub.transactionIds.length;
  const payments = `${count} ${count === 1 ? "payment" : "payments"}`;
  const last = sub.lastDate
    ? `, last on ${bookingDayLabel(sub.lastDate, today, locale)}`
    : "";
  const unsure =
    sub.confidence === "low" ? " (seen twice, a year apart: less sure)" : "";
  return `${payments}${last}${unsure}`;
}

/** The monthly cost of a cadence other than monthly: "≈ €7.83 a month". */
export function monthlyText(sub: Subscription, locale: string) {
  if (sub.cadence === "monthly") return "";
  return `≈ ${formatMoney(sub.monthlyAmount, locale)} a month`;
}

/** The monthly total's note: how many, and what is left out. */
export function totalNote(list: SubscriptionList) {
  const { confirmed, excluded, currency } = list.totals;
  const count = `${confirmed} confirmed ${confirmed === 1 ? "subscription" : "subscriptions"}`;
  if (!excluded) return count;
  return `${count}; ${excluded} in other currencies ${excluded === 1 ? "isn't" : "aren't"} counted (totals use ${currency})`;
}

// ---- Grouping ---------------------------------------------------------------

export type SubscriptionGroups = {
  suggested: Subscription[];
  /** Confirmed and on track (or a little late), next payment first. */
  confirmed: Subscription[];
  /** Confirmed, but the expected payment is more than a week late. */
  missed: Subscription[];
  ignored: Subscription[];
};

const byNextDate = (a: Subscription, b: Subscription) =>
  (a.nextDate ?? "9999").localeCompare(b.nextDate ?? "9999") ||
  a.name.localeCompare(b.name);

/** The tab's sections. */
export function subscriptionGroups(items: Subscription[]): SubscriptionGroups {
  const confirmed = items.filter((item) => item.state === "confirmed");
  return {
    suggested: items.filter((item) => item.state === "suggested"),
    confirmed: confirmed
      .filter((item) => item.renewal !== "missed")
      .sort(byNextDate),
    missed: confirmed.filter((item) => item.renewal === "missed"),
    ignored: items.filter((item) => item.state === "ignored"),
  };
}

/**
 * The subscription a transaction belongs to (confirmed before suggested),
 * if any: its dialog says so instead of offering "This is a subscription".
 */
export function subscriptionOf(items: Subscription[], transactionId: string) {
  const holding = items.filter(
    (item) =>
      item.state !== "ignored" && item.transactionIds.includes(transactionId),
  );
  return (
    holding.find((item) => item.state === "confirmed") ??
    holding.find((item) => item.state === "suggested")
  );
}

/** Replaces the subscription with the same id, or adds it; totals stay. */
export function withSubscription(
  list: SubscriptionList,
  sub: Subscription,
): SubscriptionList {
  const known = list.items.some((item) => item.id === sub.id);
  return {
    ...list,
    items: known
      ? list.items.map((item) => (item.id === sub.id ? sub : item))
      : [...list.items, sub],
  };
}

// ---- Edit form --------------------------------------------------------------

export type SubscriptionDraft = {
  name: string;
  cadence: SubscriptionCadence;
  /** The typical payment, as typed. */
  amount: string;
  /** A category id, or "" for none. */
  categoryId: string;
};

export type SubscriptionErrors = { name?: string; amount?: string };

export function subscriptionDraft(
  sub: Subscription,
  locale: string,
): SubscriptionDraft {
  return {
    name: sub.name,
    cadence: sub.cadence,
    amount: amountInput(sub.amount.amount, sub.amount.currency, locale),
    categoryId: sub.categoryId ?? "",
  };
}

function amountError(currency: string, example: string) {
  const digits = minorDigits(currency);
  return digits
    ? `Enter an amount like ${example}, with at most ${digits} decimals.`
    : "Enter a whole amount, like 1300.";
}

/**
 * What the draft changes, as a PATCH body (undefined when nothing changed),
 * or the fields' errors.
 */
export function updateSubscriptionBody(
  sub: Subscription,
  draft: SubscriptionDraft,
): { body?: UpdateSubscriptionBody; errors: SubscriptionErrors } {
  const errors: SubscriptionErrors = {};
  const name = draft.name.trim();
  if (!name || name.length > 100)
    errors.name = "Enter a name of 1 to 100 characters.";
  const amount = parseAmount(draft.amount, sub.amount.currency);
  if (amount === undefined || amount <= 0) {
    errors.amount = amountError(sub.amount.currency, "12.99");
  }
  if (Object.keys(errors).length) return { errors };
  const body: UpdateSubscriptionBody = {};
  if (name !== sub.name) body.name = name;
  if (draft.cadence !== sub.cadence) body.cadence = draft.cadence;
  if (amount !== sub.amount.amount) body.amountMinor = amount;
  if ((draft.categoryId || null) !== sub.categoryId) {
    body.categoryId = draft.categoryId || null;
  }
  return { body: Object.keys(body).length ? body : undefined, errors };
}

// ---- Alert settings ---------------------------------------------------------

export type SettingsDraft = {
  largeDebitEnabled: boolean;
  largeDebit: string;
  lowBalanceEnabled: boolean;
  lowBalance: string;
  renewalsEnabled: boolean;
  leadDays: string;
};

export type SettingsErrors = {
  largeDebit?: string;
  lowBalance?: string;
  leadDays?: string;
};

export function settingsDraft(
  settings: FinanceSettings,
  locale: string,
): SettingsDraft {
  const { currency } = settings;
  return {
    largeDebitEnabled: settings.largeDebit.enabled,
    largeDebit: amountInput(
      settings.largeDebit.threshold.amount,
      currency,
      locale,
    ),
    lowBalanceEnabled: settings.lowBalance.enabled,
    lowBalance: amountInput(
      settings.lowBalance.threshold.amount,
      currency,
      locale,
    ),
    renewalsEnabled: settings.renewals.enabled,
    leadDays: String(settings.renewals.leadDays),
  };
}

/** The PUT body for the draft, or the fields' errors. */
export function settingsBody(
  draft: SettingsDraft,
  currency: string,
): { body?: UpdateSettingsBody; errors: SettingsErrors } {
  const errors: SettingsErrors = {};
  const largeDebit = parseAmount(draft.largeDebit, currency);
  if (largeDebit === undefined || largeDebit <= 0) {
    errors.largeDebit = amountError(currency, "250.00");
  }
  const lowBalance = parseAmount(draft.lowBalance, currency);
  if (lowBalance === undefined || lowBalance < 0) {
    errors.lowBalance = amountError(currency, "100.00");
  }
  const leadDays = Number(draft.leadDays.trim());
  if (
    !/^\d+$/.test(draft.leadDays.trim()) ||
    leadDays < 0 ||
    leadDays > MAX_LEAD_DAYS
  ) {
    errors.leadDays = `Enter a whole number of days from 0 to ${MAX_LEAD_DAYS}.`;
  }
  if (Object.keys(errors).length) return { errors };
  return {
    body: {
      largeDebit: {
        enabled: draft.largeDebitEnabled,
        thresholdMinor: largeDebit ?? 0,
      },
      lowBalance: {
        enabled: draft.lowBalanceEnabled,
        thresholdMinor: lowBalance ?? 0,
      },
      renewals: { enabled: draft.renewalsEnabled, leadDays },
    },
    errors,
  };
}

/** "3 days before", "1 day before", "on the day". */
export function leadDaysText(days: number) {
  if (days === 0) return "on the day";
  return `${days} ${days === 1 ? "day" : "days"} before`;
}

// ---- Home -------------------------------------------------------------------

export type HomeRenewal = {
  id: string;
  name: string;
  amount: Money;
  nextDate: string;
  /** Days from today; 0 is today. */
  days: number;
};

/**
 * Confirmed subscriptions renewing within the reminders' lead days (the
 * API marks them `upcoming`), soonest first, at most HOME_RENEWALS.
 */
export function homeRenewals(
  list: SubscriptionList,
  today: string,
): { items: HomeRenewal[]; more: boolean } {
  const upcoming = list.items
    .filter(
      (item) =>
        item.state === "confirmed" &&
        item.renewal === "upcoming" &&
        item.nextDate,
    )
    .sort(byNextDate)
    .map((item) => ({
      id: item.id,
      name: item.name,
      amount: item.amount,
      nextDate: item.nextDate as string,
      days: daysUntil(item.nextDate as string, today),
    }));
  return {
    items: upcoming.slice(0, HOME_RENEWALS),
    more: upcoming.length > HOME_RENEWALS,
  };
}

/** Accounts whose balance is below the low-balance alert's threshold. */
export function lowBalanceAccounts(accounts: Account[]) {
  return accounts.filter((account) => account.lowBalance && !account.archived);
}

/** The Subscriptions tab, for links. */
export const SUBSCRIPTIONS_HREF = "/finance?tab=subscriptions";
