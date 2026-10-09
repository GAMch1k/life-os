import assert from "node:assert/strict";
import test from "node:test";

import {
  formatAge,
  limitResetsAt,
  runtimeFacts,
  statusAlerts,
  toRuntimeStatus,
  usageFacts,
  type AudioBacklog,
  type InboxBacklog,
  type ProcessorStatus,
  type RuntimeSignals,
} from "./runtime-status";

const now = new Date("2026-10-09T18:00:00Z");
const minutesAgo = (minutes: number) =>
  new Date(now.getTime() - minutes * 60_000).toISOString();

const processor = (
  codex: Partial<ProcessorStatus["codex"]> = {},
  poll: Partial<ProcessorStatus["poll"]> = {},
): ProcessorStatus => ({
  model: "gpt-5.6-luna",
  startedAt: minutesAgo(60),
  poll: { lastAt: minutesAgo(0), lastSucceededAt: minutesAgo(0), ...poll },
  codex: {
    state: "ready",
    authenticated: true,
    authCheckedAt: minutesAgo(1),
    lastSucceededAt: minutesAgo(5),
    lastFailedAt: null,
    consecutiveFailures: 0,
    ...codex,
  },
});
const inbox = (overrides: Partial<InboxBacklog> = {}): InboxBacklog => ({
  pendingCount: 0,
  retryingCount: 0,
  humanReviewCount: 0,
  ...overrides,
});
const voice = (overrides: Partial<AudioBacklog> = {}): AudioBacklog => ({
  queuedCount: 0,
  processingCount: 0,
  retryableCount: 0,
  workerStalled: false,
  whisperReady: true,
  ...overrides,
});
const healthy: RuntimeSignals = {
  processor: processor(),
  inbox: inbox(),
  voice: voice(),
  services: [
    { name: "Tasks", ready: true },
    { name: "Inbox", ready: true },
  ],
};
const check = (signals: RuntimeSignals, id: string) =>
  toRuntimeStatus(signals, now).checks.find((entry) => entry.id === id)!;

test("a healthy runtime has no alerts", () => {
  const status = toRuntimeStatus(healthy, now);
  assert.equal(status.overall, "ok");
  assert.deepEqual(statusAlerts(status), []);
});

test("a Codex usage limit is a warning that keeps Codex's own reset text", () => {
  const signals = {
    ...healthy,
    processor: processor({
      state: "usage_limited",
      lastError:
        "run Codex CLI: exit status 1: ERROR: You’ve hit your usage limit. try again at 6:22 PM.",
      consecutiveFailures: 12,
    }),
    inbox: inbox({
      pendingCount: 1,
      retryingCount: 1,
      oldestPendingAt: minutesAgo(3),
      lastFailureAt: minutesAgo(0),
      lastFailureSummary:
        "planning failed: ERROR: You’ve hit your usage limit.",
    }),
  };
  const processing = check(signals, "processing");
  assert.equal(processing.state, "warning");
  assert.match(processing.summary, /usage limit/);
  assert.match(processing.detail!, /try again at 6:22 PM/);
  assert.match(processing.action!, /resets/);
  const backlog = check(signals, "inbox");
  assert.equal(backlog.state, "warning");
  assert.match(backlog.summary, /1 capture has failed and is being retried/);
  const alerts = statusAlerts(toRuntimeStatus(signals, now));
  assert.deepEqual(
    alerts.map((alert) => alert.id),
    ["processing", "inbox"],
  );
});

test("sign-out, a stopped processor, and an MCP outage are problems", () => {
  assert.equal(
    check(
      { ...healthy, processor: processor({ state: "unauthenticated" }) },
      "processing",
    ).state,
    "problem",
  );
  const stopped = check({ ...healthy, processor: null }, "processing");
  assert.equal(stopped.state, "problem");
  assert.match(stopped.summary, /not running/);
  const outage = check(
    {
      ...healthy,
      processor: processor({}, { lastError: "connection refused" }),
    },
    "processing",
  );
  assert.equal(outage.state, "problem");
  assert.equal(outage.detail, "connection refused");
});

test("a capture stuck for longer than the threshold is a prolonged backlog", () => {
  const stuck = check(
    {
      ...healthy,
      inbox: inbox({ pendingCount: 1, oldestPendingAt: minutesAgo(3000) }),
    },
    "inbox",
  );
  assert.equal(stuck.state, "warning");
  assert.match(stuck.summary, /waited 2 days/);
  assert.equal(
    check(
      {
        ...healthy,
        inbox: inbox({ pendingCount: 1, oldestPendingAt: minutesAgo(2) }),
      },
      "inbox",
    ).state,
    "ok",
  );
});

test("Voice reports Whisper, worker stalls, and scheduled retries", () => {
  assert.match(
    check({ ...healthy, voice: voice({ whisperReady: false }) }, "voice")
      .summary,
    /Whisper/,
  );
  assert.match(
    check({ ...healthy, voice: voice({ workerStalled: true }) }, "voice")
      .summary,
    /not picking up/,
  );
  const retry = check(
    {
      ...healthy,
      voice: voice({
        retryableCount: 2,
        nextRetryAt: "2026-10-09T18:05:00Z",
        lastFailureSummary: "whisper returned 503",
      }),
    },
    "voice",
  );
  assert.equal(retry.state, "warning");
  assert.equal(retry.detail, "whisper returned 503");
  assert.ok(
    retry.facts.some(
      (fact) => fact.label === "Next retry" && fact.value.includes("18:05"),
    ),
  );
});

