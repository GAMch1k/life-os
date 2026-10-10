import assert from "node:assert/strict";
import test from "node:test";

import {
  SOURCE_FAILURE_ALERT,
  formatAge,
  limitResetsAt,
  runtimeFacts,
  statusAlerts,
  toRuntimeStatus,
  usageFacts,
  type AudioBacklog,
  type CalendarFeed,
  type CalendarSource,
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

test("times are shown in the owner's timezone with its abbreviation", () => {
  const facts = runtimeFacts(
    {
      timeoutSeconds: 300,
      current: {
        itemId: "item-1",
        state: "running",
        since: "2026-10-09T20:00:00Z",
      },
      completed: 0,
      failed: 0,
      timedOut: 0,
      cancelled: 0,
      recovered: 0,
    },
    "Europe/Berlin",
  );
  assert.equal(
    facts.find((fact) => fact.label === "Codex run now")?.value,
    "running since 9 Oct 2026, 22:00 CEST",
  );
  const status = toRuntimeStatus(
    {
      ...healthy,
      processor: { ...processor(), timeZone: "Europe/Berlin" },
      inbox: inbox({
        pendingCount: 1,
        retryingCount: 1,
        nextRetryAt: "2026-10-09T23:00:30Z",
        nextRetryReason: "backoff",
      }),
    },
    now,
    "Europe/Berlin",
  );
  assert.equal(status.timeZone, "Europe/Berlin");
  const fact = (id: string, label: string) =>
    status.checks
      .find((entry) => entry.id === id)
      ?.facts.find((entry) => entry.label === label)?.value;
  // 23:00 UTC is already the next day in Berlin.
  assert.equal(
    fact("inbox", "Next retry"),
    "10 Oct 2026, 01:00 CEST (backoff)",
  );
  assert.equal(
    fact("processing", "Planning timezone"),
    "capture's browser timezone, else Europe/Berlin",
  );
  assert.equal(fact("processing", "Last poll"), "9 Oct 2026, 20:00 CEST");
});

test("the planning timezone is not compared with the viewer's browser zone", () => {
  // The page is shown in the browser's zone; the processor's configured zone
  // is only the fallback for captures without one, so this is no mismatch.
  const status = toRuntimeStatus(
    { ...healthy, processor: { ...processor(), timeZone: "Europe/Berlin" } },
    now,
    "America/New_York",
    "Europe/Berlin",
  );
  assert.equal(
    status.checks[0].facts.find((fact) => fact.label === "Planning timezone")
      ?.value,
    "capture's browser timezone, else Europe/Berlin",
  );
  assert.equal(status.checks[0].state, "ok");
});

test("a processor configured with another timezone than the Dashboard is pointed out", () => {
  const status = toRuntimeStatus(
    { ...healthy, processor: { ...processor(), timeZone: "UTC" } },
    now,
    "America/New_York",
    "Europe/Berlin",
  );
  assert.equal(
    status.checks[0].facts.find((fact) => fact.label === "Planning timezone")
      ?.value,
    "capture's browser timezone, else UTC (the Dashboard's LIFE_OS_TIMEZONE is Europe/Berlin)",
  );
});

const feed = (
  name: string,
  refresh: Partial<CalendarFeed["refresh"]> = {},
  overrides: Partial<CalendarFeed> = {},
): CalendarFeed => ({
  id: `feed-${name}`,
  sourceId: `source-${name}`,
  name,
  url: `https://feeds.example.org/${name}/private-street.ics`,
  enabled: true,
  filter: { include: [], exclude: [] },
  refresh: {
    consecutiveFailures: 0,
    refreshing: false,
    lastAttemptAt: minutesAgo(30),
    lastSuccessAt: minutesAgo(30),
    nextRefreshAt: "2026-10-10T02:30:00Z",
    lastOutcome: "imported",
    lastCounts: {
      feedEvents: 29,
      imported: 21,
      inserted: 3,
      updated: 0,
      removed: 1,
      restored: 0,
      unchanged: 17,
    },
    ...refresh,
  },
  createdAt: minutesAgo(600),
  updatedAt: minutesAgo(600),
  ...overrides,
});
const calendarCheck = (
  feeds: CalendarFeed[] | null,
  calendarSources?: CalendarSource[] | null,
) => {
  const status = toRuntimeStatus(
    { ...healthy, calendarFeeds: feeds, calendarSources },
    now,
    "Europe/Berlin",
  );
  return { status, check: status.checks.find((c) => c.id === "calendar")! };
};

test("calendar feeds show their last refresh, success, and next run", () => {
  const { status, check } = calendarCheck([feed("ZAW")]);
  assert.equal(status.overall, "ok");
  assert.equal(check.title, "Calendar sources");
  assert.equal(check.summary, "1 calendar feed refreshing normally.");
  const [part] = check.parts!;
  assert.equal(part.title, "ZAW");
  assert.equal(part.state, "ok");
  assert.deepEqual(part.facts, [
    {
      label: "Last refresh",
      value: "imported, 3 added, 1 cancelled (9 Oct 2026, 19:30 CEST)",
    },
    { label: "Last success", value: "9 Oct 2026, 19:30 CEST" },
    { label: "Next refresh", value: "10 Oct 2026, 04:30 CEST" },
  ]);
  // The feed URL can carry personal data such as a street.
  assert.doesNotMatch(JSON.stringify(status), /private-street|feeds\.example/);

  assert.equal(
    calendarCheck([]).check.summary,
    "No calendar feeds are subscribed.",
  );
  const unavailable = calendarCheck(null);
  assert.equal(unavailable.check.state, "problem");
  assert.equal(unavailable.check.summary, "Calendar sources are unavailable.");
  // Without the signal the check is left out.
  assert.equal(
    toRuntimeStatus(healthy, now).checks.some((c) => c.id === "calendar"),
    false,
  );
});

test("a feed shows the name and switch its Calendar source has now", () => {
  const source = (
    id: string,
    name: string,
    enabled: boolean,
  ): CalendarSource => ({
    id,
    kind: "external",
    name,
    enabled,
    integration: "calendar-feeds",
    externalId: id,
    provider: "ICS",
    readOnly: true,
    privateLocation: false,
    defaultReminders: null,
    defaultPriority: "medium",
    createdAt: minutesAgo(600),
    updatedAt: minutesAgo(600),
  });
  const failing = feed("ZAW", {
    lastOutcome: "failed",
    consecutiveFailures: SOURCE_FAILURE_ALERT,
  });
  // Renamed and switched off in Calendar since calendar-feeds last heard.
  const { check } = calendarCheck(
    [failing, feed("School")],
    [source("source-ZAW", "Müllabfuhr", false)],
  );
  assert.equal(check.state, "ok");
  assert.deepEqual(
    check.parts!.map((part) => [part.title, part.state]),
    [
      ["Müllabfuhr", "ok"],
      ["School", "ok"],
    ],
  );
  assert.match(check.parts![0].summary, /^Disabled/);
  // Calendar unreadable: the feeds' own copies.
  assert.equal(calendarCheck([failing], null).check.state, "warning");
});

test("a failed feed refresh shows on Status; Home hears only of repeated failures", () => {
  const failed = (failures: number) =>
    feed("ZAW", {
      lastOutcome: "failed",
      lastError: "the feed answered HTTP 503",
      consecutiveFailures: failures,
      lastAttemptAt: minutesAgo(5),
      nextRefreshAt: "2026-10-09T18:15:00Z",
    });

  const retrying = calendarCheck([failed(1), feed("School")]);
  assert.equal(retrying.check.state, "ok");
  assert.match(retrying.check.summary, /^ZAW: the last refresh failed/);
  assert.deepEqual(statusAlerts(retrying.status), []);
  const [zaw, school] = retrying.check.parts!;
  assert.equal(zaw.state, "warning");
  assert.equal(zaw.detail, "the feed answered HTTP 503");
  assert.match(zaw.summary, /retried automatically/);
  assert.deepEqual(
    zaw.facts.map((fact) => fact.label),
    ["Last refresh", "Last success", "Failed in a row", "Next refresh"],
  );
  assert.equal(zaw.facts[0].value, "failed (9 Oct 2026, 19:55 CEST)");
  assert.equal(school.state, "ok");

  const keepsFailing = calendarCheck([failed(SOURCE_FAILURE_ALERT)]);
  assert.equal(keepsFailing.check.state, "warning");
  assert.equal(
    keepsFailing.check.summary,
    "The calendar feed ZAW has failed 3 refreshes in a row.",
  );
  assert.equal(keepsFailing.status.overall, "warning");
  assert.deepEqual(
    statusAlerts(keepsFailing.status).map((check) => check.id),
    ["calendar"],
  );
  assert.match(keepsFailing.check.parts![0].summary, /^Keeps failing: 3/);

  // A disabled feed is not refreshed, so its old failure needs nothing.
  const disabled = calendarCheck([
    { ...failed(5), enabled: false },
    feed("New", { lastOutcome: undefined, lastSuccessAt: null }),
  ]);
  assert.equal(disabled.check.state, "ok");
  assert.match(disabled.check.parts![0].summary, /^Disabled/);
  assert.equal(disabled.check.parts![0].facts.at(-1)?.value, "none (disabled)");
  assert.equal(
    disabled.check.parts![1].summary,
    "Waiting for its first refresh.",
  );
});
