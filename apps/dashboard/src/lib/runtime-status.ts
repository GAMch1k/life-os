import { normalizeServiceUrl, serviceBaseUrls } from "@/config/services";
import { calendarClient } from "@/lib/api/calendar-client";
import type { components as CalendarComponents } from "@/lib/api/generated/calendar";
import { inboxClient } from "@/lib/api/inbox-client";
import type { components as InboxComponents } from "@/lib/api/generated/inbox";
import type { components as VoiceComponents } from "@/lib/api/generated/voice";
import { voiceClient } from "@/lib/api/voice-client";
import {
  type GoogleConnection,
  missingPermissions,
} from "@/lib/google-calendar";
import { DEFAULT_TIME_ZONE, dateTimeFormat } from "@/lib/time-zone";
import { configuredTimeZone, userTimeZone } from "@/lib/user-time-zone";

// The Status page turns supported runtime state into actionable checks:
// - the processor's private /status endpoint (what the running process saw),
// - the Inbox and Voice backlog APIs (durable queue state),
// - the Calendar sources API (each subscribed feed's refresh state) and its
//   Google connection,
// - each owning service's /ready endpoint.
// Nothing here is inferred from UI behaviour.

export type InboxBacklog = InboxComponents["schemas"]["InboxBacklog"];
export type AudioBacklog = VoiceComponents["schemas"]["AudioBacklog"];
export type CalendarSource = CalendarComponents["schemas"]["CalendarSource"];

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

export type CheckState = "ok" | "warning" | "problem";

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
  id: "processing" | "inbox" | "voice" | "calendar" | "services";
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
   * Calendar sources; null when Calendar could not be read, absent when not
   * checked (the check is left out).
   */
  calendarSources?: CalendarSource[] | null;
  /** The Google Calendar connection; null or absent when not read. */
  googleConnection?: GoogleConnection | null;
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

const SEVERITY: Record<CheckState, number> = { ok: 0, warning: 1, problem: 2 };

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
function refreshCounts(counts: CalendarSource["refresh"]["lastCounts"]) {
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

function lastRefreshLabel(source: CalendarSource, timeZone: string) {
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
export function sourcePart(
  source: CalendarSource,
  timeZone: string,
): StatusPart {
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

function calendarCheck(
  sources: CalendarSource[] | null,
  timeZone: string,
): StatusCheck {
  const base = { id: "calendar" as const, title: "Calendar sources" };
  if (sources === null) {
    return {
      ...base,
      state: "problem",
      summary: "Calendar sources are unavailable.",
      action:
        "Subscribed feeds are not refreshed until Calendar is back. Their events stay as they are.",
      facts: [],
    };
  }
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
  const feeds = `${sources.length} calendar feed${sources.length === 1 ? "" : "s"}`;
  return {
    ...base,
    state: "ok",
    summary: retrying.length
      ? `${retrying.map((source) => source.name).join(", ")}: the last refresh failed and is retried automatically.`
      : `${feeds} refreshing normally.`,
    facts: [],
    parts,
  };
}

/** What to do when the Google connection stops working. */
export const GOOGLE_RECONNECT = "Reconnect Google in Calendar.";

/**
 * The Google Calendar connection as a part of the Calendar check. Nothing
 * to show while Google is neither set up nor connected.
 */
export function googlePart(
  connection: GoogleConnection,
  timeZone: string,
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
  return { ...base, state: "ok", summary: `Connected as ${account}.`, facts };
}

/** Adds the Google part to the Calendar check; trouble with it needs action. */
function withGoogle(
  check: StatusCheck,
  connection: GoogleConnection | null | undefined,
  timeZone: string,
): StatusCheck {
  const part = connection ? googlePart(connection, timeZone) : undefined;
  if (!part) return check;
  const parts = [...(check.parts ?? []), part];
  if (part.state === "ok" || check.state !== "ok") return { ...check, parts };
  return {
    ...check,
    state: part.state,
    summary: part.summary,
    action: connection?.configured
      ? GOOGLE_RECONNECT
      : "Add the LIFE_OS_GOOGLE_* settings to the server's .env again, or disconnect Google in Calendar.",
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
    ...(signals.calendarSources === undefined
      ? []
      : [
          withGoogle(
            calendarCheck(signals.calendarSources, timeZone),
            signals.googleConnection,
            timeZone,
          ),
        ]),
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
    .filter((check) => check.state !== "ok")
    .sort((a, b) => SEVERITY[b.state] - SEVERITY[a.state]);
}

// ---- Loading ---------------------------------------------------------------

const PROBE_TIMEOUT_MS = 2500;

const processorStatusUrl = normalizeServiceUrl(
  "PROCESSOR_STATUS_URL",
  process.env.PROCESSOR_STATUS_URL ?? "http://localhost:8091",
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
};

export async function getRuntimeStatus(): Promise<RuntimeStatus> {
  const timeout = { signal: AbortSignal.timeout(PROBE_TIMEOUT_MS) };
  const [processor, inbox, voice, calendarSources, googleConnection, services] =
    await Promise.all([
      getJson<ProcessorStatus>(`${processorStatusUrl}/status`),
      inboxClient
        .GET("/v1/backlog", timeout)
        .then(({ data }) => data ?? null)
        .catch(() => null),
      voiceClient
        .GET("/v1/backlog", timeout)
        .then(({ data }) => data ?? null)
        .catch(() => null),
      calendarClient
        .GET("/v1/sources", timeout)
        .then(({ data }) => data?.items ?? null)
        .catch(() => null),
      calendarClient
        .GET("/v1/google/connection", timeout)
        .then(({ data }) => data ?? null)
        .catch(() => null),
      Promise.all(
        (Object.keys(SERVICE_NAMES) as (keyof typeof serviceBaseUrls)[]).map(
          async (key) => ({
            name: SERVICE_NAMES[key],
            ready: await ready(serviceBaseUrls[key]),
          }),
        ),
      ),
    ]);
  return toRuntimeStatus(
    { processor, inbox, voice, calendarSources, googleConnection, services },
    new Date(),
    await userTimeZone(),
    configuredTimeZone(),
  );
}
