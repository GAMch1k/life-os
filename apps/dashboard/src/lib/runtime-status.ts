import { normalizeServiceUrl, serviceBaseUrls } from "@/config/services";
import { bankingClient } from "@/lib/api/banking-client";
import { calendarClient } from "@/lib/api/calendar-client";
import { calendarFeedsClient } from "@/lib/api/calendar-feeds-client";
import type { components as CalendarComponents } from "@/lib/api/generated/calendar";
import type { components as CalendarFeedsComponents } from "@/lib/api/generated/calendar-feeds";
import { inboxClient } from "@/lib/api/inbox-client";
import type { components as InboxComponents } from "@/lib/api/generated/inbox";
import type { components as NotificationsComponents } from "@/lib/api/generated/notifications";
import { notificationsClient } from "@/lib/api/notifications-client";
import type { components as VoiceComponents } from "@/lib/api/generated/voice";
import { voiceClient } from "@/lib/api/voice-client";
import {
  type GoogleCalendar,
  type GoogleConnection,
  missingPermissions,
} from "@/lib/google-calendar";
import {
  accountLabel,
  type BankConnection,
  type BankConnectionList,
  expiringSoon,
} from "@/lib/banking";
import { DEFAULT_TIME_ZONE, dateTimeFormat } from "@/lib/time-zone";
import { configuredTimeZone, userTimeZone } from "@/lib/user-time-zone";

// The Status page turns supported runtime state into actionable checks:
// - the processor's private /status endpoint (what the running process saw),
// - the Inbox and Voice backlog APIs (durable queue state),
// - the calendar-feeds integration (each subscribed feed's refresh state,
//   with its name and enabled from Calendar's sources), Calendar's Google
//   connection, and the sync state of the Google calendars,
// - the Notifications channels API (each delivery channel's recent outcomes),
// - the banking integration's connections (consent and sync state),
// - each owning service's /ready endpoint.
// Nothing here is inferred from UI behaviour.

export type InboxBacklog = InboxComponents["schemas"]["InboxBacklog"];
export type AudioBacklog = VoiceComponents["schemas"]["AudioBacklog"];
export type CalendarSource = CalendarComponents["schemas"]["CalendarSource"];
export type CalendarFeed = CalendarFeedsComponents["schemas"]["Feed"];
/** What Status shows of a feed: its refresh state, name, and switch. */
export type FeedHealth = Pick<
  CalendarFeed,
  "id" | "name" | "enabled" | "refresh"
>;
export type NotificationChannel = NotificationsComponents["schemas"]["Channel"];

/** Mirrors `Status` in services/mcp/internal/processor/status.go. */
export type ProcessorStatus = {
  model: string;
  /**
   * The planner's fallback timezone (its LIFE_OS_TIMEZONE), for captures
   * whose source metadata names no browser timezone.
   */
  timeZone?: string;
  startedAt: string;
  poll: {
    lastAt: string | null;
    lastSucceededAt: string | null;
    lastError?: string;
  };
  codex: {
    state:
      "ready" | "usage_limited" | "unauthenticated" | "failing" | "unknown";
    authenticated: boolean | null;
    authCheckedAt: string | null;
    lastSucceededAt: string | null;
    lastFailedAt: string | null;
    lastError?: string;
    consecutiveFailures: number;
  };
  /** The Codex runtime (see docs/services/mcp.md). */
  runtime?: {
    timeoutSeconds: number;
    current: {
      itemId: string;
      state: string;
      sessionId?: string;
      since: string;
    } | null;
    completed: number;
    failed: number;
    timedOut: number;
    cancelled: number;
    recovered: number;
    lastReconcileError?: string;
  };
  /** Account usage as reported by the Codex CLI (see docs/services/mcp.md). */
  usage?: {
    current: CodexUsage | null;
    error?: string;
  };
};

export type CodexUsage = {
  checkedAt: string;
  authMode?: string;
  planType?: string;
  resetCreditsAvailable?: number;
  limits: {
    id: string;
    name?: string;
    model?: string;
    reached?: string;
    hasCredits?: boolean;
    windows: {
      usedPercent: number;
      windowMinutes?: number;
      resetsAt?: string;
    }[];
  }[];
};

function windowLabel(minutes: number | undefined) {
  if (minutes === 300) return "5-hour";
  if (minutes === 10080) return "weekly";
  if (minutes === undefined) return "current";
  return minutes % 1440 === 0
    ? `${minutes / 1440}-day`
    : minutes % 60 === 0
      ? `${minutes / 60}-hour`
      : `${minutes}-minute`;
}

/** The run in progress and what happened to runs since the processor started. */
export function runtimeFacts(
  runtime: ProcessorStatus["runtime"],
  timeZone: string = DEFAULT_TIME_ZONE,
) {
  if (!runtime) return [];
  const facts = [
    {
      label: "Codex run now",
      value: runtime.current
        ? `${runtime.current.state} since ${formatTime(runtime.current.since, timeZone)}`
        : "none",
    },
    {
      label: "Runs since start",
      value: `${runtime.completed} completed, ${runtime.failed} failed, ${runtime.timedOut} timed out (limit ${runtime.timeoutSeconds} s), ${runtime.cancelled} cancelled`,
    },
    {
      label: "Recovered after a crash",
      value: String(runtime.recovered),
    },
  ];
  if (runtime.lastReconcileError) {
    facts.push({
      label: "Crash recovery",
      value: `failing: ${runtime.lastReconcileError}`,
    });
  }
  return facts;
}

/**
 * Usage facts exactly as the Codex CLI reported them: one line per metered
 * bucket and window. Nothing is estimated when the read is unavailable.
 */
