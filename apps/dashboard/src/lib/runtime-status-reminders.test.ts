import assert from "node:assert/strict";
import test from "node:test";

import {
  eventRemindersPart,
  foodRemindersPart,
  senderAction,
  senderStaleAfterMs,
  statusAlerts,
  taskRemindersPart,
  toRuntimeStatus,
  type CalendarFeed,
  type FoodNotifications,
  type NotificationChannel,
  type ReminderDelivery,
  type RuntimeSignals,
  type TaskNotifications,
} from "./runtime-status";

const now = new Date("2026-10-10T12:00:00Z");
const zone = "Europe/Berlin";

const delivery = (
  overrides: Partial<ReminderDelivery> = {},
): ReminderDelivery => ({
  enabled: true,
  intervalSeconds: 30,
  graceMinutes: 120,
  handledThrough: "2026-10-10T11:59:40Z",
  lastRunAt: "2026-10-10T11:59:40Z",
  lastSuccessAt: "2026-10-10T11:59:40Z",
  lastError: null,
  lastErrorAt: null,
  lastSentAt: "2026-10-10T09:00:00Z",
  ...overrides,
});

const taskNotifications = (
  overrides: Partial<TaskNotifications> = {},
): TaskNotifications => ({
  ...delivery(),
  calendarConfigured: true,
  timeZone: "Europe/Berlin",
  summaryTime: "08:00",
  morningTime: "09:00",
  eveningTime: "19:00",
  timedLeadMinutes: 30,
  mergedMorning: false,
  ...overrides,
});

const foodNotifications = (
  overrides: Partial<FoodNotifications> = {},
): FoodNotifications => ({
  ...delivery(),
  timeZone: "Europe/Berlin",
  expiryTime: "08:00",
  ...overrides,
});

const ntfy: NotificationChannel = {
  name: "ntfy",
  state: "ok",
  lastSuccessAt: "2026-10-10T11:30:00Z",
  lastFailureAt: null,
  lastError: null,
  recentFailures: 0,
  retrying: 0,
  pending: 0,
};

const signals = (overrides: Partial<RuntimeSignals> = {}): RuntimeSignals => ({
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
  calendarFeeds: [],
  notificationChannels: [ntfy],
  services: [],
  ...overrides,
});

const check = (id: string, overrides: Partial<RuntimeSignals> = {}) =>
  toRuntimeStatus(signals(overrides), now, zone).checks.find(
    (entry) => entry.id === id,
  )!;

const part = (id: string, partId: string, overrides: Partial<RuntimeSignals>) =>
  check(id, overrides).parts?.find((entry) => entry.id === partId);

test("a sender is stale after three intervals and a minute", () => {
  assert.equal(senderStaleAfterMs(30), 150_000);
  const at = (ms: number) => new Date(now.getTime() - ms).toISOString();
  const state = (ms: number) =>
    eventRemindersPart(
      delivery({ lastRunAt: at(ms), lastSuccessAt: at(ms) }),
      now,
      zone,
    ).state;
  assert.equal(state(150_000), "ok");
  assert.equal(state(151_000), "warning");
});

test("healthy reminder delivery is an OK Event reminders part of the Calendar check", () => {
  const calendar = check("calendar", { eventReminders: delivery() });
  assert.equal(calendar.state, "ok");
  assert.equal(calendar.summary, "No calendar feeds are subscribed.");
  assert.equal(calendar.action, undefined);
  const reminders = calendar.parts?.at(-1);
  assert.equal(reminders?.id, "event-reminders");
  assert.equal(reminders?.title, "Event reminders");
  assert.equal(reminders?.state, "ok");
  assert.equal(reminders?.summary, "Sending normally.");
  assert.equal(reminders?.detail, undefined);
  assert.deepEqual(reminders?.facts, [
    { label: "Last check", value: "10 Oct 2026, 13:59 CEST" },
    { label: "Last sent", value: "10 Oct 2026, 11:00 CEST" },
    { label: "Check interval", value: "30 s" },
  ]);

  const quiet = eventRemindersPart(delivery({ lastSentAt: null }), now, zone);
  assert.equal(quiet.state, "ok");
  assert.equal(quiet.summary, "Running; nothing sent yet.");
  assert.deepEqual(quiet.facts[1], { label: "Last sent", value: "never" });
});

