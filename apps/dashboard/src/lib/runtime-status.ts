import { normalizeServiceUrl, serviceBaseUrls } from "@/config/services";
import { backupClient } from "@/lib/api/backup-client";
import { bankingClient } from "@/lib/api/banking-client";
import { calendarClient } from "@/lib/api/calendar-client";
import { calendarFeedsClient } from "@/lib/api/calendar-feeds-client";
import type { components as BackupComponents } from "@/lib/api/generated/backup";
import type { components as CalendarComponents } from "@/lib/api/generated/calendar";
import type { components as HostHealthComponents } from "@/lib/api/generated/host-health";
import type { components as CalendarFeedsComponents } from "@/lib/api/generated/calendar-feeds";
import { foodClient } from "@/lib/api/food-client";
import type { components as FoodComponents } from "@/lib/api/generated/food";
import { googleCalendarClient } from "@/lib/api/google-calendar-client";
import { hostHealthClient } from "@/lib/api/host-health-client";
import { inboxClient } from "@/lib/api/inbox-client";
import type { components as InboxComponents } from "@/lib/api/generated/inbox";
import type { components as NotificationsComponents } from "@/lib/api/generated/notifications";
import type { components as TasksComponents } from "@/lib/api/generated/tasks";
import { notificationsClient } from "@/lib/api/notifications-client";
import { tasksClient } from "@/lib/api/tasks-client";
import type { components as VoiceComponents } from "@/lib/api/generated/voice";
import { voiceClient } from "@/lib/api/voice-client";
import {
  changesUnreadable,
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
import { formatBinaryBytes } from "@/lib/format-bytes";
import { monthLabel } from "@/lib/finance-summary";
import { formatUsage } from "@/lib/format-usage";
import {
  type DashboardService,
  type InstalledParts,
  installed,
} from "@/lib/modules";
import { downsampleMax, downsampleMin, type SparkPoint } from "@/lib/sparkline";
import { DEFAULT_TIME_ZONE, dateTimeFormat } from "@/lib/time-zone";
import { configuredTimeZone, userTimeZone } from "@/lib/user-time-zone";

// The Status page turns supported runtime state into actionable checks:
// - the processor's private /status endpoint (what the running process saw),
// - the Inbox and Voice backlog APIs (durable queue state),
// - the calendar-feeds integration (each subscribed feed's refresh state,
//   with its name and enabled from Calendar's sources),
// - the Google Calendar integration's connection (with its exports and the
//   edits on their way to Google) and the sync state of its calendars,
// - the Notifications channels API (each delivery channel's recent outcomes),
// - the reminder delivery health Calendar, Tasks, and Food report (their
//   senders' last checks for due reminders),
// - the banking integration's connections (consent and sync state),
// - the backup service's status (the last backups and restore check),
// - host-health's view of the server (memory, load, disks, containers),
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
/** Calendar's reminder delivery health (`GET /v1/reminders/status`). */
export type ReminderDelivery =
  CalendarComponents["schemas"]["ReminderDeliveryStatus"];
/** Tasks' notification delivery health (`GET /v1/notifications/status`). */
export type TaskNotifications =
  TasksComponents["schemas"]["TaskNotificationStatus"];
/** The backup service's status (`GET /v1/status`). */
export type BackupStatus = BackupComponents["schemas"]["BackupStatus"];
/** host-health's view of the server (`GET /v1/status`). */
export type HostStatus = HostHealthComponents["schemas"]["HostStatus"];
/** host-health's samples (`GET /v1/history`). */
export type HostHistory = HostHealthComponents["schemas"]["HostHistory"];
/** Food's expiry reminder health (`GET /v1/notifications/status`). */
export type FoodNotifications =
  FoodComponents["schemas"]["FoodNotificationStatus"];

/** Mirrors `Status` in services/mcp/internal/processor/status.go. */
export type ProcessorStatus = {
  /**
   * The AI runtime (LIFE_OS_AI_PROVIDER) and its name as the owner reads
   * it; absent from a processor older than the choice, which runs Codex.
   */
  provider?: "codex" | "claude";
  providerName?: string;
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
  /** The AI runtime's state, Codex or Claude Code (the field keeps its name). */
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
  /** The AI runtime's runs (see docs/services/mcp.md). */
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
  /** The recipe ideas job (see docs/services/mcp.md). */
  ideas?: {
    /** When the daily run is due ("16:00"); absent when it is off. */
    dailyAt?: string;
    webSearch: string;
    nextRunAt: string | null;
    running: boolean;
    queued: boolean;
    lastRun: {
      trigger: string;
      finishedAt: string;
      created: number;
      /** The first preferred cooking method, and how many ideas use it. */
      firstMethod?: string;
      withFirstMethod?: number;
      /** How many ideas were found on a web page. */
      fromWeb?: number;
      outcome: string;
      error?: string;
    } | null;
  };
  /** The AI categories job (see docs/services/mcp.md#ai-categories). */
  categories?: {
    enabled: boolean;
    running: boolean;
    nextRunAt: string | null;
    /** Transactions waiting at the last check; null before it. */
    waiting: number | null;
    lastCheckAt: string | null;
    batchesToday: number;
    lastRun: {
      finishedAt: string;
      examined: number;
      categorized: number;
      skipped: number;
      unsure: number;
      rejected: number;
      chosenMeanwhile: number;
      outcome: string;
      error?: string;
    } | null;
  };
  /** The AI budget plan job (see docs/services/mcp.md#ai-budget-plans). */
  budgetPlans?: {
    /** LIFE_OS_AI_BUDGET_PLANS. */
    enabled: boolean;
    /** LIFE_OS_AI_BUDGET_PLAN_MONTHLY: the plan on the 1st of each month. */
    monthly: boolean;
    running: boolean;
    /** Null when the job is off. */
    nextCheckAt: string | null;
    /** The next 1st-of-month run; null when it or the job is off. */
    nextMonthlyAt: string | null;
    /** Plans waiting at the last check; null before it. */
    waiting: number | null;
    lastCheckAt: string | null;
    lastRun: {
      planId: string;
      /** YYYY-MM. */
      month: string;
      trigger: "request" | "schedule" | "salary";
      startedAt: string;
      finishedAt: string;
      attempt: number;
      outcome: "ready" | "retry" | "failed" | "dropped";
      categories: number;
      suggestions: number;
      error?: string;
    } | null;
  };
  /** Account usage as the AI runtime's CLI reported it (see docs/services/mcp.md). */
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

/**
 * The AI runtime's name as the owner reads it ("Codex", "Claude Code"): the
 * processor's own, Codex for a processor older than the choice, and "AI"
 * while the processor cannot be read.
 */
export function aiName(processor: ProcessorStatus | null | undefined) {
  if (!processor) return "AI";
  return processor.providerName ?? "Codex";
}

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
  name = "Codex",
) {
  if (!runtime) return [];
  const facts = [
    {
      label: `${name} run now`,
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
 * The recipe ideas job in two facts: what its last run brought, and when
 * it runs next.
 */
export function ideasFacts(
  ideas: ProcessorStatus["ideas"],
  timeZone: string = DEFAULT_TIME_ZONE,
) {
  if (!ideas) return [];
  const run = ideas.lastRun;
  const outcomes: Record<string, string> = {
    empty_pantry: "skipped: nothing in stock",
    done_today: "skipped: ideas already left today",
    none: "no new ideas",
    failed: `failed${run?.error ? `: ${run.error}` : ""}`,
  };
  const details = [
    ...(run?.firstMethod
      ? [`${run.withFirstMethod ?? 0} ${run.firstMethod.replace(/_/g, " ")}`]
      : []),
    ...(run?.fromWeb ? [`${run.fromWeb} from the web`] : []),
  ];
  const made = `${run?.created ?? 0} new${run?.created && details.length ? ` (${details.join(", ")})` : ""}`;
  const last = run
    ? `${outcomes[run.outcome] ?? made}, ${formatTime(run.finishedAt, timeZone)}${run.trigger === "request" ? " (asked for)" : ""}`
    : "not run since the processor started";
  return [
    {
      label: "Recipe ideas",
      value: ideas.running
        ? "thinking of ideas now"
        : ideas.queued
          ? "asked for, starting soon"
          : last,
    },
    {
      label: "Next recipe ideas",
      value: ideas.nextRunAt
        ? formatTime(ideas.nextRunAt, timeZone)
        : "only when asked for",
    },
  ];
}

/**
 * The AI categories job in two facts: what its last batch did, and when it
 * checks next with how many transactions wait.
 */
export function categoriesFacts(
  categories: ProcessorStatus["categories"],
  timeZone: string = DEFAULT_TIME_ZONE,
) {
  if (!categories) return [];
  if (!categories.enabled)
    return [{ label: "AI categories", value: "off (LIFE_OS_AI_CATEGORIES)" }];
  const run = categories.lastRun;
  const skipped = run
    ? [
        ...(run.unsure ? [`${run.unsure} unsure`] : []),
        ...(run.rejected ? [`${run.rejected} rejected`] : []),
        ...(run.chosenMeanwhile
          ? [`${run.chosenMeanwhile} chosen meanwhile`]
          : []),
      ]
    : [];
  const result = run
    ? run.outcome === "failed"
      ? `failed${run.error ? `: ${run.error}` : ""}`
      : `${run.categorized} categorized, ${run.skipped} skipped${skipped.length ? ` (${skipped.join(", ")})` : ""}`
    : "";
  const waiting =
    categories.waiting === null
      ? ""
      : categories.waiting === 0
        ? ", nothing waiting"
        : `, ${categories.waiting} waiting`;
  return [
    {
      label: "AI categories",
      value: categories.running
        ? "categorizing now"
        : run
          ? `${result}, ${formatTime(run.finishedAt, timeZone)}`
          : "nothing categorized since the processor started",
    },
    {
      label: "Next AI categories",
      value: categories.nextRunAt
        ? `${formatTime(categories.nextRunAt, timeZone)}${waiting}`
        : "not scheduled",
    },
  ];
}

/**
 * The AI budget plan job in two facts: what its last run made, and when it
 * plans next (the 1st of the month, else only when asked for).
 */
export function budgetPlansFacts(
  budgetPlans: ProcessorStatus["budgetPlans"],
  timeZone: string = DEFAULT_TIME_ZONE,
) {
  if (!budgetPlans) return [];
  if (!budgetPlans.enabled)
    return [
      { label: "AI budget plans", value: "off (LIFE_OS_AI_BUDGET_PLANS)" },
    ];
  const run = budgetPlans.lastRun;
  let last = "nothing planned since the processor started";
  if (run) {
    const month = monthLabel(run.month);
    const error = run.error ? `: ${run.error}` : "";
    const outcome = {
      ready: `${month} ready (${run.suggestions} ${run.suggestions === 1 ? "way" : "ways"} to spend less)`,
      failed: `${month} failed${error}`,
      retry: `${month} will be tried again${error}`,
      dropped: `${month} dropped (dismissed meanwhile)`,
    }[run.outcome];
    last = `${outcome ?? `${month} ${run.outcome}`}, ${formatTime(run.finishedAt, timeZone)}`;
  }
  const waiting = budgetPlans.waiting ? `, ${budgetPlans.waiting} waiting` : "";
  return [
    {
      label: "AI budget plans",
      value: budgetPlans.running ? "planning now" : last,
    },
    {
      label: "Next AI budget plan",
      value: `${
        budgetPlans.nextMonthlyAt
          ? `monthly on ${formatTime(budgetPlans.nextMonthlyAt, timeZone)}`
          : "only when asked for"
      }${waiting}`,
    },
  ];
}

/** How the AI runtime is signed in, as its CLI reports it. */
const AUTH_MODES: Record<string, string> = {
  chatgpt: "ChatGPT sign-in",
  oauth_token: "Claude subscription token",
  "claude.ai": "Claude subscription sign-in",
  api_key: "Anthropic API key",
};

/**
 * Usage facts exactly as the AI runtime's CLI reported them: one line per
 * metered bucket and window. Nothing is estimated when the read is
 * unavailable.
 */
export function usageFacts(
  usage: ProcessorStatus["usage"],
  timeZone: string = DEFAULT_TIME_ZONE,
  name = "Codex",
) {
  const current = usage?.current;
  if (!current) {
    return [
      {
        label: `${name} usage`,
        value: usage?.error ? "unavailable" : "not read yet",
      },
    ];
  }
  const facts = [
    {
      label: `${name} plan`,
      value: [
        current.planType,
        current.authMode
          ? (AUTH_MODES[current.authMode] ?? current.authMode)
          : undefined,
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

/** The reset time of the exhausted limit, when the AI runtime reports one. */
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
    | "backups"
    | "server"
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
  /** A control the card offers: Backups' "Back up now". */
  control?: { kind: "backup-now"; running: boolean };
  /** A small trend the card draws, such as memory available over a day. */
  chart?: StatusChart;
  /** Smaller trends drawn side by side under it: Life OS's memory and CPU. */
  sideCharts?: StatusChart[];
  /**
   * An ok that cannot rule a warning out yet: the Server check while
   * host-health has watched the containers' restarts for less than its
   * window (after it starts). The monitor never takes it as a recovery
   * (docs/services/monitor.md).
   */
  provisional?: boolean;
};

/** A sparkline: values from 0 to max, with a warning level. */
export type StatusChart = {
  label: string;
  max: number;
  threshold?: number;
  points: SparkPoint[];
  /** What the values count: bytes (the default) or CPU cores. */
  unit?: "bytes" | "cores";
  /**
   * The worst moment its caption names: the lowest value (the default, as
   * for memory available) or the highest (as for use).
   */
  worst?: "lowest" | "highest";
};

export type RuntimeSignals = {
  /**
   * The processor's status; null when it could not be read, absent when AI
   * processing is off (its check is left out, and Inbox keeps captures as
   * written).
   */
  processor?: ProcessorStatus | null;
  inbox: InboxBacklog | null;
  /**
   * Voice's backlog; null when it could not be read, absent when the voice
   * notes module is off (the check is left out).
   */
  voice?: AudioBacklog | null;
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
   * Calendar's reminder delivery, an Event reminders part of the Calendar
   * check; null or absent when not read (the part is left out: Calendar's
   * own unavailability shows under Services).
   */
  eventReminders?: ReminderDelivery | null;
  /**
   * Tasks' reminders and daily lists, a Task reminders part of the
   * Notifications check; null or absent when not read (the part is left
   * out: Tasks' own unavailability shows under Services).
   */
  taskReminders?: TaskNotifications | null;
  /**
   * Food's daily expiry reminder, a Food reminders part of the
   * Notifications check; null or absent when not read (the part is left
   * out: Food's own unavailability shows under Services).
   */
  foodReminders?: FoodNotifications | null;
  /**
   * The bank connections; null when banking could not be read, absent when
   * not checked (the check is left out).
   */
  bankConnections?: BankConnectionList | null;
  /**
   * The backup service's status; null when it could not be read, absent
   * when not checked (the check is left out).
   */
  backups?: BackupStatus | null;
  /**
   * host-health's status and history; null when it could not be read,
   * absent when not checked (the check is left out).
   */
  server?: { status: HostStatus; history: HostHistory | null } | null;
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
        "Captures are kept in Inbox but nothing processes them. Start the processor service (docker compose up -d --no-build --no-deps processor) and check its logs.",
      facts: [],
    };
  }
  const { codex, poll } = processor;
  const name = aiName(processor);
  const claude = processor.provider === "claude";
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
      label: `${name} sign-in`,
      value:
        codex.authenticated === null
          ? "not checked yet"
          : codex.authenticated
            ? "signed in"
            : "signed out",
    },
    { label: "Last poll", value: formatTime(poll.lastAt, timeZone) },
    {
      label: `Last ${name} success`,
      value: formatTime(codex.lastSucceededAt, timeZone),
    },
    ...runtimeFacts(processor.runtime, timeZone, name),
    ...ideasFacts(processor.ideas, timeZone),
    ...categoriesFacts(processor.categories, timeZone),
    ...budgetPlansFacts(processor.budgetPlans, timeZone),
    ...usageFacts(processor.usage, timeZone, name),
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
        summary: `${name} is not signed in.`,
        detail: codex.lastError,
        action: claude
          ? "Create a new token with `claude setup-token` (or use a valid Anthropic API key) and run ./setup.sh claude, or set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN (or LIFE_OS_ANTHROPIC_API_KEY) in .env, then recreate the processor so it picks it up."
          : "Sign in with `codex login` on the host as the Life OS user, then restart the processor so it picks up the new session.",
        facts,
      };
    case "usage_limited":
      return {
        ...base,
        state: "warning",
        summary: `${name} has reached its usage limit.`,
        detail: codex.lastError,
        action: `Captures wait in Inbox and are processed automatically once the limit resets${
          limitResetsAt(processor.usage)
            ? ` (${name} reports ${formatTime(limitResetsAt(processor.usage), timeZone)})`
            : ""
        }. ${
          claude
            ? "To resume sooner, switch LIFE_OS_CLAUDE_MODEL."
            : "To resume sooner, add Codex credits or switch LIFE_OS_CODEX_MODEL."
        }`,
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
        summary: `${name} runs are failing.`,
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
        summary: `Waiting for work; ${name} has not run since the processor started.`,
        facts,
      };
  }
}

function retryReasonLabel(reason: string, name: string) {
  if (reason === "usage_limited") return `after the ${name} usage limit resets`;
  if (reason === "backoff") return "backoff";
  return reason;
}

function inboxCheck(
  inbox: InboxBacklog | null,
  now: Date,
  timeZone: string,
  name: string,
  /** Whether AI processing is on: off, captures stay as written. */
  processing = true,
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
        ? `${formatTime(inbox.nextRetryAt, timeZone)}${inbox.nextRetryReason ? ` (${retryReasonLabel(inbox.nextRetryReason, name)})` : ""}`
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
  if (!processing) {
    return {
      ...base,
      state: "ok",
      summary:
        inbox.pendingCount === 0
          ? "Nothing waiting."
          : `${inbox.pendingCount} capture${inbox.pendingCount === 1 ? " is" : "s are"} kept in Inbox as written: AI processing is off.`,
      facts,
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
  // Life OS events and edits of Google events come from Calendar's change
  // feed; edits are written to Google a few seconds after they are made.
  const changes = connection.changes;
  if (changes) {
    facts.push({
      label: "Edits to Google",
      value: changes.failedEdits
        ? `${changes.failedEdits} failing, retried automatically`
        : changes.pendingEdits
          ? `${changes.pendingEdits} on the way`
          : "up to date",
    });
  }
  if (changesUnreadable(connection)) {
    return {
      ...base,
      state: "ok",
      summary: `Connected as ${account}. Calendar's changes could not be read, so Life OS events and edits made here wait to reach Google; retried automatically.`,
      detail: changes?.lastError ?? undefined,
      facts,
    };
  }
  if (changes?.failedEdits) {
    return {
      ...base,
      state: "ok",
      summary: `Connected as ${account}. ${changes.failedEdits} edit${changes.failedEdits === 1 ? "" : "s"} made in Life OS could not be written to Google yet; retried automatically.`,
      detail: changes.lastEditError ?? undefined,
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

/**
 * Adds a part to a check. A part in worse shape than the check (a warning
 * in an OK or Not set up check) gives the check its state and summary, with
 * `action`; otherwise the check's own stay.
 */
function withPart(
  check: StatusCheck,
  part: StatusPart | undefined,
  action: string,
): StatusCheck {
  if (!part) return check;
  const parts = [...(check.parts ?? []), part];
  if (SEVERITY[part.state] <= SEVERITY[check.state]) {
    return { ...check, parts };
  }
  return { ...check, state: part.state, summary: part.summary, action, parts };
}

/** Adds the Google part to the Calendar check; trouble with it needs action. */
function withGoogle(
  check: StatusCheck,
  connection: GoogleConnection | null | undefined,
  timeZone: string,
  calendars?: GoogleCalendar[] | null,
): StatusCheck {
  if (!connection) return check;
  return withPart(
    check,
    googlePart(connection, timeZone, calendars),
    !connection.configured
      ? "Add the LIFE_OS_GOOGLE_* settings to the server's .env again (google-calendar), or disconnect Google in Calendar."
      : connection.status === "connected"
        ? GOOGLE_SYNC_RETRY
        : GOOGLE_RECONNECT,
  );
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

/** What reminder senders report alike: Calendar's, Tasks', and Food's. */
type SenderHealth = Pick<
  ReminderDelivery,
  | "enabled"
  | "intervalSeconds"
  | "graceMinutes"
  | "lastRunAt"
  | "lastSuccessAt"
  | "lastError"
  | "lastErrorAt"
  | "lastSentAt"
>;

/**
 * A sender checks for due reminders every `intervalSeconds`. Once its last
 * check or its last successful one is older than three intervals and a
 * minute, it has stopped or keeps failing.
 */
export function senderStaleAfterMs(intervalSeconds: number) {
  return (3 * intervalSeconds + 60) * 1000;
}

/** For example "2 hours" or "90 minutes". */
function minutesLabel(minutes: number) {
  if (minutes >= 60 && minutes % 60 === 0) {
    const hours = minutes / 60;
    return `${hours} hour${hours === 1 ? "" : "s"}`;
  }
  return `${minutes} minute${minutes === 1 ? "" : "s"}`;
}

/** What to do while a sender's reminders are not going out. */
export function senderAction(
  service: "calendar" | "tasks" | "food",
  what: string,
  graceMinutes: number,
) {
  return `Check the ${service} service logs (docker compose logs ${service}). ${what} due meanwhile are sent when it catches up, up to ${minutesLabel(graceMinutes)} late; older ones are recorded in Activity as missed.`;
}

/**
 * One reminder sender's health, from its last check for due reminders:
 * Not set up while it is off, a warning while its last check or success
 * is stale or its last check failed.
 */
function senderPart(
  sender: { id: string; title: string; off: string },
  health: SenderHealth,
  now: Date,
  timeZone: string,
  extra: StatusFact[] = [],
): StatusPart {
  const base = { id: sender.id, title: sender.title };
  if (!health.enabled) {
    return { ...base, state: "neutral", summary: sender.off, facts: [] };
  }
  const facts: StatusFact[] = [
    { label: "Last check", value: formatTime(health.lastRunAt, timeZone) },
    { label: "Last sent", value: formatTime(health.lastSentAt, timeZone) },
    { label: "Check interval", value: `${health.intervalSeconds} s` },
    ...extra,
  ];
  const limit = senderStaleAfterMs(health.intervalSeconds);
  const stale = (value: string | null) =>
    !value || now.getTime() - Date.parse(value) > limit;
  // The last error is kept until the next one: it is current while it is
  // later than the last success.
  const failing = Boolean(
    health.lastError &&
    (!health.lastSuccessAt ||
      (health.lastErrorAt &&
        Date.parse(health.lastErrorAt) > Date.parse(health.lastSuccessAt))),
  );
  const warning = (summary: string): StatusPart => ({
    ...base,
    state: "warning",
    summary,
    detail: failing ? (health.lastError ?? undefined) : undefined,
    facts: [
      ...facts,
      {
        label: "Last success",
        value: formatTime(health.lastSuccessAt, timeZone),
      },
    ],
  });
  if (stale(health.lastRunAt)) {
    return warning(
      `${sender.title} are not being sent: ${
        health.lastRunAt
          ? `the last check ran at ${formatTime(health.lastRunAt, timeZone)}`
          : "no check has run yet"
      }.`,
    );
  }
  if (failing) {
    return warning(`${sender.title} may be late: the last check failed.`);
  }
  if (stale(health.lastSuccessAt)) {
    return warning(
      `${sender.title} are not being sent: ${
        health.lastSuccessAt
          ? `the last successful check ran at ${formatTime(health.lastSuccessAt, timeZone)}`
          : "no check has succeeded yet"
      }.`,
    );
  }
  return {
    ...base,
    state: "ok",
    summary: health.lastSentAt
      ? "Sending normally."
      : "Running; nothing sent yet.",
    facts,
  };
}

/** Calendar's reminder delivery, a part of the Calendar check. */
export function eventRemindersPart(
  delivery: ReminderDelivery,
  now: Date,
  timeZone: string,
): StatusPart {
  return senderPart(
    {
      id: "event-reminders",
      title: "Event reminders",
      off: "Calendar sends no reminders: its NOTIFICATIONS_SERVICE_URL is not set.",
    },
    delivery,
    now,
    timeZone,
  );
}

/** When Tasks' daily notifications are due, on the owner's wall clock. */
function taskSchedule(status: TaskNotifications) {
  const morning = status.mergedMorning
    ? `summary with the morning list ${status.summaryTime}`
    : `summary ${status.summaryTime}, morning list ${status.morningTime}`;
  return `${morning}, evening list ${status.eveningTime} (${status.timeZone})`;
}

/**
 * Tasks' reminders before timed Tasks and its daily lists and summary, a
 * part of the Notifications check.
 */
export function taskRemindersPart(
  status: TaskNotifications,
  now: Date,
  timeZone: string,
): StatusPart {
  const extra: StatusFact[] = [
    { label: "Schedule", value: taskSchedule(status) },
    {
      label: "Timed Tasks",
      value: `reminded ${status.timedLeadMinutes} min before they are due`,
    },
  ];
  if (!status.calendarConfigured) {
    extra.push({
      label: "Events in the summary",
      value: "none: CALENDAR_SERVICE_URL is not set",
    });
  }
  return senderPart(
    {
      id: "task-reminders",
      title: "Task reminders",
      off: "Tasks sends no reminders or daily lists: its NOTIFICATIONS_SERVICE_URL is not set.",
    },
    status,
    now,
    timeZone,
    extra,
  );
}

/** Food's daily expiry reminder, a part of the Notifications check. */
export function foodRemindersPart(
  status: FoodNotifications,
  now: Date,
  timeZone: string,
): StatusPart {
  return senderPart(
    {
      id: "food-reminders",
      title: "Food reminders",
      off: "Food sends no expiry reminders: its NOTIFICATIONS_SERVICE_URL is not set.",
    },
    status,
    now,
    timeZone,
    [
      {
        label: "Expiry reminder",
        value: `daily at ${status.expiryTime} (${status.timeZone})`,
      },
    ],
  );
}

/** Adds Calendar's reminder delivery to the Calendar check. */
function withEventReminders(
  check: StatusCheck,
  delivery: ReminderDelivery | null | undefined,
  now: Date,
  timeZone: string,
): StatusCheck {
  if (!delivery) return check;
  return withPart(
    check,
    eventRemindersPart(delivery, now, timeZone),
    senderAction("calendar", "Reminders", delivery.graceMinutes),
  );
}

/** Adds Tasks' notification delivery to the Notifications check. */
function withTaskReminders(
  check: StatusCheck,
  status: TaskNotifications | null | undefined,
  now: Date,
  timeZone: string,
): StatusCheck {
  if (!status) return check;
  return withPart(
    check,
    taskRemindersPart(status, now, timeZone),
    senderAction("tasks", "Reminders and daily lists", status.graceMinutes),
  );
}

/** Adds Food's expiry reminder to the Notifications check. */
function withFoodReminders(
  check: StatusCheck,
  status: FoodNotifications | null | undefined,
  now: Date,
  timeZone: string,
): StatusCheck {
  if (!status) return check;
  return withPart(
    check,
    foodRemindersPart(status, now, timeZone),
    senderAction("food", "Expiry reminders", status.graceMinutes),
  );
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

/** A full backup older than this needs attention… */
export const BACKUP_RECENT_MS = 26 * 60 * 60 * 1000;
/** …and older than this is a problem. */
export const BACKUP_STALE_MS = 50 * 60 * 60 * 1000;
/** Restore checks are owed at least this often once backups exist. */
export const RESTORE_CHECK_DUE_MS = 8 * 24 * 60 * 60 * 1000;
/** Less free space than this where backups are kept needs attention. */
export const BACKUP_MIN_FREE_BYTES = 5_000_000_000;

/** What to do when the backups have stopped. */
export const BACKUP_STALLED =
  "Check the backup service (docker compose logs backup), then use Back up now. See docs/operations/backups.md.";
/** What to do when a restore check failed. */
export const RESTORE_CHECK_FAILED =
  "A backup that does not restore cannot be relied on. See the differences below and docs/operations/backups.md; check again with docker compose exec -T backup /service restore-check.";
/** What to do when restore checks have stopped. */
export const RESTORE_CHECK_OVERDUE =
  "Run one with docker compose exec -T backup /service restore-check, and check that the backup service is running (docs/operations/backups.md).";
/** What to do when the backup disk fills up. */
export const BACKUP_DISK_LOW =
  "Free space on that disk, or keep fewer backups (LIFE_OS_BACKUP_KEEP_*): a backup fails when the disk is full.";

/** For example "1.2 GB" (powers of 1000, as disk sizes are sold). */
export function formatBytes(bytes: number) {
  const units = ["B", "kB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit++;
  }
  return unit === 0
    ? `${value} B`
    : `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

type BackupRun = BackupComponents["schemas"]["BackupRun"];
type RestoreCheck = BackupComponents["schemas"]["RestoreCheck"];

/** The databases of a run that failed, one line each with its error. */
function failedDatabases(run: BackupRun) {
  return run.databases.filter((database) => database.result !== "succeeded");
}

/** A failed or interrupted run, in words, with what to do. */
function runFailure(run: BackupRun) {
  if (run.result === "interrupted") {
    return {
      summary:
        "The last backup was interrupted: the backup service stopped during it.",
      detail: run.error,
      action: "Nothing was lost; use Back up now, or wait for the next backup.",
    };
  }
  const failed = failedDatabases(run);
  const names = failed.map((database) => database.name);
  // Every database was dumped, but the Inbox images could not be copied;
  // the error says what to do (such as the media files' group).
  if (failed.length === 0 && run.media?.error) {
    return {
      summary: "The last backup of the Inbox images failed.",
      detail: run.media.error,
      action: `${run.media.error}. Then use Back up now.`,
    };
  }
  return {
    summary:
      failed.length === 0
        ? `The last backup failed: ${run.error ?? "no reason recorded"}.`
        : failed.length === 1
          ? `The last backup of ${names[0]} failed.`
          : `The last backup of ${failed.length} databases failed: ${names.join(", ")}.`,
    detail:
      failed
        .map((database) => `${database.name}: ${database.error ?? "failed"}`)
        .join("\n") ||
      run.error ||
      undefined,
    // The service's error says what to do ("lifeos_backup cannot connect to
    // food_db: run make db-provision-backup"); the monitor sends the action.
    action: failed[0]?.error
      ? `${failed[0].error}${failed.length > 1 ? ` (and ${failed.length - 1} more below)` : ""}. Then use Back up now.`
      : "See the error below and docs/operations/backups.md, then use Back up now.",
  };
}

/** A failed restore check, in words, with each database's differences. */
function checkFailure(check: RestoreCheck) {
  const failed = check.databases.filter((database) => !database.ok);
  const names = failed.map((database) => database.name);
  return {
    summary: failed.length
      ? `The last restore check failed: ${names.join(", ")} did not restore as ${failed.length === 1 ? "its" : "their"} backup says.`
      : `The last restore check failed: ${check.error ?? "no reason recorded"}.`,
    detail:
      failed
        .map((database) =>
          database.error
            ? `${database.name}: ${database.error}`
            : `${database.name}: ${database.mismatches.map((mismatch) => `${mismatch.subject}: expected ${mismatch.expected}, restored ${mismatch.actual}`).join("; ")}`,
        )
        .join("\n") ||
      check.error ||
      undefined,
  };
}

function restoreCheckLabel(status: BackupStatus, timeZone: string) {
  const check = status.lastRestoreCheck;
  if (check) {
    return `${check.ok ? "passed" : "failed"}, ${formatTime(check.finishedAt, timeZone)} (backup of ${formatTime(runStart(check.runId), timeZone)})`;
  }
  if (status.schedule.restoreCheckDay === "off") return "off";
  return status.schedule.nextRestoreCheckAt
    ? `not yet; after the backup of ${formatTime(status.schedule.nextRestoreCheckAt, timeZone)}`
    : "not yet";
}

/** A run id (20261011T011500Z) as the instant it names. */
function runStart(id: string) {
  return `${id.slice(0, 4)}-${id.slice(4, 6)}-${id.slice(6, 8)}T${id.slice(9, 11)}:${id.slice(11, 13)}:${id.slice(13, 15)}Z`;
}

/** The Inbox images the last run could not copy, as a fact's tail. */
function mediaMissing(run: BackupRun | undefined) {
  const missing = run?.media?.missing ?? 0;
  return missing > 0
    ? `; ${missing} the last backup could not copy (not on the Inbox volume, or damaged there)`
    : "";
}

function backupFacts(
  status: BackupStatus,
  now: Date,
  timeZone: string,
): StatusFact[] {
  const facts: StatusFact[] = [];
  const last = status.lastSuccess;
  if (status.running) {
    const { completed, databases, current } = status.running;
    facts.push({
      label: "Backing up now",
      value: `${current ?? "starting"} (${completed} of ${databases.length} done)`,
    });
  }
  if (status.runningRestoreCheck) {
    const { completed, databases, current } = status.runningRestoreCheck;
    facts.push({
      label: "Restore check now",
      value: `${current ?? "starting"} (${completed} of ${databases.length} done)`,
    });
  }
  facts.push({
    label: "Last backup",
    value: last
      ? `${formatTime(last.startedAt, timeZone)} (${formatAge(now.getTime() - Date.parse(last.startedAt))} ago)`
      : "none yet",
  });
  if (last) {
    const backedUp = new Set(last.databases.map((database) => database.name));
    const missing = status.databases.filter((name) => !backedUp.has(name));
    facts.push({
      label: "Databases",
      value: missing.length
        ? `${last.databases.length} of ${status.databases.length} (not in it yet: ${missing.join(", ")})`
        : String(last.databases.length),
    });
  }
  facts.push(
    {
      label: "Total size",
      value: `${formatBytes(status.storage.totalBytes)} in ${status.storage.runs} ${status.storage.runs === 1 ? "backup" : "backups"}`,
    },
    {
      label: "Inbox images",
      value: `${status.storage.mediaFiles} ${status.storage.mediaFiles === 1 ? "file" : "files"}, ${formatBytes(status.storage.mediaBytes)}${mediaMissing(status.lastRun)}`,
    },
    {
      label: "Next backup",
      value: `${formatTime(status.schedule.nextRunAt, timeZone)}${status.schedule.nextTrigger === "catch-up" ? " (catching up)" : ""}`,
    },
    { label: "Last restore check", value: restoreCheckLabel(status, timeZone) },
    {
      label: "Retention",
      value: `${status.retention.daily} daily, ${status.retention.weekly} weekly, ${status.retention.monthly} monthly`,
    },
    {
      label: "Free disk",
      value: `${formatBytes(status.storage.freeBytes)} of ${formatBytes(status.storage.sizeBytes)}`,
    },
  );
  return facts;
}

type Issue = {
  state: CheckState;
  summary: string;
  detail?: string;
  action: string;
};

/**
 * The Backups check. Left out (null) when the status cannot be read while
 * Services already reports the backup service not ready, so one outage is
 * one alert, as for the reminder parts.
 */
export function backupsCheck(
  status: BackupStatus | null,
  services: RuntimeSignals["services"],
  now: Date,
  timeZone: string,
): StatusCheck | null {
  const base = { id: "backups" as const, title: "Backups" };
  if (status === null) {
    const down = services.some(
      (service) => service.name === "Backup" && !service.ready,
    );
    return down
      ? null
      : {
          ...base,
          state: "problem",
          summary: "The backup status cannot be read.",
          action: "Check the backup service logs (docker compose logs backup).",
          facts: [],
        };
  }
  const control = {
    kind: "backup-now" as const,
    running: Boolean(status.running || status.runningRestoreCheck),
  };
  const facts = backupFacts(status, now, timeZone);
  const last = status.lastSuccess;
  const lastRun = status.lastRun;
  // The latest run, when it failed after the last success.
  const failedRun =
    lastRun &&
    lastRun.result !== "succeeded" &&
    (!last || Date.parse(lastRun.startedAt) > Date.parse(last.startedAt))
      ? lastRun
      : undefined;

  if (!last) {
    if (failedRun) {
      const failure = runFailure(failedRun);
      return {
        ...base,
        state: "warning",
        summary: `No backup has succeeded yet. ${failure.summary}`,
        detail: failure.detail,
        action: failure.action,
        facts,
        control,
      };
    }
    return {
      ...base,
      state: "neutral",
      summary: status.running
        ? "The first backup is running now."
        : `No backup yet. The first one runs at ${formatTime(status.schedule.nextRunAt, timeZone)}.`,
      facts,
      control,
    };
  }

  const age = now.getTime() - Date.parse(last.startedAt);
  const check = status.lastRestoreCheck;
  const issues: Issue[] = [];
  const failure = failedRun ? runFailure(failedRun) : undefined;
  if (age > BACKUP_RECENT_MS) {
    issues.push({
      state: age > BACKUP_STALE_MS ? "problem" : "warning",
      summary: `The last backup is ${formatAge(age)} old.`,
      detail: failure?.detail,
      action: failure?.action ?? BACKUP_STALLED,
    });
  }
  if (check && !check.ok) {
    const { summary, detail } = checkFailure(check);
    issues.push({
      state: "problem",
      summary,
      detail,
      action: RESTORE_CHECK_FAILED,
    });
  }
  if (failure && age <= BACKUP_RECENT_MS) {
    issues.push({ state: "warning", ...failure });
  }
  if (status.storage.freeBytes < BACKUP_MIN_FREE_BYTES) {
    issues.push({
      state: "warning",
      summary: `Only ${formatBytes(status.storage.freeBytes)} is free where backups are kept.`,
      action: BACKUP_DISK_LOW,
    });
  }
  if (status.schedule.restoreCheckDay !== "off") {
    const since = check
      ? Date.parse(check.startedAt)
      : status.oldestSuccessAt
        ? Date.parse(status.oldestSuccessAt)
        : now.getTime();
    if (now.getTime() - since > RESTORE_CHECK_DUE_MS) {
      issues.push({
        state: "warning",
        summary: check
          ? `No restore check for ${formatAge(now.getTime() - since)}.`
          : `No restore check has run in the ${formatAge(now.getTime() - since)} since backups began.`,
        action: RESTORE_CHECK_OVERDUE,
      });
    }
  }
  if (issues.length === 0) {
    return {
      ...base,
      state: "ok",
      summary: `Last backup ${formatTime(last.startedAt, timeZone)} (${formatAge(age)} ago).`,
      facts,
      control,
    };
  }
  const state = worst(issues.map((issue) => issue.state));
  const [first, ...others] = [
    ...issues.filter((issue) => issue.state === state),
    ...issues.filter((issue) => issue.state !== state),
  ];
  return {
    ...base,
    state,
    summary: first.summary,
    detail: first.detail,
    action: first.action,
    facts: others.length
      ? [
          {
            label: "Also",
            value: others.map((issue) => issue.summary).join(" "),
          },
          ...facts,
        ]
      : facts,
    control,
  };
}

export { formatBinaryBytes };

/** What to do when memory runs low. */
export const SERVER_MEMORY_ACTION =
  "Stop heavy jobs (on this server, one at a time); docker stats shows which containers use the most.";
/** What to do when a disk fills up. */
export const SERVER_DISK_ACTION =
  "Free space on it: old images and build cache (docker system df shows them), logs, or old backups. A full disk stops PostgreSQL.";
/** What to do when the server is busy. */
export const SERVER_LOAD_ACTION =
  "docker stats shows which containers use the CPU; heavy jobs should run one at a time.";
/** What to do when container facts cannot be read. */
export const SERVER_CONTAINERS_ACTION =
  "Check the docker gateway (docker compose logs host-health-docker): LIFE_OS_DOCKER_GID must be the Docker socket's group.";

type HostFinding = HostHealthComponents["schemas"]["HostFinding"];

/** A container's name, with its compose project when that says more. */
function containerLabel(finding: HostFinding) {
  const name = finding.subject ?? "A container";
  return finding.project && !name.startsWith(finding.project)
    ? `${name} (${finding.project})`
    : name;
}

/** A finding in words, with what to do. */
function describeFinding(finding: HostFinding, status: HostStatus) {
  const available = formatBinaryBytes(finding.availableBytes ?? 0);
  const total = formatBinaryBytes(finding.totalBytes ?? 0);
  switch (finding.kind) {
    case "memory_low": {
      const biggest = status.containers.top
        .slice(0, 3)
        .map((user) => `${user.name} ${formatBinaryBytes(user.memoryBytes)}`)
        .join(", ");
      return {
        summary: `Memory is ${finding.severity === "problem" ? "very low" : "low"}: ${available} available of ${total}.${biggest ? ` Biggest: ${biggest}.` : ""}`,
        action: SERVER_MEMORY_ACTION,
      };
    }
    case "disk_low":
      return {
        summary: `The ${finding.subject ?? ""} disk is ${finding.severity === "problem" ? "almost full" : "filling up"}: ${available} free of ${total}.`,
        action: SERVER_DISK_ACTION,
      };
    case "load_high":
      return {
        summary: `The server is busy: its 15-minute load is ${(finding.load ?? 0).toFixed(1)} on ${finding.cpus ?? 0} CPUs.`,
        action: SERVER_LOAD_ACTION,
      };
    case "container_restarting":
      return {
        summary: `${containerLabel(finding)} keeps restarting: ${finding.count ?? 0} times in ${finding.minutes ?? 0} min${finding.exitCode !== undefined ? `, last exit code ${finding.exitCode}` : ""}.`,
        action: `See why with docker logs ${finding.subject ?? ""}.`,
      };
    case "container_unhealthy":
      return {
        summary: `${containerLabel(finding)} has been unhealthy for ${formatAge((finding.minutes ?? 0) * 60_000)}.`,
        action: `See its health check with docker inspect --format '{{json .State.Health}}' ${finding.subject ?? ""}.`,
      };
    case "containers_unavailable":
      return {
        summary: `The containers cannot be read: ${finding.error ?? "no reason given"}.`,
        action: SERVER_CONTAINERS_ACTION,
      };
  }
}

function serverFacts(status: HostStatus): StatusFact[] {
  const { memory, load, containers } = status;
  const facts: StatusFact[] = [
    {
      label: "Memory",
      value: `${formatBinaryBytes(memory.availableBytes)} available of ${formatBinaryBytes(memory.totalBytes)} (${Math.round(memory.availablePercent)}%)`,
    },
    {
      label: "Load",
      value: `${load.one.toFixed(2)}, ${load.five.toFixed(2)}, ${load.fifteen.toFixed(2)} (1, 5, 15 min) on ${load.cpus} CPUs`,
    },
    ...lifeOsFacts(status),
    ...status.disks.map((disk) => ({
      label: `Disk ${disk.label}`,
      value: `${formatBinaryBytes(disk.freeBytes)} free of ${formatBinaryBytes(disk.totalBytes)} (${Math.round(disk.freePercent)}%)`,
    })),
    {
      label: "Containers",
      value: containers.available
        ? `${containers.running} running, ${containers.unhealthy} unhealthy, ${containers.restarting} restarting (${containers.total} in all${containers.testStack ? `, ${containers.testStack} of the test stack` : ""})`
        : "not available",
    },
  ];
  if (containers.top.length) {
    facts.push({
      label: "Most memory",
      value: containers.top
        .slice(0, 3)
        .map((user) => `${user.name} ${formatBinaryBytes(user.memoryBytes)}`)
        .join(", "),
    });
  }
  facts.push({
    label: "Up for",
    value: formatAge(status.uptimeSeconds * 1000),
  });
  if (status.mode === "fixture") {
    facts.push({
      label: "Source",
      value: "a made-up server (HOST_HEALTH_FIXTURE)",
    });
  }
  return facts;
}

/**
 * How much of the server Life OS uses, its services using the most memory,
 * the test stack while it runs, and everything else. Facts, not warnings:
 * none of them is a finding. Left out when host-health does not report it.
 */
function lifeOsFacts({ usage }: HostStatus): StatusFact[] {
  if (!usage) return [];
  if (!usage.available || !usage.lifeOs)
    return [
      {
        label: "Life OS",
        value: `CPU and RAM not available${usage.error ? `: ${usage.error}` : ""}`,
      },
    ];
  const facts: StatusFact[] = [
    { label: "Life OS", value: formatUsage(usage.lifeOs) },
  ];
  if (usage.services.length) {
    facts.push({
      label: "Life OS services",
      value: usage.services
        .map(
          (service) =>
            `${service.service}${service.containers > 1 ? ` ×${service.containers}` : ""} ${formatBinaryBytes(service.memoryBytes)}`,
        )
        .join(", "),
    });
  }
  if (usage.tests) {
    facts.push({ label: "Tests running", value: formatUsage(usage.tests) });
  }
  if (usage.others) {
    facts.push({ label: "Everything else", value: formatUsage(usage.others) });
  }
  return facts;
}

/**
 * Life OS's memory and CPU over the history, each at most 48 points that
 * keep their span's peak, on the scale of the whole server.
 */
function lifeOsCharts(
  status: HostStatus,
  history: HostHistory | null,
): StatusChart[] {
  const samples = history?.samples ?? [];
  // Samples without a value (not read, or CPU not known yet) are skipped.
  const memory = samples.flatMap(({ at, lifeOsMemoryBytes: value }) =>
    value === undefined ? [] : [{ at, value }],
  );
  const cpu = samples.flatMap(({ at, lifeOsCpuCores: value }) =>
    value === undefined ? [] : [{ at, value }],
  );
  const charts: StatusChart[] = [];
  if (memory.length >= 2) {
    charts.push({
      label: "Life OS memory, last 24 h",
      max: status.memory.totalBytes,
      points: downsampleMax(memory, 48),
      worst: "highest",
    });
  }
  if (cpu.length >= 2) {
    charts.push({
      label: "Life OS CPU, last 24 h",
      max: status.load.cpus,
      points: downsampleMax(cpu, 48),
      unit: "cores",
      worst: "highest",
    });
  }
  return charts;
}

/** Memory available over the history, at most 96 points. */
function memoryChart(
  status: HostStatus,
  history: HostHistory | null,
): StatusChart | undefined {
  const samples = (history?.samples ?? []).map((sample) => ({
    at: sample.at,
    value: sample.memoryAvailableBytes,
  }));
  if (samples.length < 2) return undefined;
  return {
    label: "Memory available, last 24 h",
    max: status.memory.totalBytes,
    threshold:
      (status.memory.totalBytes * status.thresholds.memoryWarningPercent) / 100,
    points: downsampleMin(samples, 96),
  };
}

const CLOCK_OPTIONS: Intl.DateTimeFormatOptions = {
  hour: "2-digit",
  minute: "2-digit",
  timeZoneName: "short",
};

/**
 * Whether host-health's restart counts can still be too low, said for the
 * card: after host-health starts it counts restarts only from then on (and
 * whatever Docker still held, about two minutes), so until it has watched
 * the whole window, no restart-loop finding does not mean there is none.
 * Undefined once they are complete, or without container facts.
 */
function restartWatch(status: HostStatus, timeZone: string) {
  const { containers, thresholds } = status;
  // An older host-health does not say: complete, as it then assumed.
  if (!containers.available || containers.restartsComplete !== false)
    return undefined;
  if (!containers.restartsWatchedSince)
    return "Not watching for restart loops yet: the container events are not followed.";
  const since = Date.parse(containers.restartsWatchedSince);
  const clock = dateTimeFormat(timeZone, CLOCK_OPTIONS);
  return `Watching for restart loops since ${clock.format(since)}; the ${thresholds.restartLoopMinutes}-min count is complete at ${clock.format(since + thresholds.restartLoopMinutes * 60_000)}.`;
}

/**
 * The Server check: host-health's findings, worst first. Left out (null)
 * when host-health cannot be read while Services already reports it not
 * ready, so one outage is one alert. Without findings while the restart
 * counts are not complete, it is ok but provisional: a restart loop that
 * host-health has not seen again yet must not read as recovered.
 */
export function serverCheck(
  server: { status: HostStatus; history: HostHistory | null } | null,
  services: RuntimeSignals["services"],
  timeZone: string = DEFAULT_TIME_ZONE,
): StatusCheck | null {
  const base = { id: "server" as const, title: "Server" };
  if (server === null) {
    const down = services.some(
      (service) => service.name === "Host health" && !service.ready,
    );
    return down
      ? null
      : {
          ...base,
          state: "problem",
          summary: "The server's health cannot be read.",
          action:
            "Check the host-health service logs (docker compose logs host-health).",
          facts: [],
        };
  }
  const { status, history } = server;
  const facts = serverFacts(status);
  const chart = memoryChart(status, history);
  const charts = lifeOsCharts(status, history);
  const sideCharts = charts.length ? charts : undefined;
  const [first, ...others] = status.findings;
  if (!first) {
    const watching = restartWatch(status, timeZone);
    return {
      ...base,
      state: "ok",
      ...(watching ? { provisional: true } : {}),
      summary: `Running normally: ${formatBinaryBytes(status.memory.availableBytes)} of memory available, a 15-minute load of ${status.load.fifteen.toFixed(1)} on ${status.load.cpus} CPUs.${watching ? ` ${watching}` : ""}`,
      facts,
      chart,
      sideCharts,
    };
  }
  const { summary, action } = describeFinding(first, status);
  return {
    ...base,
    state: first.severity,
    summary,
    action,
    facts: others.length
      ? [
          {
            label: "Also",
            value: others
              .map((finding) => describeFinding(finding, status).summary)
              .join(" "),
          },
          ...facts,
        ]
      : facts,
    chart,
    sideCharts,
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
    ...(signals.processor === undefined
      ? []
      : [processingCheck(signals.processor, timeZone, configured)]),
    inboxCheck(
      signals.inbox,
      now,
      timeZone,
      aiName(signals.processor),
      signals.processor !== undefined,
    ),
    ...(signals.voice === undefined
      ? []
      : [voiceCheck(signals.voice, now, timeZone)]),
    ...(signals.calendarFeeds === undefined
      ? []
      : [
          withEventReminders(
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
            signals.eventReminders,
            now,
            timeZone,
          ),
        ]),
    ...(signals.notificationChannels === undefined
      ? []
      : [
          withFoodReminders(
            withTaskReminders(
              notificationsCheck(signals.notificationChannels, timeZone),
              signals.taskReminders,
              now,
              timeZone,
            ),
            signals.foodReminders,
            now,
            timeZone,
          ),
        ]),
    ...(signals.bankConnections === undefined
      ? []
      : [bankingCheck(signals.bankConnections, now, timeZone)]),
    ...(signals.backups === undefined
      ? []
      : [backupsCheck(signals.backups, signals.services, now, timeZone)]),
    ...(signals.server === undefined
      ? []
      : [serverCheck(signals.server, signals.services, timeZone)]),
    servicesCheck(signals.services),
  ].filter((check): check is StatusCheck => check !== null);
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

export const processorStatusUrl = normalizeServiceUrl(
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

/** The services Status lists under Services, by their Dashboard key. */
const SERVICE_NAMES: Partial<Record<DashboardService, string>> = {
  tasks: "Tasks",
  inbox: "Inbox",
  calendar: "Calendar",
  finance: "Finance",
  voice: "Voice",
  activity: "Activity",
  notifications: "Notifications",
  banking: "Banking",
  calendarFeeds: "Calendar feeds",
  googleCalendar: "Google Calendar",
  food: "Food",
  backup: "Backup",
  hostHealth: "Host health",
  recipeImport: "Recipe import",
  monitor: "Monitor",
};

/** Where a service's readiness is asked (its base URL; GET /ready). */
function serviceUrl(service: DashboardService): string | undefined {
  if (service === "monitor") return monitorUrl;
  if (service === "processor") return undefined;
  return serviceBaseUrls[service];
}

const unread = Promise.resolve(undefined);

/**
 * The status of what runs: only the services and checks of the modules that
 * are on (src/lib/modules.ts), so a module that is off is never probed, and
 * no lookup of a service that is not there holds up the rest (BUG-50).
 */
export async function getRuntimeStatus(
  parts: InstalledParts = installed(),
): Promise<RuntimeStatus> {
  const timeout = { signal: AbortSignal.timeout(PROBE_TIMEOUT_MS) };
  const checkOn = (id: StatusCheck["id"]) =>
    (parts.checks as string[]).includes(id);
  const google = parts.features.includes("calendar.google");
  const notificationChannels = checkOn("notifications")
    ? notificationsClient
        .GET("/v1/channels", timeout)
        .then(({ data }) => data?.items ?? null)
        .catch(() => null)
    : unread;
  const bankConnections = checkOn("banking")
    ? bankingClient
        .GET("/v1/connections", timeout)
        .then(({ data }) => data ?? null)
        .catch(() => null)
    : unread;
  const eventReminders = calendarClient
    .GET("/v1/reminders/status", timeout)
    .then(({ data }) => data ?? null)
    .catch(() => null);
  const taskReminders = tasksClient
    .GET("/v1/notifications/status", timeout)
    .then(({ data }) => data ?? null)
    .catch(() => null);
  const backups = checkOn("backups")
    ? backupClient
        .GET("/v1/status", timeout)
        .then(({ data }) => data ?? null)
        .catch(() => null)
    : unread;
  const server = checkOn("server")
    ? Promise.all([
        hostHealthClient
          .GET("/v1/status", timeout)
          .then(({ data }) => data ?? null)
          .catch(() => null),
        hostHealthClient
          .GET("/v1/history", { ...timeout, params: { query: { hours: 24 } } })
          .then(({ data }) => data ?? null)
          .catch(() => null),
      ]).then(([status, history]) => (status ? { status, history } : null))
    : unread;
  const foodReminders = foodClient
    .GET("/v1/notifications/status", timeout)
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
    checkOn("processing")
      ? getJson<ProcessorStatus>(`${processorStatusUrl}/status`)
      : unread,
    inboxClient
      .GET("/v1/backlog", timeout)
      .then(({ data }) => data ?? null)
      .catch(() => null),
    checkOn("voice")
      ? voiceClient
          .GET("/v1/backlog", timeout)
          .then(({ data }) => data ?? null)
          .catch(() => null)
      : unread,
    checkOn("calendar")
      ? calendarFeedsClient
          .GET("/v1/feeds", timeout)
          .then(({ data }) => data?.items ?? null)
          .catch(() => null)
      : unread,
    calendarClient
      .GET("/v1/sources", timeout)
      .then(({ data }) => data?.items ?? null)
      .catch(() => null),
    google
      ? googleCalendarClient
          .GET("/v1/connection", timeout)
          .then(({ data }) => data ?? null)
          .catch(() => null)
      : unread,
    google
      ? googleCalendarClient
          .GET("/v1/calendars", timeout)
          .then(({ data }) => data?.items ?? null)
          .catch(() => null)
      : unread,
    Promise.all(
      (Object.keys(SERVICE_NAMES) as DashboardService[]).flatMap((service) => {
        const name = SERVICE_NAMES[service];
        const url = serviceUrl(service);
        return name && url && parts.services.includes(service)
          ? [ready(url).then((ok) => ({ name, ready: ok }))]
          : [];
      }),
    ),
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
      eventReminders: await eventReminders,
      taskReminders: await taskReminders,
      foodReminders: await foodReminders,
      bankConnections: await bankConnections,
      backups: await backups,
      server: await server,
      services,
    },
    new Date(),
    await userTimeZone(),
    configuredTimeZone(),
  );
}