export function usageFacts(
  usage: ProcessorStatus["usage"],
  timeZone: string = DEFAULT_TIME_ZONE,
) {
  const current = usage?.current;
  if (!current) {
    return [
      {
        label: "Codex usage",
        value: usage?.error ? "unavailable" : "not read yet",
      },
    ];
  }
  const facts = [
    {
      label: "Codex plan",
      value: [
        current.planType,
        current.authMode === "chatgpt" ? "ChatGPT sign-in" : current.authMode,
      ]
        .filter(Boolean)
        .join(" · "),
    },
  ];
  for (const limit of current.limits) {
    const name = limit.id === "codex" ? "Codex" : (limit.name ?? limit.id);
    for (const window of limit.windows) {
      facts.push({
        label: `${name} ${windowLabel(window.windowMinutes)} usage`,
        value: `${Math.round(window.usedPercent)}% used${window.resetsAt ? `, resets ${formatTime(window.resetsAt, timeZone)}` : ""}${limit.reached ? " (limit reached)" : ""}`,
      });
    }
  }
  if (current.resetCreditsAvailable) {
    facts.push({
      label: "Free usage resets available",
      value: String(current.resetCreditsAvailable),
    });
  }
  facts.push({
    label: "Usage read",
    value: `${formatTime(current.checkedAt, timeZone)}${usage?.error ? " (latest read failed)" : ""}`,
  });
  return facts;
}

/** The reset time of the exhausted limit, when Codex reports one. */
export function limitResetsAt(usage: ProcessorStatus["usage"]) {
  const exhausted = usage?.current?.limits
    .flatMap((limit) => limit.windows.map((window) => ({ limit, window })))
    .filter(({ limit, window }) => limit.reached || window.usedPercent >= 100)
    .map(({ window }) => window.resetsAt)
    .filter((value): value is string => Boolean(value))
    .sort();
  return exhausted?.at(-1);
}

/** neutral: not set up, which is fine and needs nothing. */
export type CheckState = "ok" | "neutral" | "warning" | "problem";

export type StatusFact = { label: string; value: string };

/** One part of a check with its own state, such as one calendar feed. */
export type StatusPart = {
  id: string;
  title: string;
  state: CheckState;
  summary: string;
  /** Raw error text from the owning runtime, shown verbatim. */
  detail?: string;
  facts: StatusFact[];
};

export type StatusCheck = {
  id:
    | "processing"
    | "inbox"
    | "voice"
    | "calendar"
    | "notifications"
    | "banking"
    | "services";
  title: string;
  state: CheckState;
  summary: string;
  /** Raw error text from the owning runtime, shown verbatim. */
  detail?: string;
  /** What the user can do about it. */
  action?: string;
  facts: StatusFact[];
  parts?: StatusPart[];
};

export type RuntimeSignals = {
  processor: ProcessorStatus | null;
  inbox: InboxBacklog | null;
  voice: AudioBacklog | null;
  /**
   * The feeds of the calendar-feeds integration; null when it could not be
   * read, absent when not checked (the check is left out).
   */
  calendarFeeds?: CalendarFeed[] | null;
  /**
   * Calendar's sources, for each feed's current name and enabled (the
   * owner's, in Calendar); null or absent when not read (the feeds' own
   * copies are shown).
   */
  calendarSources?: CalendarSource[] | null;
  /** The Google Calendar connection; null or absent when not read. */
  googleConnection?: GoogleConnection | null;
  /**
   * Notification delivery channels; null when Notifications could not be
   * read, absent when not checked (the check is left out).
   */
  notificationChannels?: NotificationChannel[] | null;
  /** The Google calendars with their sync state; null or absent when not read. */
  googleCalendars?: GoogleCalendar[] | null;
  /**
   * The bank connections; null when banking could not be read, absent when
   * not checked (the check is left out).
   */
  bankConnections?: BankConnectionList | null;
  services: { name: string; ready: boolean }[];
};

export type RuntimeStatus = {
  checkedAt: string;
  /** The timezone every time on the page is shown in. */
  timeZone: string;
  overall: CheckState;
  checks: StatusCheck[];
};

/** Work waiting longer than this is a prolonged backlog. */
export const PROLONGED_BACKLOG_MS = 15 * 60 * 1000;

export function formatAge(ms: number) {
  const minutes = Math.max(0, Math.floor(ms / 60_000));
  if (minutes < 1) return "less than a minute";
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours} h ${minutes % 60} min`;
  return `${Math.floor(hours / 24)} days`;
}

const TIME_OPTIONS: Intl.DateTimeFormatOptions = {
  day: "numeric",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  timeZoneName: "short",
};

/** For example "9 Oct 2026, 20:22 CEST" in the owner's timezone. */
export function formatTime(
  value: string | null | undefined,
  timeZone: string = DEFAULT_TIME_ZONE,
) {
  return value
    ? dateTimeFormat(timeZone, TIME_OPTIONS).format(new Date(value))
    : "never";
}

const SEVERITY: Record<CheckState, number> = {
  ok: 0,
  neutral: 0,
  warning: 1,
  problem: 2,
};

function worst(states: CheckState[]): CheckState {
  return states.reduce<CheckState>(
    (current, state) => (SEVERITY[state] > SEVERITY[current] ? state : current),
    "ok",
  );
}

/**
 * Captures are planned in their browser's timezone, else the processor's
 * configured one. That the page is shown in another (the viewer's browser)
 * is expected; a processor configured differently from the Dashboard's
 * LIFE_OS_TIMEZONE is worth pointing out.
 */
function planningTimeZone(processorZone: string, configured: string) {
  const fallback = `capture's browser timezone, else ${processorZone}`;
  return processorZone === configured
    ? fallback
    : `${fallback} (the Dashboard's LIFE_OS_TIMEZONE is ${configured})`;
}