test("Event reminders follow the feeds and Google", () => {
  const feed: CalendarFeed = {
    id: "feed-1",
    sourceId: "source-1",
    name: "ZAW",
    url: "https://feeds.example.org/zaw/private-street.ics",
    enabled: true,
    filter: { include: [], exclude: [] },
    refresh: {
      consecutiveFailures: 0,
      refreshing: false,
      lastAttemptAt: "2026-10-10T11:00:00Z",
      lastSuccessAt: "2026-10-10T11:00:00Z",
      nextRefreshAt: "2026-10-10T13:00:00Z",
      lastOutcome: "not_modified",
    },
    createdAt: "2026-10-01T00:00:00Z",
    updatedAt: "2026-10-01T00:00:00Z",
  };
  const calendar = check("calendar", {
    calendarFeeds: [feed],
    googleConnection: {
      configured: true,
      status: "not_connected",
      accountEmail: null,
      scopes: [],
      missingScopes: [],
      connectedAt: null,
      lastError: null,
      exportEnabled: false,
      export: { pending: 0, failed: 0 },
    },
    eventReminders: delivery(),
  });
  assert.deepEqual(
    calendar.parts?.map((entry) => entry.id),
    ["feed-1", "google", "event-reminders"],
  );
});

test("reminders not checked for a while are a warning on Status and Home, with what to do", () => {
  const result = toRuntimeStatus(
    signals({
      eventReminders: delivery({
        lastRunAt: "2026-10-10T11:50:00Z",
        lastSuccessAt: "2026-10-10T11:50:00Z",
      }),
    }),
    now,
    zone,
  );
  const calendar = result.checks.find((entry) => entry.id === "calendar")!;
  const summary =
    "Event reminders are not being sent: the last check ran at 10 Oct 2026, 13:50 CEST.";
  assert.equal(calendar.state, "warning");
  assert.equal(calendar.summary, summary);
  assert.equal(
    calendar.action,
    "Check the calendar service logs (docker compose logs calendar). Reminders due meanwhile are sent when it catches up, up to 2 hours late; older ones are recorded in Activity as missed.",
  );
  assert.equal(result.overall, "warning");
  assert.deepEqual(
    statusAlerts(result).map((entry) => entry.id),
    ["calendar"],
  );
  const reminders = calendar.parts?.at(-1);
  assert.equal(reminders?.state, "warning");
  assert.equal(reminders?.summary, summary);
  assert.equal(reminders?.detail, undefined);
  assert.deepEqual(reminders?.facts.at(-1), {
    label: "Last success",
    value: "10 Oct 2026, 13:50 CEST",
  });
});

test("enabled reminders that never ran are a warning", () => {
  const reminders = eventRemindersPart(
    delivery({
      handledThrough: null,
      lastRunAt: null,
      lastSuccessAt: null,
      lastSentAt: null,
    }),
    now,
    zone,
  );
  assert.equal(reminders.state, "warning");
  assert.equal(
    reminders.summary,
    "Event reminders are not being sent: no check has run yet.",
  );
  assert.deepEqual(reminders.facts[0], { label: "Last check", value: "never" });
});

test("a failed last check is a warning with Calendar's error", () => {
  const error = "send reminder: notifications: 503 Service Unavailable";
  const reminders = part("calendar", "event-reminders", {
    eventReminders: delivery({
      lastRunAt: "2026-10-10T11:59:40Z",
      lastSuccessAt: "2026-10-10T11:40:00Z",
      lastError: error,
      lastErrorAt: "2026-10-10T11:59:41Z",
    }),
  });
  assert.equal(reminders?.state, "warning");
  assert.equal(
    reminders?.summary,
    "Event reminders may be late: the last check failed.",
  );
  assert.equal(reminders?.detail, error);
  assert.deepEqual(reminders?.facts.at(-1), {
    label: "Last success",
    value: "10 Oct 2026, 13:40 CEST",
  });

  // A stopped sender whose last check failed shows that error too.
  const stopped = eventRemindersPart(
    delivery({
      lastRunAt: "2026-10-10T11:00:00Z",
      lastSuccessAt: "2026-10-10T10:00:00Z",
      lastError: error,
      lastErrorAt: "2026-10-10T11:00:01Z",
    }),
    now,
    zone,
  );
  assert.match(stopped.summary, /^Event reminders are not being sent: /);
  assert.equal(stopped.detail, error);

  // An error that has not happened since, nor before, any success.
  const failedFirst = eventRemindersPart(
    delivery({ lastSuccessAt: null, lastError: error, lastErrorAt: null }),
    now,
    zone,
  );
  assert.equal(failedFirst.state, "warning");
  assert.equal(failedFirst.detail, error);
});

test("an error older than the last success is history: still OK", () => {
  const reminders = eventRemindersPart(
    delivery({
      lastError: "list occurrences: timeout",
      lastErrorAt: "2026-10-10T11:30:00Z",
    }),
    now,
    zone,
  );
  assert.equal(reminders.state, "ok");
  assert.equal(reminders.detail, undefined);
});

