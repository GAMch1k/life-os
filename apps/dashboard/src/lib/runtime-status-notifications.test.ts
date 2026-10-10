import assert from "node:assert/strict";
import test from "node:test";

import {
  NOTIFICATIONS_FAILING_ACTION,
  channelPart,
  statusAlerts,
  toRuntimeStatus,
  type NotificationChannel,
  type RuntimeSignals,
} from "./runtime-status";

const now = new Date("2026-10-10T12:00:00Z");

const signals = (
  notificationChannels: NotificationChannel[] | null | undefined,
): RuntimeSignals => ({
  processor: {
    model: "gpt-5.6-luna",
    startedAt: "2026-10-10T11:00:00Z",
    poll: { lastAt: "2026-10-10T12:00:00Z", lastSucceededAt: null },
    codex: {
      state: "ready",
      authenticated: true,
      authCheckedAt: null,
      lastSucceededAt: null,
      lastFailedAt: null,
      consecutiveFailures: 0,
    },
  },
  inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
  voice: {
    queuedCount: 0,
    processingCount: 0,
    retryableCount: 0,
    workerStalled: false,
    whisperReady: true,
  },
  notificationChannels,
  services: [{ name: "Notifications", ready: true }],
});

const channel = (
  overrides: Partial<NotificationChannel> = {},
): NotificationChannel => ({
  name: "ntfy",
  state: "ok",
  lastSuccessAt: "2026-10-10T11:30:00Z",
  lastFailureAt: null,
  lastError: null,
  recentFailures: 0,
  retrying: 0,
  pending: 0,
  ...overrides,
});

const status = (channels: NotificationChannel[] | null | undefined) =>
  toRuntimeStatus(signals(channels), now, "Europe/Berlin");
const notificationsCheck = (channels: NotificationChannel[] | null) =>
  status(channels).checks.find((check) => check.id === "notifications")!;

test("no delivery channel is neutral: not an alert, and everything still works", () => {
  const result = status([]);
  const check = result.checks.find((entry) => entry.id === "notifications")!;
  assert.equal(check.title, "Notifications");
  assert.equal(check.state, "neutral");
  assert.match(check.summary, /^No delivery channel is set up/);
  assert.match(check.action ?? "", /docs\/operations\/ntfy\.md/);
  assert.equal(result.overall, "ok");
  assert.deepEqual(statusAlerts(result), []);
});

test("Notifications sits before Services and is left out when not checked", () => {
  assert.deepEqual(
    status([channel()]).checks.map((check) => check.id),
    ["processing", "inbox", "voice", "notifications", "services"],
  );
  assert.equal(
    status(undefined).checks.some((check) => check.id === "notifications"),
    false,
  );
});

test("a healthy ntfy channel lists its last delivery", () => {
  const check = notificationsCheck([channel()]);
  assert.equal(check.state, "ok");
  assert.equal(check.summary, "Delivering to ntfy (phone).");
  const [part] = check.parts!;
  assert.equal(part.title, "ntfy (phone)");
  assert.equal(part.summary, "Delivering normally.");
  assert.deepEqual(part.facts, [
    { label: "Last delivered", value: "10 Oct 2026, 13:30 CEST" },
    { label: "Last failure", value: "never" },
    { label: "Failed in the last 24 h", value: "0" },
  ]);
});

test("an unused channel is fine", () => {
  const part = channelPart(
    channel({ state: "idle", lastSuccessAt: null }),
    "Europe/Berlin",
  );
  assert.equal(part.state, "ok");
  assert.equal(part.summary, "Set up; nothing sent yet.");
});

test("failing ntfy deliveries are a warning on Status and Home, with ntfy's error", () => {
  const failing = channel({
    state: "failing",
    lastFailureAt: "2026-10-10T11:50:00Z",
    lastError:
      "ntfy refused the notification: 401 unauthorized (ntfy code 40101)",
    recentFailures: 3,
    retrying: 1,
  });
  const result = status([failing, channel({ name: "log" })]);
  const check = result.checks.find((entry) => entry.id === "notifications")!;
  assert.equal(check.state, "warning");
  assert.equal(check.summary, "Notifications are not reaching ntfy (phone).");
  assert.equal(check.action, NOTIFICATIONS_FAILING_ACTION);
  assert.equal(result.overall, "warning");
  assert.deepEqual(
    statusAlerts(result).map((entry) => entry.id),
    ["notifications"],
  );
  const [ntfy, log] = check.parts!;
  assert.equal(ntfy.state, "warning");
  assert.equal(
    ntfy.summary,
    "Deliveries are failing; they are retried automatically.",
  );
  assert.equal(ntfy.detail, failing.lastError);
  assert.deepEqual(ntfy.facts.slice(1), [
    { label: "Last failure", value: "10 Oct 2026, 13:50 CEST" },
    { label: "Failed in the last 24 h", value: "3" },
    { label: "Waiting for a retry", value: "1" },
  ]);
  assert.equal(log.title, "Service log");
  assert.equal(log.state, "ok");

  // A delivery that failed for good, with nothing waiting for a retry.
  const failed = channelPart(
    channel({ ...failing, retrying: 0 }),
    "Europe/Berlin",
  );
  assert.equal(failed.summary, "The latest delivery failed.");
});

test("an unreadable Notifications service is a warning", () => {
  const check = notificationsCheck(null);
  assert.equal(check.state, "warning");
  assert.equal(check.summary, "Notification delivery is unavailable.");
});