function processingCheck(
  processor: ProcessorStatus | null,
  timeZone: string,
  configured: string,
): StatusCheck {
  const base = { id: "processing" as const, title: "Automatic processing" };
  if (!processor) {
    return {
      ...base,
      state: "problem",
      summary: "The Inbox processor is not running.",
      action:
        "Captures are kept in Inbox but nothing processes them. Start the processor service (docker compose up -d processor) and check its logs.",
      facts: [],
    };
  }
  const { codex, poll } = processor;
  const facts = [
    { label: "Model", value: processor.model },
    ...(processor.timeZone
      ? [
          {
            label: "Planning timezone",
            value: planningTimeZone(processor.timeZone, configured),
          },
        ]
      : []),
    {
      label: "Codex sign-in",
      value:
        codex.authenticated === null
          ? "not checked yet"
          : codex.authenticated
            ? "signed in"
            : "signed out",
    },
    { label: "Last poll", value: formatTime(poll.lastAt, timeZone) },
    {
      label: "Last Codex success",
      value: formatTime(codex.lastSucceededAt, timeZone),
    },
    ...runtimeFacts(processor.runtime, timeZone),
    ...usageFacts(processor.usage, timeZone),
  ];
  if (poll.lastError) {
    return {
      ...base,
      state: "problem",
      summary: "The processor cannot read Inbox through MCP.",
      detail: poll.lastError,
      action:
        "Captures wait until the processor can reach MCP and Inbox again. Check that the mcp and inbox services are running.",
      facts,
    };
  }
  switch (codex.state) {
    case "unauthenticated":
      return {
        ...base,
        state: "problem",
        summary: "Codex is not signed in.",
        detail: codex.lastError,
        action:
          "Sign in with `codex login` on the host as the Life OS user, then restart the processor so it picks up the new session.",
        facts,
      };
    case "usage_limited":
      return {
        ...base,
        state: "warning",
        summary: "Codex has reached its usage limit.",
        detail: codex.lastError,
        action: `Captures wait in Inbox and are processed automatically once the limit resets${
          limitResetsAt(processor.usage)
            ? ` (Codex reports ${formatTime(limitResetsAt(processor.usage), timeZone)})`
            : ""
        }. To resume sooner, add Codex credits or switch LIFE_OS_CODEX_MODEL.`,
        facts: [
          ...facts,
          {
            label: "Failed runs in a row",
            value: String(codex.consecutiveFailures),
          },
        ],
      };
    case "failing":
      return {
        ...base,
        state: "problem",
        summary: "Codex runs are failing.",
        detail: codex.lastError,
        action:
          "Captures are retried automatically. If this persists, check the processor logs.",
        facts: [
          ...facts,
          {
            label: "Failed runs in a row",
            value: String(codex.consecutiveFailures),
          },
        ],
      };
    case "ready":
      return { ...base, state: "ok", summary: "Processing normally.", facts };
    default:
      return {
        ...base,
        state: "ok",
        summary:
          "Waiting for work; Codex has not run since the processor started.",
        facts,
      };
  }
}

function retryReasonLabel(reason: string) {
  if (reason === "usage_limited") return "after the Codex usage limit resets";
  if (reason === "backoff") return "backoff";
  return reason;
}

function inboxCheck(
  inbox: InboxBacklog | null,
  now: Date,
  timeZone: string,
): StatusCheck {
  const base = { id: "inbox" as const, title: "Inbox backlog" };
  if (!inbox) {
    return {
      ...base,
      state: "problem",
      summary: "Inbox is unavailable.",
      action: "Captures cannot be saved or processed until Inbox is back.",
      facts: [],
    };
  }
  const age = inbox.oldestPendingAt
    ? now.getTime() - Date.parse(inbox.oldestPendingAt)
    : 0;
  const facts = [
    { label: "Waiting for processing", value: String(inbox.pendingCount) },
    { label: "Failed, being retried", value: String(inbox.retryingCount) },
    { label: "In Human Review", value: String(inbox.humanReviewCount) },
    {
      label: "Oldest waiting",
      value: inbox.oldestPendingAt ? `${formatAge(age)} ago` : "none",
    },
    {
      label: "Next retry",
      value: inbox.nextRetryAt
        ? `${formatTime(inbox.nextRetryAt, timeZone)}${inbox.nextRetryReason ? ` (${retryReasonLabel(inbox.nextRetryReason)})` : ""}`
        : "none scheduled",
    },
  ];
  const failed = inbox.failedCount ?? 0;
  if (failed > 0) {
    return {
      ...base,
      state: "problem",
      summary: `${failed} capture${failed === 1 ? " needs" : "s need"} your attention: automatic processing gave up.`,
      detail: inbox.lastFailureSummary ?? undefined,
      action:
        "They stay in Inbox and are no longer retried automatically. Rephrase and capture them again if they still matter.",
      facts: [...facts, { label: "Gave up", value: String(failed) }],
    };
  }
  if (inbox.retryingCount > 0) {
    return {
      ...base,
      state: "warning",
      summary: `${inbox.retryingCount} capture${inbox.retryingCount === 1 ? " has" : "s have"} failed and ${inbox.retryingCount === 1 ? "is" : "are"} being retried.`,
      detail: inbox.lastFailureSummary ?? undefined,
      action:
        "The processor retries them automatically at the scheduled time. The error above is the latest failure.",
      facts: [
        ...facts,
        {
          label: "Latest failure",
          value: formatTime(inbox.lastFailureAt, timeZone),
        },
      ],
    };
  }
  if (age > PROLONGED_BACKLOG_MS) {
    return {
      ...base,
      state: "warning",
      summary: `The oldest unprocessed capture has waited ${formatAge(age)}.`,
      action:
        "Check Automatic processing above. A capture that stays in processing for this long may need attention.",
      facts,
    };
  }
  return {
    ...base,
    state: "ok",
    summary:
      inbox.pendingCount === 0
        ? "Nothing waiting."
        : `${inbox.pendingCount} capture${inbox.pendingCount === 1 ? "" : "s"} being processed.`,
    facts,
  };
}