test("unreachable services are named", () => {
  const services = check(
    {
      ...healthy,
      services: [
        { name: "Tasks", ready: false },
        { name: "Calendar", ready: true },
      ],
    },
    "services",
  );
  assert.equal(services.state, "problem");
  assert.equal(services.summary, "Tasks is not ready.");
  assert.equal(check({ ...healthy, inbox: null }, "inbox").state, "problem");
  assert.equal(check({ ...healthy, voice: null }, "voice").state, "problem");
});

test("ages read naturally", () => {
  assert.equal(formatAge(30_000), "less than a minute");
  assert.equal(formatAge(16 * 60_000), "16 min");
  assert.equal(formatAge(125 * 60_000), "2 h 5 min");
  assert.equal(formatAge(3 * 24 * 60 * 60_000), "3 days");
});

const usage = {
  current: {
    checkedAt: "2026-10-09T19:55:00Z",
    authMode: "chatgpt",
    planType: "plus",
    resetCreditsAvailable: 2,
    limits: [
      {
        id: "codex",
        windows: [
          {
            usedPercent: 100,
            windowMinutes: 300,
            resetsAt: "2026-10-09T18:22:00Z",
          },
          {
            usedPercent: 31,
            windowMinutes: 10080,
            resetsAt: "2026-10-16T08:22:53Z",
          },
        ],
        reached: "rate_limit_reached",
      },
      {
        id: "base_model_inference",
        name: "gpt-reserve",
        model: "gpt-5.6-luna",
        windows: [
          {
            usedPercent: 24,
            windowMinutes: 10080,
            resetsAt: "2026-10-12T19:25:52Z",
          },
        ],
      },
    ],
  },
};

test("usage is shown per bucket exactly as Codex reported it", () => {
  const facts = usageFacts(usage);
  const value = (label: string) =>
    facts.find((fact) => fact.label === label)?.value;
  assert.equal(value("Codex plan"), "plus · ChatGPT sign-in");
  assert.equal(
    value("Codex 5-hour usage"),
    "100% used, resets 9 Oct 2026, 18:22 UTC (limit reached)",
  );
  assert.match(value("Codex weekly usage")!, /^31% used, resets 16 Oct 2026/);
  assert.match(value("gpt-reserve weekly usage")!, /^24% used/);
  assert.equal(value("Free usage resets available"), "2");
  assert.deepEqual(usageFacts(undefined), [
    { label: "Codex usage", value: "not read yet" },
  ]);
  assert.deepEqual(usageFacts({ current: null, error: "app-server closed" }), [
    { label: "Codex usage", value: "unavailable" },
  ]);
});

test("a usage limit names Codex's own reset time when it reports one", () => {
  assert.equal(limitResetsAt(usage), "2026-10-16T08:22:53Z");
  const limited = check(
    {
      ...healthy,
      processor: {
        ...processor({ state: "usage_limited", consecutiveFailures: 1 }),
        usage,
      },
    },
    "processing",
  );
  assert.match(limited.action!, /Codex reports 16 Oct 2026, 08:22 UTC/);
  assert.equal(limitResetsAt({ current: null }), undefined);
});

test("scheduled retries show when and why; given-up captures need attention", () => {
  const retrying = check(
    {
      ...healthy,
      inbox: inbox({
        pendingCount: 1,
        retryingCount: 1,
        nextRetryAt: "2026-10-09T23:00:30Z",
        nextRetryReason: "usage_limited",
      }),
    },
    "inbox",
  );
  assert.equal(retrying.state, "warning");
  assert.match(retrying.action!, /at the scheduled time/);
  assert.ok(
    retrying.facts.some(
      (fact) =>
        fact.label === "Next retry" &&
        fact.value ===
          "9 Oct 2026, 23:00 UTC (after the Codex usage limit resets)",
    ),
  );
  const failed = check(
    {
      ...healthy,
      inbox: inbox({
        failedCount: 2,
        lastFailureSummary: "gave up after 5 attempts: invalid action plan",
      }),
    },
    "inbox",
  );
  assert.equal(failed.state, "problem");
  assert.match(failed.summary, /2 captures need your attention/);
  assert.match(failed.detail!, /gave up after 5 attempts/);
});

test("the Codex runtime shows the run in progress and past outcomes", () => {
  const facts = runtimeFacts({
    timeoutSeconds: 300,
    current: {
      itemId: "item-1",
      state: "running",
      sessionId: "thread-1",
      since: "2026-10-09T20:00:00Z",
    },
    completed: 4,
    failed: 1,
    timedOut: 1,
    cancelled: 2,
    recovered: 3,
  });
  const value = (label: string) =>
    facts.find((fact) => fact.label === label)?.value;
  assert.equal(value("Codex run now"), "running since 9 Oct 2026, 20:00 UTC");
  assert.equal(
    value("Runs since start"),
    "4 completed, 1 failed, 1 timed out (limit 300 s), 2 cancelled",
  );
  assert.equal(value("Recovered after a crash"), "3");
  assert.deepEqual(runtimeFacts(undefined), []);
});
