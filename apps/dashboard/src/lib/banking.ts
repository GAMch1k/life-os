import type { components } from "@/lib/api/generated/banking";
import { dateTimeFormat } from "@/lib/time-zone";

// The bank connection as the Dashboard shows it. The banking integration
// service owns the consent, the provider session, and the sync schedule;
// the Dashboard opens the bank's consent page, carries the answer back from
// /finance/bank/callback, and shows how the connection and its syncs are
// going. Session ids and full account numbers never reach the browser.

export type BankConnection = components["schemas"]["BankConnection"];
export type BankConnectionList = components["schemas"]["BankConnectionList"];
export type BankAccount = components["schemas"]["BankAccount"];

/** The callback route the bank returns to (LIFE_OS_BANKING_REDIRECT_URI). */
export const BANK_CALLBACK_PATH = "/finance/bank/callback";

/** The query parameter the callback adds to /finance after connecting. */
export const BANK_PARAM = "bank";
export const BANK_CONNECTED = "connected";

/** The bank Life OS connects. */
export const DEFAULT_BANK = "Deutsche Bank";

export const BANKING_SETTINGS = [
  "LIFE_OS_ENABLE_BANKING_APP_ID",
  "LIFE_OS_ENABLE_BANKING_KEY_FILE",
  "LIFE_OS_BANKING_TOKEN_KEY",
] as const;

/** Days before the consent ends from which it counts as expiring soon. */
export const EXPIRING_SOON_DAYS = 7;

const DAY_MS = 24 * 60 * 60 * 1000;

/** The bank's live (not disconnected) connection, if any. */
export function liveConnection(
  list: BankConnectionList,
  institution: string = DEFAULT_BANK,
): BankConnection | undefined {
  const live = list.items.filter((item) => item.status !== "disconnected");
  return live.find((item) => item.institution === institution) ?? live[0];
}

/** What the bank card shows. */
export type BankState =
  | "not_configured"
  | "not_connected"
  | "pending"
  | "connected"
  | "expiring"
  | "expired"
  | "error";

export function bankState(
  configured: boolean,
  connection: BankConnection | undefined,
  now: Date,
): BankState {
  if (!configured) return "not_configured";
  if (!connection || connection.status === "disconnected") {
    return "not_connected";
  }
  if (connection.status !== "connected") return connection.status;
  return expiringSoon(connection, now) ? "expiring" : "connected";
}

/** Whether a live consent ends within EXPIRING_SOON_DAYS. */
export function expiringSoon(connection: BankConnection, now: Date) {
  if (!connection.consentValidUntil) return false;
  const left = new Date(connection.consentValidUntil).getTime() - now.getTime();
  return left <= EXPIRING_SOON_DAYS * DAY_MS;
}

/** Whether the card offers Reconnect: the consent ended or needs action. */
export function needsReconnect(state: BankState) {
  return state === "expired" || state === "error" || state === "expiring";
}

/** An account as listed, such as "Das Junge Konto (DE89…3000)". */
export function accountLabel(account: BankAccount) {
  return account.ibanMasked
    ? `${account.name} (${account.ibanMasked})`
    : account.name;
}

const DAY: Intl.DateTimeFormatOptions = {
  day: "numeric",
  month: "short",
  year: "numeric",
};

const MOMENT: Intl.DateTimeFormatOptions = {
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  timeZoneName: "short",
};

const CLOCK: Intl.DateTimeFormatOptions = {
  hour: "2-digit",
  minute: "2-digit",
  timeZoneName: "short",
};

/** A date such as "7 Apr 2027". */
export function bankDay(value: string, timeZone: string) {
  return dateTimeFormat(timeZone, DAY).format(new Date(value));
}

/** A moment such as "10 Oct, 12:05 CEST". */
export function bankTime(value: string, timeZone: string) {
  return dateTimeFormat(timeZone, MOMENT).format(new Date(value));
}

/** How long the consent lasts, such as "Access valid until 7 Apr 2027". */
export function consentText(
  connection: BankConnection,
  timeZone: string,
  now: Date,
) {
  const until = connection.consentValidUntil;
  if (!until) return undefined;
  const left = new Date(until).getTime() - now.getTime();
  if (left <= 0) return `Access ended on ${bankDay(until, timeZone)}`;
  if (left <= DAY_MS) return `Access ends ${bankTime(until, timeZone)}`;
  if (left <= EXPIRING_SOON_DAYS * DAY_MS) {
    const days = Math.ceil(left / DAY_MS);
    return `Access ends in ${days} days (${bankDay(until, timeZone)})`;
  }
  return `Access valid until ${bankDay(until, timeZone)}`;
}

/**
 * How syncing goes, for the line under the accounts, such as "Synced 10
 * Oct, 12:05 CEST · 3 new transactions"; failing marks a failed last sync.
 */