function voiceCheck(
  voice: AudioBacklog | null,
  now: Date,
  timeZone: string,
): StatusCheck {
  const base = { id: "voice" as const, title: "Voice transcription" };
  if (!voice) {
    return {
      ...base,
      state: "problem",
      summary: "Voice is unavailable.",
      action: "Recordings cannot be uploaded until the voice service is back.",
      facts: [],
    };
  }
  const waiting =
    voice.queuedCount + voice.processingCount + voice.retryableCount;
  const age = voice.oldestWaitingAt
    ? now.getTime() - Date.parse(voice.oldestWaitingAt)
    : 0;
  const facts = [
    { label: "Whisper", value: voice.whisperReady ? "ready" : "not ready" },
    { label: "Queued", value: String(voice.queuedCount) },
    { label: "Transcribing", value: String(voice.processingCount) },
    { label: "Waiting to be retried", value: String(voice.retryableCount) },
    {
      label: "Next retry",
      value: voice.nextRetryAt
        ? formatTime(voice.nextRetryAt, timeZone)
        : "none",
    },
  ];
  if (!voice.whisperReady) {
    return {
      ...base,
      state: "problem",
      summary: "Whisper speech-to-text is not ready.",
      action:
        "Recordings are kept and transcribed once Whisper is ready. On first start it downloads its model; otherwise check the whisper service.",
      facts,
    };
  }
  if (voice.workerStalled) {
    return {
      ...base,
      state: "problem",
      summary: "The Voice worker is not picking up recordings.",
      action:
        "Recordings are kept. Restart the voice-worker service; expired work is reclaimed automatically.",
      facts,
    };
  }
  if (voice.retryableCount > 0) {
    return {
      ...base,
      state: "warning",
      summary: `${voice.retryableCount} recording${voice.retryableCount === 1 ? " is" : "s are"} waiting to be retried.`,
      detail: voice.lastFailureSummary ?? undefined,
      action: "They are retried automatically at the time shown.",
      facts,
    };
  }
  if (age > PROLONGED_BACKLOG_MS) {
    return {
      ...base,
      state: "warning",
      summary: `The oldest recording has waited ${formatAge(age)}.`,
      facts,
    };
  }
  return {
    ...base,
    state: "ok",
    summary:
      waiting === 0
        ? "Idle."
        : `${waiting} recording${waiting === 1 ? "" : "s"} in progress.`,
    facts,
  };
}

/**
 * A feed that fails this many refreshes in a row needs attention: Status
 * always shows a failed refresh, Home only from here on. Fewer failures are
 * retried automatically and usually pass.
 */
export const SOURCE_FAILURE_ALERT = 3;

/** What a feed's last refresh changed, such as "3 added, 1 cancelled". */
function refreshCounts(counts: FeedHealth["refresh"]["lastCounts"]) {
  if (!counts) return "no changes";
  const parts = (
    [
      [counts.inserted, "added"],
      [counts.updated, "changed"],
      [counts.removed, "cancelled"],
      [counts.restored, "restored"],
    ] as const
  )
    .filter(([count]) => count > 0)
    .map(([count, verb]) => `${count} ${verb}`);
  return parts.length ? parts.join(", ") : "no changes";
}

function lastRefreshLabel(source: FeedHealth, timeZone: string) {
  const { refresh } = source;
  const at = refresh.lastAttemptAt
    ? ` (${formatTime(refresh.lastAttemptAt, timeZone)})`
    : "";
  switch (refresh.lastOutcome) {
    case "imported":
      return `imported, ${refreshCounts(refresh.lastCounts)}${at}`;
    case "not_modified":
      return `feed unchanged${at}`;
    case "failed":
      return `failed${at}`;
    default:
      return "not refreshed yet";
  }
}

/** One feed's refresh health. Its URL is never shown: it can be personal. */
export function sourcePart(source: FeedHealth, timeZone: string): StatusPart {
  const { refresh } = source;
  const failures = refresh.consecutiveFailures;
  const failing = source.enabled && refresh.lastOutcome === "failed";
  const facts: StatusFact[] = [
    { label: "Last refresh", value: lastRefreshLabel(source, timeZone) },
    {
      label: "Last success",
      value: formatTime(refresh.lastSuccessAt, timeZone),
    },
  ];
  if (failures > 0) {
    facts.push({ label: "Failed in a row", value: String(failures) });
  }
  facts.push({
    label: "Next refresh",
    value: !source.enabled
      ? "none (disabled)"
      : refresh.refreshing
        ? "running now"
        : refresh.nextRefreshAt
          ? formatTime(refresh.nextRefreshAt, timeZone)
          : "not scheduled",
  });
  const base = { id: source.id, title: source.name, facts };
  if (!source.enabled) {
    return {
      ...base,
      state: "ok",
      summary: "Disabled: not refreshed; its events stay as they are.",
    };
  }
  if (failing) {
    return {
      ...base,
      state: "warning",
      summary:
        failures >= SOURCE_FAILURE_ALERT
          ? `Keeps failing: ${failures} refreshes in a row. Its events from the last good refresh are kept.`
          : "The last refresh failed; it is retried automatically. Its events from the last good refresh are kept.",
      detail: refresh.lastError ?? undefined,
    };
  }
  return {
    ...base,
    state: "ok",
    summary: refresh.lastOutcome
      ? "Refreshing normally."
      : "Waiting for its first refresh.",
  };
}

