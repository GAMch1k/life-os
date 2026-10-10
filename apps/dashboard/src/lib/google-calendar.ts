import type { components } from "@/lib/api/generated/google-calendar";
import { dateTimeFormat } from "@/lib/time-zone";

// The Google Calendar connection as the Dashboard shows it: the
// google-calendar integration service owns the OAuth flow, the tokens, and
// the two-way sync with Calendar; the Dashboard opens Google's consent page,
// carries the answer back from /calendar/google/callback (the redirect URI
// registered at Google, unchanged) to the integration, edits which
// calendars sync and whether Life OS events are exported to the Life OS
// calendar, and shows how both are going. Tokens never reach the browser.

export type GoogleConnection = components["schemas"]["GoogleConnection"];
export type GoogleCalendar = components["schemas"]["GoogleCalendar"];
export type EventExport = components["schemas"]["EventExport"];

/** What the Google dialog says when the integration cannot be reached. */
export const GOOGLE_UNAVAILABLE =
  "Google Calendar is unavailable right now. Try again.";

/** The callback route Google returns to (LIFE_OS_GOOGLE_REDIRECT_URI). */
export const GOOGLE_CALLBACK_PATH = "/calendar/google/callback";

/** The query parameter the callback adds to /calendar after connecting. */
export const GOOGLE_PARAM = "google";
export const GOOGLE_CONNECTED = "connected";

export const GOOGLE_SETTINGS = [
  "LIFE_OS_GOOGLE_CLIENT_ID",
  "LIFE_OS_GOOGLE_CLIENT_SECRET",
  "LIFE_OS_GOOGLE_TOKEN_KEY",
] as const;

/** What the Google dialog shows. */
export type GoogleState =
  "not_configured" | "not_connected" | "connected" | "expired" | "error";

export function googleState(connection: GoogleConnection): GoogleState {
  if (!connection.configured) return "not_configured";
  return connection.status;
}

/** Plain names of the permissions Life OS asks Google for. */
const PERMISSIONS: Record<string, string> = {
  "https://www.googleapis.com/auth/calendar.calendarlist.readonly":
    "see your list of calendars",
  "https://www.googleapis.com/auth/calendar.events":
    "see and edit events in your calendars",
  "https://www.googleapis.com/auth/calendar.app.created":
    "manage the calendars Life OS creates",
};

/** The permissions Google did not grant, in plain words. */
export function missingPermissions(connection: GoogleConnection) {
  return connection.missingScopes.map((scope) => PERMISSIONS[scope] ?? scope);
}

const ROLE_LABEL: Record<GoogleCalendar["accessRole"], string> = {
  owner: "Owner",
  writer: "Can edit",
  reader: "Read only",
  freeBusyReader: "Free/busy only",
};

/**
 * How the calendar is described under its name, such as "Primary · Owner"
 * or "Created by Life OS · Owner" for the Life OS calendar.
 */
export function calendarDetail(calendar: GoogleCalendar) {
  const role = ROLE_LABEL[calendar.accessRole] ?? calendar.accessRole;
  if (calendar.lifeOs) return `Created by Life OS · ${role}`;
  return calendar.primary ? `Primary · ${role}` : role;
}

/** Calendars that will sync, as a short sentence. */
export function syncSummary(calendars: GoogleCalendar[]) {
  const chosen = calendars.filter((calendar) => calendar.syncEnabled).length;
  if (calendars.length === 0) return "Google lists no calendars.";
  if (chosen === 0) return "No calendar is chosen to sync.";
  return `${chosen} of ${calendars.length} calendar${calendars.length === 1 ? "" : "s"} chosen to sync.`;
}

/** What the dialog says about syncing chosen calendars. */
export const SYNC_NOTE =
  "Chosen calendars sync both ways every few minutes: editing their events here changes them in Google within seconds (calendars you can only read stay read-only here, except reminders and priority; if an event changed in Google meanwhile, Google's version is kept). Unticking one removes its events from Life OS (they stay in Google).";

