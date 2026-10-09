import assert from "node:assert/strict";
import test from "node:test";

import {
  formatAge,
  statusAlerts,
  toRuntimeStatus,
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
  assert.match(backlog.summary, /1 capture is waiting to be retried/);
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