/**
 * Each feed with the name and enabled its Calendar source has now (the
 * owner changes them in Calendar); a feed whose source is not listed keeps
 * its own copies.
 */
export function feedHealth(
  feeds: CalendarFeed[],
  sources: CalendarSource[] | null | undefined,
): FeedHealth[] {
  return feeds.map((feed) => {
    const source = sources?.find((entry) => entry.id === feed.sourceId);
    return {
      id: feed.id,
      name: source?.name ?? feed.name,
      enabled: source?.enabled ?? feed.enabled,
      refresh: feed.refresh,
    };
  });
}

function calendarCheck(
  feeds: CalendarFeed[] | null,
  calendarSources: CalendarSource[] | null | undefined,
  timeZone: string,
): StatusCheck {
  const base = { id: "calendar" as const, title: "Calendar sources" };
  if (feeds === null) {
    return {
      ...base,
      state: "problem",
      summary: "Calendar sources are unavailable.",
      action:
        "Subscribed feeds are not refreshed until the calendar-feeds service is back. Their events stay as they are.",
      facts: [],
    };
  }
  const sources = feedHealth(feeds, calendarSources);
  if (sources.length === 0) {
    return {
      ...base,
      state: "ok",
      summary: "No calendar feeds are subscribed.",
      facts: [],
    };
  }
  const parts = sources.map((source) => sourcePart(source, timeZone));
  const failing = sources.filter(
    (source) =>
      source.enabled &&
      source.refresh.lastOutcome === "failed" &&
      source.refresh.consecutiveFailures >= SOURCE_FAILURE_ALERT,
  );
  const retrying = sources.filter(
    (source) =>
      source.enabled &&
      source.refresh.lastOutcome === "failed" &&
      source.refresh.consecutiveFailures < SOURCE_FAILURE_ALERT,
  );
  if (failing.length) {
    const [first] = failing;
    return {
      ...base,
      state: "warning",
      summary:
        failing.length === 1
          ? `The calendar feed ${first.name} has failed ${first.refresh.consecutiveFailures} refreshes in a row.`
          : `${failing.length} calendar feeds keep failing to refresh.`,
      action:
        "Their events from the last good refresh are kept, and they are retried automatically with growing pauses. If this persists, check that the feed still exists.",
      facts: [],
      parts,
    };
  }
  const count = `${sources.length} calendar feed${sources.length === 1 ? "" : "s"}`;
  return {
    ...base,
    state: "ok",
    summary: retrying.length
      ? `${retrying.map((source) => source.name).join(", ")}: the last refresh failed and is retried automatically.`
      : `${count} refreshing normally.`,
    facts: [],
    parts,
  };
}

/** What to do when the Google connection stops working. */
export const GOOGLE_RECONNECT = "Reconnect Google in Calendar.";

/** What to do when a synced Google calendar keeps failing to sync. */
export const GOOGLE_SYNC_RETRY =
  "Its events from the last good sync are kept, and the sync is retried automatically with growing pauses. If this persists, open Calendar → Google Calendar to check the calendar is still listed, or reconnect Google.";

/** Failed syncs in a row after which a synced calendar needs attention. */
export const GOOGLE_SYNC_ALERT = 3;

/**
 * The Google Calendar connection as a part of the Calendar check, with the
 * sync of the calendars chosen to sync (when read). Nothing to show while
 * Google is neither set up nor connected.
 */