test("checks that run but no longer succeed are a warning", () => {
  const reminders = eventRemindersPart(
    delivery({ lastSuccessAt: "2026-10-10T11:00:00Z" }),
    now,
    zone,
  );
  assert.equal(reminders.state, "warning");
  assert.equal(
    reminders.summary,
    "Event reminders are not being sent: the last successful check ran at 10 Oct 2026, 13:00 CEST.",
  );
  const never = eventRemindersPart(
    delivery({ lastSuccessAt: null }),
    now,
    zone,
  );
  assert.equal(
    never.summary,
    "Event reminders are not being sent: no check has succeeded yet.",
  );
});

test("reminders that are off are Not set up and raise no alert", () => {
  const result = toRuntimeStatus(
    signals({
      eventReminders: delivery({ enabled: false, lastRunAt: null }),
    }),
    now,
    zone,
  );
  const calendar = result.checks.find((entry) => entry.id === "calendar")!;
  assert.equal(calendar.state, "ok");
  const reminders = calendar.parts?.at(-1);
  assert.equal(reminders?.state, "neutral");
  assert.equal(
    reminders?.summary,
    "Calendar sends no reminders: its NOTIFICATIONS_SERVICE_URL is not set.",
  );
  assert.deepEqual(reminders?.facts, []);
  assert.equal(result.overall, "ok");
  assert.deepEqual(statusAlerts(result), []);
});

test("unreadable reminder health adds nothing: Services names the service", () => {
  for (const eventReminders of [null, undefined]) {
    const calendar = check("calendar", { eventReminders });
    assert.equal(calendar.state, "ok");
    assert.equal(calendar.parts, undefined);
  }
  for (const taskReminders of [null, undefined]) {
    const notifications = check("notifications", { taskReminders });
    assert.equal(notifications.state, "ok");
    assert.deepEqual(
      notifications.parts?.map((entry) => entry.id),
      ["ntfy"],
    );
  }
});

test("a check already in trouble keeps its own summary", () => {
  const notifications = check("notifications", {
    notificationChannels: null,
    taskReminders: taskNotifications({
      lastRunAt: "2026-10-10T11:00:00Z",
      lastSuccessAt: "2026-10-10T11:00:00Z",
    }),
  });
  assert.equal(notifications.state, "warning");
  assert.equal(notifications.summary, "Notification delivery is unavailable.");
  assert.equal(notifications.parts?.[0].state, "warning");
});

test("healthy Task reminders follow the delivery channels in Notifications", () => {
  const notifications = check("notifications", {
    taskReminders: taskNotifications(),
  });
  assert.equal(notifications.state, "ok");
  assert.equal(notifications.summary, "Delivering to ntfy (phone).");
  assert.deepEqual(
    notifications.parts?.map((entry) => entry.id),
    ["ntfy", "task-reminders"],
  );
  const reminders = notifications.parts?.at(-1);
  assert.equal(reminders?.title, "Task reminders");
  assert.equal(reminders?.state, "ok");
  assert.equal(reminders?.summary, "Sending normally.");
  assert.deepEqual(reminders?.facts, [
    { label: "Last check", value: "10 Oct 2026, 13:59 CEST" },
    { label: "Last sent", value: "10 Oct 2026, 11:00 CEST" },
    { label: "Check interval", value: "30 s" },
    {
      label: "Schedule",
      value:
        "summary 08:00, morning list 09:00, evening list 19:00 (Europe/Berlin)",
    },
    { label: "Timed Tasks", value: "reminded 30 min before they are due" },
  ]);
});

test("Task reminders show a merged morning and a summary without events", () => {
  const reminders = taskRemindersPart(
    taskNotifications({
      mergedMorning: true,
      morningTime: "08:00",
      calendarConfigured: false,
    }),
    now,
    zone,
  );
  assert.equal(reminders.state, "ok");
  assert.deepEqual(reminders.facts.slice(3), [
    {
      label: "Schedule",
      value:
        "summary with the morning list 08:00, evening list 19:00 (Europe/Berlin)",
    },
    { label: "Timed Tasks", value: "reminded 30 min before they are due" },
    {
      label: "Events in the summary",
      value: "none: CALENDAR_SERVICE_URL is not set",
    },
  ]);
});