/** What the dialog says about exporting Life OS events. */
export const EXPORT_NOTE =
  "Events you create in Life OS are copied to a “Life OS” calendar in your Google account, with its own color on your phone; changes made there come back to Life OS. Hide or remove that calendar in Google Calendar at any time. Feed events (such as ZAW) are not exported.";

/**
 * How exporting Life OS events goes, for the line under the toggle, such as
 * "Up to date in “Life OS” · last export 10 Oct, 12:05 CEST"; failing marks
 * exports that keep failing (retried automatically).
 */
export function exportDetail(
  connection: GoogleConnection,
  timeZone: string,
): { text: string; failing: boolean } {
  if (!connection.exportEnabled) {
    return {
      text: "Off: events created in Life OS stay in Life OS.",
      failing: false,
    };
  }
  const { pending, failed, lastError, lastExportedAt } = connection.export ?? {
    pending: 0,
    failed: 0,
  };
  const calendar = connection.lifeOsCalendar;
  if (failed > 0) {
    return {
      text: `${failed} event${failed === 1 ? "" : "s"} could not be exported${lastError ? `: ${lastError}` : ""}. Retrying automatically.`,
      failing: true,
    };
  }
  if (!calendar) {
    return lastError
      ? { text: lastError, failing: true }
      : {
          text: "Life OS creates its “Life OS” calendar in your Google account within a few minutes.",
          failing: false,
        };
  }
  if (pending > 0) {
    return {
      text: `${pending} event${pending === 1 ? "" : "s"} waiting to be exported to “${calendar.summary}”…`,
      failing: false,
    };
  }
  const last = lastExportedAt
    ? ` · last export ${syncTime(lastExportedAt, timeZone)}`
    : "";
  return { text: `Up to date in “${calendar.summary}”${last}`, failing: false };
}

/**
 * How writing your edits of Google events to Google goes, for the line
 * under the connection, such as "1 edit made here is waiting to reach
 * Google…"; undefined when nothing waits. failing marks edits whose last
 * attempt failed (retried automatically), or Calendar's changes that could
 * not be read.
 */
export function editsDetail(
  connection: GoogleConnection,
): { text: string; failing: boolean } | undefined {
  const changes = connection.changes;
  if (!changes) return undefined;
  if (changesUnreadable(connection)) {
    return {
      text: `Life OS events and edits made here do not reach Google right now: ${changes.lastError}`,
      failing: true,
    };
  }
  if (changes.failedEdits > 0) {
    return {
      text: `${changes.failedEdits} edit${changes.failedEdits === 1 ? "" : "s"} made here could not be written to Google yet${changes.lastEditError ? `: ${changes.lastEditError}` : ""}. Retrying automatically.`,
      failing: true,
    };
  }
  if (changes.pendingEdits > 0) {
    return {
      text: `${changes.pendingEdits} edit${changes.pendingEdits === 1 ? "" : "s"} made here ${changes.pendingEdits === 1 ? "is" : "are"} on the way to Google…`,
      failing: false,
    };
  }
  return undefined;
}

/**
 * Whether the integration's last read of Calendar's changes failed after
 * its last good one (such as Calendar unreachable).
 */
export function changesUnreadable(connection: GoogleConnection) {
  const changes = connection.changes;
  if (!changes?.lastError || !changes.lastErrorAt) return false;
  return (
    !changes.lastReadAt ||
    new Date(changes.lastErrorAt).getTime() >
      new Date(changes.lastReadAt).getTime()
  );
}

const SYNC_TIME: Intl.DateTimeFormatOptions = {
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  timeZoneName: "short",
};

/** A sync time such as "10 Oct, 12:05 CEST". */
export function syncTime(value: string, timeZone: string) {
  return dateTimeFormat(timeZone, SYNC_TIME).format(new Date(value));
}