export function googlePart(
  connection: GoogleConnection,
  timeZone: string,
  calendars?: GoogleCalendar[] | null,
): StatusPart | undefined {
  const base = { id: "google", title: "Google Calendar" };
  const account = connection.accountEmail ?? "your Google account";
  const facts: StatusFact[] = connection.accountEmail
    ? [
        { label: "Account", value: connection.accountEmail },
        {
          label: "Connected",
          value: formatTime(connection.connectedAt, timeZone),
        },
      ]
    : [];
  if (!connection.configured) {
    if (connection.status === "not_connected") return undefined;
    return {
      ...base,
      state: "warning",
      summary: `${account} is connected, but this server no longer has its Google settings.`,
      facts,
    };
  }
  switch (connection.status) {
    case "not_connected":
      return {
        ...base,
        state: "ok",
        summary: "Not connected. Connect Google in Calendar to sync it.",
        facts,
      };
    case "expired":
      return {
        ...base,
        state: "warning",
        summary: `Google access for ${account} expired or was revoked.`,
        detail: connection.lastError ?? undefined,
        facts,
      };
    case "error":
      return {
        ...base,
        state: "warning",
        summary: `Google refused the connection for ${account}.`,
        detail: connection.lastError ?? undefined,
        facts,
      };
  }
  const missing = missingPermissions(connection);
  if (missing.length) {
    facts.push({ label: "Not granted", value: missing.join("; ") });
  }
  const synced = (calendars ?? []).filter((calendar) => calendar.syncEnabled);
  if (calendars) {
    facts.push({
      label: "Syncing",
      value: synced.length
        ? synced.map((calendar) => calendar.summary).join(", ")
        : "no calendar chosen",
    });
  }
  if (synced.length) {
    const last = synced
      .map((calendar) => calendar.sync.lastSyncedAt)
      .filter((value): value is string => Boolean(value))
      .sort()
      .at(-1);
    facts.push({
      label: "Last sync",
      value: last ? formatTime(last, timeZone) : "not yet",
    });
  }
  // Events created in Life OS, exported to the Life OS calendar.
  const exports = connection.export;
  if (exports) {
    const name = connection.lifeOsCalendar?.summary ?? "Life OS";
    facts.push({
      label: "Export",
      value: !connection.exportEnabled
        ? "off"
        : exports.failed
          ? `${exports.failed} failing, retried automatically`
          : exports.pending
            ? `${exports.pending} waiting for “${name}”`
            : `up to date in “${name}”`,
    });
  }
  if (connection.lifeOsCalendar?.note) {
    facts.push({
      label: "Life OS calendar",
      value: connection.lifeOsCalendar.note,
    });
  }
  const failing = synced.filter(
    (calendar) => calendar.sync.state === "failing",
  );
  const stuck = failing.filter(
    (calendar) => calendar.sync.consecutiveFailures >= GOOGLE_SYNC_ALERT,
  );
  if (stuck.length) {
    const [first] = stuck;
    return {
      ...base,
      state: "warning",
      summary:
        stuck.length === 1
          ? `Syncing ${first.summary} keeps failing: ${first.sync.consecutiveFailures} attempts in a row.`
          : `${stuck.length} Google calendars keep failing to sync.`,
      detail: first.sync.lastError ?? undefined,
      facts,
    };
  }
  if (failing.length) {
    return {
      ...base,
      state: "ok",
      summary: `Connected as ${account}. The last sync of ${failing.map((calendar) => calendar.summary).join(", ")} failed; it is retried automatically.`,
      detail: failing[0].sync.lastError ?? undefined,
      facts,
    };
  }
  if (connection.exportEnabled && exports?.failed) {
    return {
      ...base,
      state: "ok",
      summary: `Connected as ${account}. ${exports.failed} event${exports.failed === 1 ? "" : "s"} created in Life OS could not be exported to Google yet; retried automatically.`,
      detail: exports.lastError ?? undefined,
      facts,
    };
  }
  return { ...base, state: "ok", summary: `Connected as ${account}.`, facts };
}

/** Adds the Google part to the Calendar check; trouble with it needs action. */
function withGoogle(
  check: StatusCheck,
  connection: GoogleConnection | null | undefined,
  timeZone: string,
  calendars?: GoogleCalendar[] | null,
): StatusCheck {
  const part = connection
    ? googlePart(connection, timeZone, calendars)
    : undefined;
  if (!part) return check;
  const parts = [...(check.parts ?? []), part];
  if (part.state === "ok" || check.state !== "ok") return { ...check, parts };
  return {
    ...check,
    state: part.state,
    summary: part.summary,
    action: !connection?.configured
      ? "Add the LIFE_OS_GOOGLE_* settings to the server's .env again, or disconnect Google in Calendar."
      : connection.status === "connected"
        ? GOOGLE_SYNC_RETRY
        : GOOGLE_RECONNECT,
    parts,
  };
}

const CHANNEL_TITLES: Record<string, string> = {
  ntfy: "ntfy (phone)",
  log: "Service log",
};

/** What to do when deliveries fail. */
export const NOTIFICATIONS_FAILING_ACTION =
  "Notifications stay listed in Life OS, and failed deliveries are retried for up to 24 hours. For ntfy, check the ntfy service and its token (docs/operations/ntfy.md).";

/** One delivery channel's health, from the outcomes of its deliveries. */
export function channelPart(
  channel: NotificationChannel,
  timeZone: string,
): StatusPart {
  const facts: StatusFact[] = [
    {
      label: "Last delivered",
      value: formatTime(channel.lastSuccessAt, timeZone),
    },
    {
      label: "Last failure",
      value: formatTime(channel.lastFailureAt, timeZone),
    },
    {
      label: "Failed in the last 24 h",
      value: String(channel.recentFailures),
    },
  ];
  if (channel.retrying > 0) {
    facts.push({
      label: "Waiting for a retry",
      value: String(channel.retrying),
    });
  }
  const base = {
    id: channel.name,
    title: CHANNEL_TITLES[channel.name] ?? channel.name,
    facts,
  };
  switch (channel.state) {
    case "failing":
      return {
        ...base,
        state: "warning",
        summary:
          channel.retrying > 0
            ? "Deliveries are failing; they are retried automatically."
            : "The latest delivery failed.",
        detail: channel.lastError ?? undefined,
      };
    case "idle":
      return { ...base, state: "ok", summary: "Set up; nothing sent yet." };
    default:
      return { ...base, state: "ok", summary: "Delivering normally." };
  }
}

function notificationsCheck(
  channels: NotificationChannel[] | null,
  timeZone: string,
): StatusCheck {
  const base = { id: "notifications" as const, title: "Notifications" };
  if (channels === null) {
    return {
      ...base,
      state: "warning",
      summary: "Notification delivery is unavailable.",
      action: "Nothing can notify you until the notifications service is back.",
      facts: [],
    };
  }
  if (channels.length === 0) {
    return {
      ...base,
      state: "neutral",
      summary:
        "No delivery channel is set up: notifications are listed in Life OS only.",
      action:
        "To get them on your phone, set up ntfy (docs/operations/ntfy.md).",
      facts: [],
    };
  }
  const parts = channels.map((channel) => channelPart(channel, timeZone));
  const failing = parts.filter((part) => part.state !== "ok");
  if (failing.length) {
    return {
      ...base,
      state: "warning",
      summary:
        failing.length === 1
          ? `Notifications are not reaching ${failing[0].title}.`
          : `Notifications are not reaching ${failing.map((part) => part.title).join(", ")}.`,
      action: NOTIFICATIONS_FAILING_ACTION,
      facts: [],
      parts,
    };
  }
  return {
    ...base,
    state: "ok",
    summary: `Delivering to ${parts.map((part) => part.title).join(", ")}.`,
    facts: [],
    parts,
  };
}