test("stalled Task reminders turn a Not set up Notifications check into a warning", () => {
  const result = toRuntimeStatus(
    signals({
      notificationChannels: [],
      taskReminders: taskNotifications({
        graceMinutes: 90,
        lastRunAt: "2026-10-10T11:40:00Z",
        lastSuccessAt: "2026-10-10T11:40:00Z",
      }),
    }),
    now,
    zone,
  );
  const notifications = result.checks.find(
    (entry) => entry.id === "notifications",
  )!;
  assert.equal(notifications.state, "warning");
  assert.equal(
    notifications.summary,
    "Task reminders are not being sent: the last check ran at 10 Oct 2026, 13:40 CEST.",
  );
  assert.equal(
    notifications.action,
    "Check the tasks service logs (docker compose logs tasks). Reminders and daily lists due meanwhile are sent when it catches up, up to 90 minutes late; older ones are recorded in Activity as missed.",
  );
  assert.deepEqual(
    statusAlerts(result).map((entry) => entry.id),
    ["notifications"],
  );
});

test("Task reminders that are off leave Notifications as it is", () => {
  const notifications = check("notifications", {
    notificationChannels: [],
    taskReminders: taskNotifications({ enabled: false }),
  });
  assert.equal(notifications.state, "neutral");
  assert.match(notifications.summary, /^No delivery channel is set up/);
  const reminders = notifications.parts?.at(-1);
  assert.equal(reminders?.state, "neutral");
  assert.equal(
    reminders?.summary,
    "Tasks sends no reminders or daily lists: its NOTIFICATIONS_SERVICE_URL is not set.",
  );
});

test("the grace window reads in hours or minutes", () => {
  assert.match(senderAction("calendar", "Reminders", 60), /up to 1 hour late/);
  assert.match(senderAction("calendar", "Reminders", 1), /up to 1 minute late/);
  assert.match(
    senderAction("calendar", "Reminders", 150),
    /up to 150 minutes late/,
  );
});

test("healthy Food reminders follow Task reminders in Notifications", () => {
  const notifications = check("notifications", {
    taskReminders: taskNotifications(),
    foodReminders: foodNotifications({ lastSentAt: null }),
  });
  assert.equal(notifications.state, "ok");
  assert.deepEqual(
    notifications.parts?.map((entry) => entry.id),
    ["ntfy", "task-reminders", "food-reminders"],
  );
  const reminders = notifications.parts?.at(-1);
  assert.equal(reminders?.title, "Food reminders");
  assert.equal(reminders?.state, "ok");
  assert.equal(reminders?.summary, "Running; nothing sent yet.");
  assert.deepEqual(reminders?.facts, [
    { label: "Last check", value: "10 Oct 2026, 13:59 CEST" },
    { label: "Last sent", value: "never" },
    { label: "Check interval", value: "30 s" },
    { label: "Expiry reminder", value: "daily at 08:00 (Europe/Berlin)" },
  ]);
  assert.equal(
    foodRemindersPart(foodNotifications(), now, zone).summary,
    "Sending normally.",
  );
});

test("stalled or failing Food reminders make Notifications a warning", () => {
  const stalled = check("notifications", {
    foodReminders: foodNotifications({
      lastRunAt: "2026-10-10T11:40:00Z",
      lastSuccessAt: "2026-10-10T11:40:00Z",
    }),
  });
  assert.equal(stalled.state, "warning");
  assert.equal(
    stalled.summary,
    "Food reminders are not being sent: the last check ran at 10 Oct 2026, 13:40 CEST.",
  );
  assert.equal(
    stalled.action,
    "Check the food service logs (docker compose logs food). Expiry reminders due meanwhile are sent when it catches up, up to 2 hours late; older ones are recorded in Activity as missed.",
  );
  const failing = part("notifications", "food-reminders", {
    foodReminders: foodNotifications({
      lastError: "send notification: 503",
      lastErrorAt: "2026-10-10T11:59:40Z",
      lastSuccessAt: "2026-10-10T11:58:40Z",
    }),
  });
  assert.equal(failing?.state, "warning");
  assert.equal(
    failing?.summary,
    "Food reminders may be late: the last check failed.",
  );
  assert.equal(failing?.detail, "send notification: 503");
});

test("Food reminders that are off or unreadable leave Notifications as it is", () => {
  const off = check("notifications", {
    foodReminders: foodNotifications({ enabled: false }),
  });
  assert.equal(off.state, "ok");
  const reminders = off.parts?.at(-1);
  assert.equal(reminders?.state, "neutral");
  assert.equal(
    reminders?.summary,
    "Food sends no expiry reminders: its NOTIFICATIONS_SERVICE_URL is not set.",
  );
  for (const foodReminders of [null, undefined]) {
    assert.deepEqual(
      check("notifications", { foodReminders }).parts?.map((entry) => entry.id),
      ["ntfy"],
    );
  }
});