export function syncText(
  connection: BankConnection,
  timeZone: string,
): { text: string; failing: boolean } {
  const last = connection.lastSuccessAt
    ? ` Last successful sync ${bankTime(connection.lastSuccessAt, timeZone)}.`
    : "";
  if (connection.waitingForFinance) {
    return {
      text: `Read from the bank; waiting for Finance, sent again automatically.${last}`,
      failing: connection.consecutiveFailures > 1,
    };
  }
  if (connection.consecutiveFailures > 0 && connection.lastError) {
    return { text: `${connection.lastError}${last}`, failing: true };
  }
  if (!connection.lastSuccessAt) {
    return { text: "Importing your transactions…", failing: false };
  }
  const created = connection.lastCounts?.transactions.created ?? 0;
  const detail =
    created > 0
      ? ` · ${created} new transaction${created === 1 ? "" : "s"}`
      : " · up to date";
  return {
    text: `Synced ${bankTime(connection.lastSuccessAt, timeZone)}${detail}`,
    failing: false,
  };
}

/** When the next scheduled sync runs, such as "Next sync 18:00 CEST". */
export function nextSyncText(connection: BankConnection, timeZone: string) {
  if (!connection.nextSyncAt) return undefined;
  return `Next sync ${dateTimeFormat(timeZone, CLOCK).format(new Date(connection.nextSyncAt))}`;
}

/** Whether "Sync now" may run, and why not. */
export function syncNowState(
  connection: BankConnection,
  timeZone: string,
  now: Date,
): { allowed: boolean; reason?: string } {
  if (connection.status !== "connected" && connection.status !== "pending") {
    return { allowed: false, reason: "Reconnect the bank to sync again." };
  }
  const available = connection.manualSyncAvailableAt;
  if (available && new Date(available).getTime() > now.getTime()) {
    return {
      allowed: false,
      reason: `Synced moments ago; again from ${dateTimeFormat(timeZone, CLOCK).format(new Date(available))}.`,
    };
  }
  return { allowed: true };
}

type QueryValue = string | string[] | undefined;

function first(value: QueryValue) {
  return Array.isArray(value) ? value[0] : value;
}

/** What the callback route does with the query the bank returned. */
export type CallbackStep =
  | { kind: "exchange"; code: string; state: string }
  | { kind: "error"; message: string };

const BANK_ERRORS: Record<string, string> = {
  access_denied:
    "You did not give consent at the bank, so nothing was connected.",
  rejected: "The bank did not accept the consent, so nothing was connected.",
};

export function callbackStep(query: {
  code?: QueryValue;
  state?: QueryValue;
  error?: QueryValue;
}): CallbackStep {
  const error = first(query.error);
  if (error) {
    return {
      kind: "error",
      message:
        BANK_ERRORS[error] ??
        `The bank did not complete the consent (${error.slice(0, 64)}).`,
    };
  }
  const code = first(query.code);
  const state = first(query.state);
  if (!code || !state) {
    return {
      kind: "error",
      message: `This page is where the bank returns after you give consent. Start from Finance with Connect ${DEFAULT_BANK}.`,
    };
  }
  return { kind: "exchange", code, state };
}

/** Why banking refused a request, in plain words. */
export function bankFailure(
  status: number,
  body: unknown,
): { message: string; retry: boolean } {
  const error =
    typeof body === "object" && body !== null && "error" in body
      ? (body as { error?: { code?: unknown; message?: unknown } }).error
      : undefined;
  const code = typeof error?.code === "string" ? error.code : undefined;
  const detail =
    typeof error?.message === "string" && error.message
      ? sentence(error.message)
      : undefined;
  switch (code) {
    case "invalid_state":
      return {
        message: `This consent expired or was already used. Start again with Connect ${DEFAULT_BANK}.`,
        retry: true,
      };
    case "bank_code_rejected":
      return {
        message: `The bank did not accept the consent. Start again with Connect ${DEFAULT_BANK}.`,
        retry: true,
      };
    case "no_accounts":
      return {
        message:
          "The consent covers no account. Connect again and choose at least one account at the bank.",
        retry: true,
      };
    case "banking_not_configured":
      return {
        message: `Bank connections are not set up on this server: it needs ${BANKING_SETTINGS.join(", ")} in its .env.`,
        retry: false,
      };
    case "sync_throttled":
    case "daily_limit":
    case "sync_in_progress":
    case "connection_inactive":
    case "unknown_bank":
    case "bank_unavailable":
    case "bank_expired":
    case "bank_refused":
    case "finance_unavailable":
    case "finance_refused":
      return {
        message: detail ?? "The bank could not be synced.",
        retry: true,
      };
  }
  if (status >= 500) {
    return {
      message: detail ?? "Banking is unavailable right now. Try again.",
      retry: true,
    };
  }
  return { message: detail ?? "The bank could not be connected.", retry: true };
}

function sentence(message: string) {
  const text = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

/** The URL without the "connected" marker, once it has been shown. */
export function withoutBankParam(search: string) {
  const params = new URLSearchParams(search);
  params.delete(BANK_PARAM);
  const rest = params.toString();
  return rest ? `?${rest}` : "";
}