/** What to do when the bank connection needs the owner. */
export const BANK_RECONNECT =
  "Reconnect the bank in Finance (Bank connection → Reconnect). Its accounts and history stay.";

/** What to do when bank syncs keep failing. */
export const BANK_SYNC_RETRY =
  "Transactions from the last good sync are kept, and the sync is retried automatically. If this persists, see docs/operations/banking.md.";

/** Failed syncs in a row after which a bank connection needs attention. */
export const BANK_FAILURE_ALERT = 3;

/** One bank connection's consent and sync state. */
export function bankPart(
  connection: BankConnection,
  now: Date,
  timeZone: string,
): StatusPart {
  const name = connection.institution;
  const facts: StatusFact[] = [
    {
      label: "Accounts",
      value: connection.accounts.map(accountLabel).join(", ") || "none",
    },
    {
      label: "Last sync",
      value: formatTime(connection.lastSuccessAt, timeZone),
    },
  ];
  if (connection.nextSyncAt) {
    facts.push({
      label: "Next sync",
      value: formatTime(connection.nextSyncAt, timeZone),
    });
  }
  if (connection.consentValidUntil) {
    facts.push({
      label: "Access until",
      value: formatTime(connection.consentValidUntil, timeZone),
    });
  }
  const base = { id: `bank-${connection.id}`, title: name, facts };
  const detail = connection.lastError ?? undefined;
  switch (connection.status) {
    case "expired":
      return {
        ...base,
        state: "warning",
        summary: `${name} access expired; transactions no longer sync.`,
        detail,
      };
    case "error":
      return {
        ...base,
        state: "warning",
        summary: `${name} needs you to connect again.`,
        detail,
      };
  }
  if (connection.consecutiveFailures >= BANK_FAILURE_ALERT) {
    return {
      ...base,
      state: "warning",
      summary: `Syncing ${name} keeps failing: ${connection.consecutiveFailures} attempts in a row.`,
      detail,
    };
  }
  // A consent ending within 7 days is shown but stays OK: banking's own
  // 7-day and 1-day reminders tell the owner, so the monitor (which
  // notifies on warnings) does not tell them twice. Once it has ended, the
  // connection is expired, above.
  if (expiringSoon(connection, now) && connection.consentValidUntil) {
    return {
      ...base,
      state: "ok",
      summary: `${name} access ends on ${formatTime(connection.consentValidUntil, timeZone)}.`,
    };
  }
  if (connection.consecutiveFailures > 0) {
    return {
      ...base,
      state: "ok",
      summary: `Connected. The last sync failed; it is retried automatically.`,
      detail,
    };
  }
  return {
    ...base,
    state: "ok",
    summary: connection.lastSuccessAt
      ? "Connected and syncing."
      : "Connected; importing transactions.",
  };
}

function bankingCheck(
  list: BankConnectionList | null,
  now: Date,
  timeZone: string,
): StatusCheck {
  const base = { id: "banking" as const, title: "Bank connection" };
  if (list === null) {
    return {
      ...base,
      state: "warning",
      summary: "The bank connection is unavailable.",
      action:
        "Bank transactions do not sync until the banking service is back.",
      facts: [],
    };
  }
  if (!list.configured) {
    return list.setupProblem
      ? {
          ...base,
          state: "warning",
          summary: "The bank connection is set up incompletely.",
          detail: list.setupProblem,
          action:
            "Fix the Enable Banking settings in the server's .env and restart banking (docs/operations/banking.md).",
          facts: [],
        }
      : {
          ...base,
          state: "neutral",
          summary: "No bank connection is set up.",
          action:
            "To sync your bank into Finance, set up Enable Banking (docs/operations/banking.md).",
          facts: [],
        };
  }
  const live = list.items.filter((item) => item.status !== "disconnected");
  if (live.length === 0) {
    return {
      ...base,
      state: "neutral",
      summary: "No bank is connected.",
      action: "Connect your bank in Finance to sync its transactions.",
      facts: [],
    };
  }
  const parts = live.map((connection) => bankPart(connection, now, timeZone));
  const trouble = parts.filter((part) => part.state !== "ok");
  if (trouble.length) {
    const [first] = trouble;
    const connection = live[parts.indexOf(first)];
    const reconnect =
      connection.status === "expired" || connection.status === "error";
    return {
      ...base,
      state: worst(trouble.map((part) => part.state)),
      summary: first.summary,
      detail: first.detail,
      action: reconnect ? BANK_RECONNECT : BANK_SYNC_RETRY,
      facts: [],
      parts,
    };
  }
  return {
    ...base,
    state: "ok",
    summary:
      parts.length !== 1
        ? `${parts.length} banks connected.`
        : parts[0].summary.startsWith(parts[0].title)
          ? parts[0].summary
          : `${parts[0].title}: ${parts[0].summary}`,
    facts: [],
    parts,
  };
}

