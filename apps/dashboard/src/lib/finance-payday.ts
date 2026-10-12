import { financeClient } from "@/lib/api/finance-client";
import type { components } from "@/lib/api/generated/finance";
import {
  amountInput,
  formatMoney,
  minorDigits,
  parseAmount,
} from "@/lib/finance-money";
import { load } from "@/lib/finance-workspace";
import { DATE_LOCALE } from "@/lib/time-zone";

// Payday on the Budget tab (P13-10): the AI plans the budget when the
// salary arrives. The owner names who pays it, as their bank statement
// shows it, with an optional minimum so that a small refund from the same
// company does not count; Finance suggests the payer from the data. These
// are the dialog's rules (the draft, the PUT body, the suggestion) and the
// fetch helper. Everything but the fetch helper is pure and unit-tested.

type Schemas = components["schemas"];

export type PaydaySettings = Schemas["PaydaySettings"];
export type PaydaySuggestion = Schemas["PaydaySuggestion"];
export type UpdatePaydayBody = Schemas["UpdatePaydaySettingsRequest"];

/** How long the payer text may be, as Finance allows. */
export const MAX_PAYER = 200;

/** The payday setting with Finance's suggestion. */
export function fetchPayday() {
  return load(
    () => financeClient.GET("/v1/settings/payday"),
    "The payday setting could not be loaded.",
  );
}

/** The dialog's fields as typed. */
export type PaydayDraft = { enabled: boolean; payer: string; minimum: string };

export type PaydayErrors = { payer?: string; minimum?: string };

export function paydayDraft(
  settings: PaydaySettings,
  locale: string,
): PaydayDraft {
  const minimum = settings.minAmount;
  return {
    enabled: settings.enabled,
    payer: settings.payerMatch ?? "",
    minimum: minimum
      ? amountInput(minimum.amount, minimum.currency, locale)
      : "",
  };
}

/**
 * The PUT body for the draft, or the fields' errors. Switched on, it needs
 * a payer; an empty minimum means any amount.
 */
export function paydayBody(
  draft: PaydayDraft,
  currency: string,
): { body?: UpdatePaydayBody; errors: PaydayErrors } {
  const errors: PaydayErrors = {};
  const payer = draft.payer.trim();
  if (draft.enabled && !payer) {
    errors.payer =
      "Enter who pays your salary, as your bank statement shows it.";
  } else if ([...payer].length > MAX_PAYER) {
    errors.payer = `Keep it to ${MAX_PAYER} characters.`;
  }
  let minimum: number | null = null;
  if (draft.minimum.trim()) {
    const amount = parseAmount(draft.minimum, currency);
    if (amount === undefined || amount < 1) {
      errors.minimum = minorDigits(currency)
        ? `Enter an amount like 1000.00, with at most ${minorDigits(currency)} decimals, or leave it empty.`
        : "Enter a whole amount, like 1000, or leave it empty.";
    } else {
      minimum = amount;
    }
  }
  if (Object.keys(errors).length) return { errors };
  return {
    body: {
      enabled: draft.enabled,
      payerMatch: payer || null,
      minAmountMinor: minimum,
    },
    errors,
  };
}

function comparable(text: string) {
  return text.trim().replace(/\s+/g, " ").toLowerCase();
}

/**
 * The suggestion to offer: Finance's, unless the payer field already says
 * it (ignoring case and spacing, as Finance matches).
 */
export function offeredSuggestion(
  suggestion: PaydaySuggestion | null,
  draft: PaydayDraft,
) {
  if (!suggestion) return undefined;
  return comparable(draft.payer) === comparable(suggestion.payerMatch)
    ? undefined
    : suggestion;
}

/** The suggestion chip: "Use ACME Robotics GmbH". */
export function suggestionLabel(suggestion: PaydaySuggestion) {
  return `Use ${suggestion.payerMatch}`;
}

/**
 * What Finance saw: "About €3,100.00 a month, 3 months in a row, last on
 * 29 Sept".
 */
export function suggestionNote(suggestion: PaydaySuggestion, locale: string) {
  const last = new Intl.DateTimeFormat(DATE_LOCALE, {
    day: "numeric",
    month: "short",
    timeZone: "UTC",
  }).format(new Date(`${suggestion.lastDate}T00:00:00Z`));
  return `About ${formatMoney(suggestion.typicalAmount, locale)} a month, ${suggestion.payments} months in a row, last on ${last}`;
}
