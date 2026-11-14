import {
  SECURITY_EVENT_KINDS,
  SECURITY_KINDS,
  type SecurityEvent,
  type SecurityEventKind,
} from "@/lib/auth/security-log";
import { addDays, startOfDay } from "@/lib/time-zone";

/**
 * How the security log reads on its pages (Settings → Security log, and
 * the admins' view of every account's): a title and a short detail for
 * each entry, and the admin view's filters.
 */

const METHODS: Record<string, string> = {
  password: "with a password",
  totp: "with an authenticator code",
  recovery_code: "with a recovery code",
  passkey: "with a passkey",
};

const REASONS: Record<string, string> = {
  invalid_username_or_password: "wrong username or password",
  too_many_failed_sign_ins: "too many failed sign-ins",
  invalid_code: "wrong code",
  invalid_backup_code: "wrong recovery code",
  invalid_two_factor_cookie: "the sign-in expired",
  too_many_attempts: "too many attempts",
  banned_user: "the account is disabled",
  second_factor_required: "needs two-factor sign-in",
  session_not_fresh: "needs a sign-in from the last 12 hours",
  admin_only: "not an admin",
  sign_in_required: "needs a sign-in",
  owner_only: "only the server's owner may",
  own_role: "not one's own role",
  owner_protected: "the server's owner is protected",
  role_invalid: "not a role",
  host_only: "only on the server",
  accounts: "more than one account exists",
  setup_code_invalid: "wrong, used, or expired code",
};

const ACTIONS: Record<string, string> = {
  backup: "Back up now",
  "set-role": "changing a role",
  "ban-user": "disabling an account",
  "unban-user": "enabling an account",
  "remove-user": "deleting an account",
  "revoke-user-session": "signing an account out",
  "revoke-user-sessions": "signing an account out",
};

function humanize(code: string) {
  return code.replace(/[_:.-]+/g, " ").trim();
}

function reason(code: string | undefined) {
  if (!code) return "";
  return REASONS[code] ?? humanize(code);
}

/** The entry's title: what happened. */
export function securityTitle(event: Pick<SecurityEvent, "kind">) {
  return (
    SECURITY_EVENT_KINDS[event.kind as SecurityEventKind] ??
    humanize(event.kind)
  );
}

/** A short line about how, from the entry's detail code. */
export function securityDetail(
  event: Pick<SecurityEvent, "kind" | "detail" | "count">,
): string {
  const detail = event.detail ?? "";
  const [first, second] = detail.split(":");
  switch (event.kind) {
    case "sign_in":
      return METHODS[detail] ?? "";
    case "sign_in_failed":
      return [METHODS[first], reason(second)].filter(Boolean).join(": ");
    case "sessions_revoked":
      return detail === "one"
        ? "one session"
        : detail === "others"
          ? "every other session"
          : "every session";
    case "role_changed":
      return detail === "admin" || detail === "member"
        ? `to ${detail}`
        : reason(detail);
    case "legacy_access_changed":
      return second
        ? `turning it ${first} refused: ${reason(second)}`
        : `turned ${first}`;
    case "legacy_access_used":
      return `${event.count.toLocaleString("en")} ${event.count === 1 ? "request" : "requests"}`;
    case "owner_recovered":
      return detail === "reset_link_2fa"
        ? "two-factor sign-in removed too"
        : "";
    case "signed_out_everyone":
      return detail === "keep_devices" ? "paired devices kept" : "";
    case "keys_rotated":
      return detail ? `now ${detail}` : "";
    case "admin_refused":
      return [ACTIONS[first] ?? humanize(first ?? ""), reason(second)]
        .filter(Boolean)
        .join(": ");
    case "account_created":
    case "password_reset":
      return detail === "setup_code" || detail === "reset_link"
        ? ""
        : reason(detail);
    case "backup_started":
      return detail ? reason(detail) : "";
    default:
      return detail ? reason(detail) : "";
  }
}

/** Who did it, as the entry says. */
export function securityActor(
  event: Pick<SecurityEvent, "actor" | "via">,
): string {
  if (event.via === "host") return "On the server";
  if (event.via === "legacy") return "Without signing in";
  if (event.actor) return event.actor.name ?? "A deleted account";
  return "Someone not signed in";
}

// ---- The admin view's filters ----------------------------------------------

export type SearchParams = Record<string, string | string[] | undefined>;

export type SecurityFilters = {
  user?: string;
  kind?: SecurityEventKind;
  /** Inclusive first day, YYYY-MM-DD in the viewer's timezone. */
  fromDate?: string;
  /** Inclusive last day. */
  toDate?: string;
  cursor?: string;
};

function single(value: string | string[] | undefined) {
  const text = (Array.isArray(value) ? value[0] : value)?.trim();
  return text ? text.slice(0, 200) : undefined;
}

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function parseSecurityFilters(params: SearchParams): SecurityFilters {
  const user = single(params.user);
  const kind = single(params.kind);
  const day = (value: string | undefined) =>
    value && DAY.test(value) && !Number.isNaN(Date.parse(`${value}T00:00:00Z`))
      ? value
      : undefined;
  const filters: SecurityFilters = {
    user: user && UUID.test(user) ? user.toLowerCase() : undefined,
    kind: SECURITY_KINDS.find((value) => value === kind),
    fromDate: day(single(params.fromDate)),
    toDate: day(single(params.toDate)),
    cursor: single(params.cursor),
  };
  return Object.fromEntries(
    Object.entries(filters).filter(([, value]) => value !== undefined),
  ) as SecurityFilters;
}

/** The instants the filters' days cover, in the viewer's timezone. */
export function securityRange(filters: SecurityFilters, timeZone: string) {
  return {
    from: filters.fromDate ? startOfDay(filters.fromDate, timeZone) : undefined,
    to: filters.toDate
      ? startOfDay(addDays(filters.toDate, 1), timeZone)
      : undefined,
  };
}

export function hasSecurityFilters(filters: SecurityFilters) {
  return Boolean(
    filters.user || filters.kind || filters.fromDate || filters.toDate,
  );
}

/** A URL of a log page with these filters. */
export function securityHref(
  base: string,
  filters: SecurityFilters,
  changes: Partial<SecurityFilters> = {},
) {
  const merged: Record<string, string | undefined> = {
    ...filters,
    ...changes,
  };
  const params = new URLSearchParams();
  for (const key of ["user", "kind", "fromDate", "toDate", "cursor"]) {
    const value = merged[key];
    if (value) params.set(key, value);
  }
  const query = params.toString();
  return query ? `${base}?${query}` : base;
}