function servicesCheck(services: RuntimeSignals["services"]): StatusCheck {
  const down = services.filter((service) => !service.ready);
  return {
    id: "services",
    title: "Services",
    state: down.length ? "problem" : "ok",
    summary: down.length
      ? `${down.map((service) => service.name).join(", ")} ${down.length === 1 ? "is" : "are"} not ready.`
      : "All services are ready.",
    action: down.length
      ? "Pages that use these services show them as unavailable until they are back."
      : undefined,
    facts: services.map((service) => ({
      label: service.name,
      value: service.ready ? "ready" : "not ready",
    })),
  };
}

export function toRuntimeStatus(
  signals: RuntimeSignals,
  now: Date,
  timeZone: string = DEFAULT_TIME_ZONE,
  configured: string = timeZone,
): RuntimeStatus {
  const checks = [
    processingCheck(signals.processor, timeZone, configured),
    inboxCheck(signals.inbox, now, timeZone),
    voiceCheck(signals.voice, now, timeZone),
    ...(signals.calendarFeeds === undefined
      ? []
      : [
          withGoogle(
            calendarCheck(
              signals.calendarFeeds,
              signals.calendarSources,
              timeZone,
            ),
            signals.googleConnection,
            timeZone,
            signals.googleCalendars,
          ),
        ]),
    ...(signals.notificationChannels === undefined
      ? []
      : [notificationsCheck(signals.notificationChannels, timeZone)]),
    ...(signals.bankConnections === undefined
      ? []
      : [bankingCheck(signals.bankConnections, now, timeZone)]),
    servicesCheck(signals.services),
  ];
  return {
    checkedAt: now.toISOString(),
    timeZone,
    overall: worst(checks.map((check) => check.state)),
    checks,
  };
}

/** Checks that need the user's attention, most severe first. */
export function statusAlerts(status: RuntimeStatus) {
  return status.checks
    .filter((check) => SEVERITY[check.state] > 0)
    .sort((a, b) => SEVERITY[b.state] - SEVERITY[a.state]);
}

// ---- Loading ---------------------------------------------------------------

const PROBE_TIMEOUT_MS = 2500;

const processorStatusUrl = normalizeServiceUrl(
  "PROCESSOR_STATUS_URL",
  process.env.PROCESSOR_STATUS_URL ?? "http://localhost:8091",
);

/**
 * The monitor (docs/services/monitor.md) has no API of its own. It is listed
 * under Services so that a stopped monitor, which means no status
 * notifications, shows here.
 */
const monitorUrl = normalizeServiceUrl(
  "MONITOR_SERVICE_URL",
  process.env.MONITOR_SERVICE_URL ?? "http://localhost:8088",
);

async function getJson<T>(url: string): Promise<T | null> {
  try {
    const response = await fetch(url, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return response.ok ? ((await response.json()) as T) : null;
  } catch {
    return null;
  }
}

async function ready(url: string) {
  try {
    const response = await fetch(`${url}/ready`, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return response.ok;
  } catch {
    return false;
  }
}

const SERVICE_NAMES: Record<keyof typeof serviceBaseUrls, string> = {
  tasks: "Tasks",
  inbox: "Inbox",
  calendar: "Calendar",
  finance: "Finance",
  voice: "Voice",
  activity: "Activity",
  notifications: "Notifications",
  banking: "Banking",
  calendarFeeds: "Calendar feeds",
};

export async function getRuntimeStatus(): Promise<RuntimeStatus> {
  const timeout = { signal: AbortSignal.timeout(PROBE_TIMEOUT_MS) };
  const notificationChannels = notificationsClient
    .GET("/v1/channels", timeout)
    .then(({ data }) => data?.items ?? null)
    .catch(() => null);
  const bankConnections = bankingClient
    .GET("/v1/connections", timeout)
    .then(({ data }) => data ?? null)
    .catch(() => null);
  const [
    processor,
    inbox,
    voice,
    calendarFeeds,
    calendarSources,
    googleConnection,
    googleCalendars,
    services,
  ] = await Promise.all([
    getJson<ProcessorStatus>(`${processorStatusUrl}/status`),
    inboxClient
      .GET("/v1/backlog", timeout)
      .then(({ data }) => data ?? null)
      .catch(() => null),
    voiceClient
      .GET("/v1/backlog", timeout)
      .then(({ data }) => data ?? null)
      .catch(() => null),
    calendarFeedsClient
      .GET("/v1/feeds", timeout)
      .then(({ data }) => data?.items ?? null)
      .catch(() => null),
    calendarClient
      .GET("/v1/sources", timeout)
      .then(({ data }) => data?.items ?? null)
      .catch(() => null),
    calendarClient
      .GET("/v1/google/connection", timeout)
      .then(({ data }) => data ?? null)
      .catch(() => null),
    calendarClient
      .GET("/v1/google/calendars", timeout)
      .then(({ data }) => data?.items ?? null)
      .catch(() => null),
    Promise.all([
      ...(Object.keys(SERVICE_NAMES) as (keyof typeof serviceBaseUrls)[]).map(
        async (key) => ({
          name: SERVICE_NAMES[key],
          ready: await ready(serviceBaseUrls[key]),
        }),
      ),
      ready(monitorUrl).then((ok) => ({ name: "Monitor", ready: ok })),
    ]),
  ]);
  return toRuntimeStatus(
    {
      processor,
      inbox,
      voice,
      calendarFeeds,
      calendarSources,
      googleConnection,
      notificationChannels: await notificationChannels,
      googleCalendars,
      bankConnections: await bankConnections,
      services,
    },
    new Date(),
    await userTimeZone(),
    configuredTimeZone(),
  );
}