/**
 * How a chosen calendar's sync is going, for the line under its name, such
 * as "Synced 10 Oct, 12:05 CEST · 42 events"; undefined while it does not
 * sync. `failing` marks a failed last sync (retried automatically, its
 * events from the last good sync kept).
 */
export function syncDetail(
  calendar: GoogleCalendar,
  timeZone: string,
): { text: string; failing: boolean } | undefined {
  const sync = calendar.sync;
  if (!calendar.syncEnabled || sync.state === "off") return undefined;
  if (sync.state === "failing") {
    const last = sync.lastSyncedAt
      ? ` Last synced ${syncTime(sync.lastSyncedAt, timeZone)}.`
      : "";
    return {
      text: `Sync failed: ${sync.lastError ?? "unknown error"}. Retrying automatically.${last}`,
      failing: true,
    };
  }
  if (sync.state === "pending" || !sync.lastSyncedAt) {
    return { text: "Waiting for its first sync…", failing: false };
  }
  const events = sync.lastCounts?.imported;
  const count =
    events === undefined ? "" : ` · ${events} event${events === 1 ? "" : "s"}`;
  return {
    text: `Synced ${syncTime(sync.lastSyncedAt, timeZone)}${count}`,
    failing: false,
  };
}

/** Replaces one calendar in a list (after its sync choice was saved). */
export function replaceCalendar(
  calendars: GoogleCalendar[],
  updated: GoogleCalendar,
) {
  return calendars.map((calendar) =>
    calendar.id === updated.id ? updated : calendar,
  );
}

type QueryValue = string | string[] | undefined;

function first(value: QueryValue) {
  return Array.isArray(value) ? value[0] : value;
}

/** What the callback route does with the query Google returned. */
export type CallbackStep =
  | { kind: "exchange"; code: string; state: string }
  | { kind: "error"; message: string };

/** Google's error answers (RFC 6749 4.1.2.1), in plain words. */
const GOOGLE_ERRORS: Record<string, string> = {
  access_denied:
    "You did not allow Life OS to use your Google Calendar, so nothing was connected.",
  admin_policy_enforced:
    "Your Google account's administrator does not allow this app.",
  invalid_scope: "Google did not accept the permissions Life OS asks for.",
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
        GOOGLE_ERRORS[error] ??
        `Google did not complete the sign-in (${error.slice(0, 64)}).`,
    };
  }
  const code = first(query.code);
  const state = first(query.state);
  if (!code || !state) {
    return {
      kind: "error",
      message:
        "This page is where Google returns after you connect your account. Start from Calendar with Connect Google.",
    };
  }
  return { kind: "exchange", code, state };
}

/**
 * Why the Google Calendar integration refused to complete the connection,
 * in plain words.
 */
export function connectFailure(
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
        message:
          "This sign-in expired or was already used. Start again with Connect Google.",
        retry: true,
      };
    case "google_code_rejected":
      return {
        message:
          "Google did not accept the sign-in. Start again with Connect Google.",
        retry: true,
      };
    case "google_scope_missing":
      return {
        message:
          "Google did not grant permission to see your list of calendars. Connect again and allow every permission Life OS asks for.",
        retry: true,
      };
    case "google_not_configured":
      return {
        message: `Google Calendar is not set up on this server: it needs ${GOOGLE_SETTINGS.join(", ")} in its .env.`,
        retry: false,
      };
    case "google_unavailable":
      return {
        message:
          "Google could not be reached. Start again with Connect Google in a moment.",
        retry: true,
      };
  }
  if (status === 503 || status >= 500) {
    return {
      message: detail ?? GOOGLE_UNAVAILABLE,
      retry: true,
    };
  }
  return { message: detail ?? "Google could not be connected.", retry: true };
}

function sentence(message: string) {
  const text = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

/** The URL without the "connected" marker, once it has been shown. */
export function withoutGoogleParam(search: string) {
  const params = new URLSearchParams(search);
  params.delete(GOOGLE_PARAM);
  const rest = params.toString();
  return rest ? `?${rest}` : "";
}
