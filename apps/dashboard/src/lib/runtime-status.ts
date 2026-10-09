import { normalizeServiceUrl, serviceBaseUrls } from "@/config/services";
import { inboxClient } from "@/lib/api/inbox-client";
import type { components as InboxComponents } from "@/lib/api/generated/inbox";
import type { components as VoiceComponents } from "@/lib/api/generated/voice";
import { voiceClient } from "@/lib/api/voice-client";

// The Status page turns supported runtime state into actionable checks:
// - the processor's private /status endpoint (what the running process saw),
// - the Inbox and Voice backlog APIs (durable queue state),
// - each owning service's /ready endpoint.
// Nothing here is inferred from UI behaviour.

export type InboxBacklog = InboxComponents["schemas"]["InboxBacklog"];
export type AudioBacklog = VoiceComponents["schemas"]["AudioBacklog"];

/** Mirrors `Status` in services/mcp/internal/processor/status.go. */
export type ProcessorStatus = {
  model: string;
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
};

export type CheckState = "ok" | "warning" | "problem";

export type StatusCheck = {
  id: "processing" | "inbox" | "voice" | "services";
  title: string;
  state: CheckState;
  summary: string;
  /** Raw error text from the owning runtime, shown verbatim. */
  detail?: string;
  /** What the user can do about it. */
  action?: string;
  facts: { label: string; value: string }[];
};

export type RuntimeSignals = {
  processor: ProcessorStatus | null;
  inbox: InboxBacklog | null;
  voice: AudioBacklog | null;
  services: { name: string; ready: boolean }[];
};

export type RuntimeStatus = {
  checkedAt: string;
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

const timeFormat = new Intl.DateTimeFormat("en-GB", {
  dateStyle: "medium",
  timeStyle: "short",
  timeZone: "UTC",
});

export function formatTime(value: string | null | undefined) {
  return value ? `${timeFormat.format(new Date(value))} UTC` : "never";
}

const SEVERITY: Record<CheckState, number> = { ok: 0, warning: 1, problem: 2 };

function worst(states: CheckState[]): CheckState {
  return states.reduce<CheckState>(
    (current, state) => (SEVERITY[state] > SEVERITY[current] ? state : current),
    "ok",
  );
}

function processingCheck(processor: ProcessorStatus | null): StatusCheck {
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
    {
      label: "Codex sign-in",
      value:
        codex.authenticated === null
          ? "not checked yet"
          : codex.authenticated
            ? "signed in"
            : "signed out",
    },
    { label: "Last poll", value: formatTime(poll.lastAt) },
    { label: "Last Codex success", value: formatTime(codex.lastSucceededAt) },
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
        action:
          "Captures wait in Inbox and are processed automatically once the limit resets. To resume sooner, add Codex credits or switch LIFE_OS_CODEX_MODEL.",
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

function inboxCheck(inbox: InboxBacklog | null, now: Date): StatusCheck {
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
  ];
  if (inbox.retryingCount > 0) {
    return {
      ...base,
      state: "warning",
      summary: `${inbox.retryingCount} capture${inbox.retryingCount === 1 ? " has" : "s have"} failed and ${inbox.retryingCount === 1 ? "is" : "are"} being retried.`,
      detail: inbox.lastFailureSummary ?? undefined,
      action:
        "The processor keeps retrying them on each poll. The error above is the latest failure.",
      facts: [
        ...facts,
        { label: "Latest failure", value: formatTime(inbox.lastFailureAt) },
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

function voiceCheck(voice: AudioBacklog | null, now: Date): StatusCheck {
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
      value: voice.nextRetryAt ? formatTime(voice.nextRetryAt) : "none",
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
): RuntimeStatus {
  const checks = [
    processingCheck(signals.processor),
    inboxCheck(signals.inbox, now),
    voiceCheck(signals.voice, now),
    servicesCheck(signals.services),
  ];
  return {
    checkedAt: now.toISOString(),
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
  const [processor, inbox, voice, services] = await Promise.all([
    getJson<ProcessorStatus>(`${processorStatusUrl}/status`),
    inboxClient
      .GET("/v1/backlog", timeout)
      .then(({ data }) => data ?? null)
      .catch(() => null),
    voiceClient
      .GET("/v1/backlog", timeout)
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
  return toRuntimeStatus({ processor, inbox, voice, services }, new Date());
}
