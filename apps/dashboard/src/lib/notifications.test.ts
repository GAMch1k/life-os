import assert from "node:assert/strict";
import test from "node:test";

import {
  bellState,
  deliveryView,
  emptyMessage,
  fullTime,
  groupByDay,
  happenedAt,
  hasFilters,
  LIFE_OS_ONLY,
  listQuery,
  markAllBefore,
  markAllReadLocally,
  notificationsHref,
  parseFilters,
  preview,
  relativeTime,
  replaceNotification,
  safeLink,
  sourceLabel,
  sourceOptions,
  testNotification,
  type Delivery,
  type Notification,
} from "./notifications";

const BERLIN = "Europe/Berlin";
const now = new Date("2026-10-10T12:00:00Z"); // 14:00 CEST

const notification = (
  id: string,
  createdAt: string,
  changes: Partial<Notification> = {},
): Notification => ({
  id,
  title: `Notification ${id}`,
  body: "",
  importance: "medium",
  category: "test.manual",
  source: "dashboard",
  link: null,
  dedupeKey: `test.manual:${id}`,
  occurredAt: createdAt,
  createdAt,
  readAt: null,
  deliveries: [],
  ...changes,
});

const delivery = (changes: Partial<Delivery>): Delivery => ({
  channel: "ntfy",
  status: "pending",
  attempts: 0,
  lastError: null,
  nextAttemptAt: null,
  sentAt: null,
  ...changes,
});

test("the bell shows a dot, tinted for high importance, and names the count", () => {
  assert.deepEqual(bellState(null), { dot: "none", label: "Notifications" });
  assert.deepEqual(bellState({ count: 0, highCount: 0 }), {
    dot: "none",
    label: "Notifications",
  });
  assert.deepEqual(bellState({ count: 3, highCount: 0 }), {
    dot: "unread",
    label: "Notifications, 3 unread",
  });
  assert.deepEqual(bellState({ count: 1, highCount: 1 }), {
    dot: "high",
    label: "Notifications, 1 unread",
  });
});

test("previews collapse whitespace and shorten long bodies", () => {
  assert.equal(preview("  Two\n\nlines\tand  spaces "), "Two lines and spaces");
  assert.equal(preview("abcdefghij", 6), "abcde…");
  assert.equal(preview("abcdef", 6), "abcdef");
  assert.equal(preview(""), "");
});

test("relative times read naturally in the owner's timezone", () => {
  const ago = (ms: number) => new Date(now.getTime() - ms).toISOString();
  assert.equal(relativeTime(ago(20_000), BERLIN, now), "Just now");
  // A notification from a clock slightly ahead is not "in the future".
  assert.equal(relativeTime(ago(-30_000), BERLIN, now), "Just now");
  assert.equal(relativeTime(ago(5 * 60_000), BERLIN, now), "5 min ago");
  assert.equal(relativeTime(ago(3 * 3_600_000), BERLIN, now), "3 h ago");
  // 21:30 CEST the day before.
  assert.equal(
    relativeTime("2026-10-09T19:30:00Z", BERLIN, now),
    "Yesterday, 21:30",
  );
  assert.equal(
    relativeTime("2026-10-07T08:05:00Z", BERLIN, now),
    "7 Oct, 10:05",
  );
  // 23:30 UTC on 9 Oct is already 10 Oct in Berlin: today, not yesterday.
  assert.equal(relativeTime("2026-10-09T23:30:00Z", BERLIN, now), "12 h ago");
  assert.equal(
    relativeTime("2026-10-09T23:30:00Z", "UTC", now),
    "Yesterday, 23:30",
  );
  assert.equal(
    fullTime("2026-10-09T18:22:00Z", BERLIN),
    "9 Oct 2026, 20:22 CEST",
  );
});

test("notifications group by the owner's day, newest first", () => {
  const items = [
    notification("a", "2026-10-10T11:00:00Z"),
    // 00:30 CEST on 10 Oct: still today in Berlin.
    notification("b", "2026-10-09T22:30:00Z"),
    notification("c", "2026-10-09T21:30:00Z"),
    notification("d", "2026-10-06T09:00:00Z"),
  ];
  const days = groupByDay(items, BERLIN, now);
  assert.deepEqual(
    days.map((day) => [day.day, day.label, day.items.map((item) => item.id)]),
    [
      ["2026-10-10", "Today", ["a", "b"]],
      ["2026-10-09", "Yesterday", ["c"]],
      ["2026-10-06", "Tue 6 Oct", ["d"]],
    ],
  );
  assert.deepEqual(groupByDay([], BERLIN, now), []);
});

test("the time it happened is shown only when it differs from receipt", () => {
  const at = "2026-10-10T11:00:00Z";
  assert.equal(happenedAt({ createdAt: at, occurredAt: at }), undefined);
  assert.equal(
    happenedAt({ createdAt: at, occurredAt: "2026-10-10T10:59:30Z" }),
    undefined,
  );
  assert.equal(
    happenedAt({ createdAt: at, occurredAt: "2026-10-10T10:40:00Z" }),
    "2026-10-10T10:40:00Z",
  );
});

test("delivery states say what happened on each channel", () => {
  assert.equal(LIFE_OS_ONLY, "Shown in Life OS only");
  assert.deepEqual(
    deliveryView(
      delivery({ status: "sent", attempts: 1, sentAt: "2026-10-10T11:05:00Z" }),
      BERLIN,
      now,
    ),
    { text: "ntfy: sent 13:05", tone: "success" },
  );
  assert.deepEqual(deliveryView(delivery({}), BERLIN, now), {
    text: "ntfy: sending",
    tone: "muted",
  });
  assert.deepEqual(
    deliveryView(
      delivery({
        status: "retrying",
        attempts: 2,
        lastError: " 503 Service Unavailable ",
        nextAttemptAt: "2026-10-10T12:10:00Z",
      }),
      BERLIN,
      now,
    ),
    {
      text: "ntfy: retrying after 2 attempts, next try 14:10",
      tone: "warning",
      error: "503 Service Unavailable",
    },
  );
  assert.deepEqual(
    deliveryView(
      delivery({
        status: "failed",
        attempts: 1,
        lastError: "401 unauthorized",
      }),
      BERLIN,
      now,
    ),
    {
      text: "ntfy: failed after 1 attempt",
      tone: "destructive",
      error: "401 unauthorized",
    },
  );
  assert.deepEqual(deliveryView(delivery({ status: "skipped" }), BERLIN, now), {
    text: "ntfy: skipped",
    tone: "muted",
  });
});

test("filters come from search params and drop invalid values", () => {
  assert.deepEqual(parseFilters({}), {});
  assert.deepEqual(
    parseFilters({ importance: "high", source: "calendar", unread: "true" }),
    { importance: "high", source: "calendar", unread: true },
  );
  assert.deepEqual(
    parseFilters({
      importance: "urgent",
      source: "Not a source!",
      unread: "false",
    }),
    {},
  );
  assert.deepEqual(parseFilters({ importance: ["low", "high"] }), {
    importance: "low",
  });
  assert.equal(hasFilters({}), false);
  assert.equal(hasFilters({ unread: true }), true);
});

test("filters round-trip through the page URL and the API query", () => {
  assert.equal(notificationsHref({}), "/notifications");
  assert.equal(
    notificationsHref({ importance: "low", source: "inbox", unread: true }),
    "/notifications?importance=low&source=inbox&unread=true",
  );
  assert.deepEqual(listQuery({ importance: "high" }, "next", 10), {
    importance: "high",
    source: undefined,
    unread: undefined,
    cursor: "next",
    limit: 10,
  });
  assert.equal(listQuery({}).limit, 50);
  assert.equal(emptyMessage({}), "No notifications yet.");
  assert.equal(
    emptyMessage({ source: "calendar" }),
    "No notifications match these filters.",
  );
});

test("sources read as names, and the filter offers known and seen ones", () => {
  assert.equal(sourceLabel("calendar"), "Calendar");
  assert.equal(sourceLabel("dev"), "Developer tool");
  assert.equal(sourceLabel("food-tracker"), "Food tracker");
  const options = sourceOptions(["food-tracker", "calendar"], "zaw");
  assert.ok(options.some((option) => option.value === "food-tracker"));
  assert.ok(options.some((option) => option.value === "zaw"));
  assert.equal(
    options.filter((option) => option.value === "calendar").length,
    1,
  );
  const labels = options.map((option) => option.label);
  assert.deepEqual(
    labels,
    [...labels].sort((a, b) => a.localeCompare(b)),
  );
});

test("only Life OS paths are followed", () => {
  assert.equal(
    safeLink("/calendar?date=2026-10-10"),
    "/calendar?date=2026-10-10",
  );
  assert.equal(safeLink("/"), "/");
  assert.equal(safeLink(null), undefined);
  assert.equal(safeLink("//evil.example"), undefined);
  assert.equal(safeLink("/\\evil.example"), undefined);
  assert.equal(safeLink("https://evil.example"), undefined);
});

test("marking read updates the list in place", () => {
  const items = [
    notification("new", "2026-10-10T11:59:00Z"),
    notification("old", "2026-10-10T10:00:00Z"),
    notification("seen", "2026-10-10T09:00:00Z", {
      readAt: "2026-10-10T09:30:00Z",
    }),
  ];
  const read = { ...items[1], readAt: "2026-10-10T12:00:00Z" };
  assert.deepEqual(
    replaceNotification(items, read).map((item) => item.readAt),
    [null, "2026-10-10T12:00:00Z", "2026-10-10T09:30:00Z"],
  );
  // Everything up to `before`; a later arrival and an earlier read stay.
  assert.deepEqual(
    markAllReadLocally(
      items,
      "2026-10-10T10:00:00Z",
      "2026-10-10T12:00:00Z",
    ).map((item) => item.readAt),
    [null, "2026-10-10T12:00:00Z", "2026-10-10T09:30:00Z"],
  );
  assert.deepEqual(
    markAllReadLocally(items, undefined, "2026-10-10T12:00:00Z").map(
      (item) => item.readAt,
    ),
    ["2026-10-10T12:00:00Z", "2026-10-10T12:00:00Z", "2026-10-10T09:30:00Z"],
  );
});

test("mark all read stops at the newest notification the owner saw", () => {
  const items = [
    notification("a", "2026-10-10T11:59:00.123456Z"),
    notification("b", "2026-10-10T10:00:00Z"),
  ];
  // The service's own instant, unchanged, so nothing at it is missed.
  assert.equal(markAllBefore(items), "2026-10-10T11:59:00.123456Z");
  assert.equal(
    markAllBefore(items, "2026-10-10T12:00:00.000Z"),
    "2026-10-10T12:00:00.000Z",
  );
  assert.equal(markAllBefore([]), undefined);
  assert.equal(
    markAllBefore([], "2026-10-10T12:00:00.000Z"),
    "2026-10-10T12:00:00.000Z",
  );
});

test("a test notification is new every time and uses the test category", () => {
  assert.deepEqual(testNotification("high", "k1"), {
    title: "Test notification (high)",
    body: "Sent from the Dashboard to check that notifications reach you.",
    importance: "high",
    category: "test.manual",
    link: "/notifications",
    dedupeKey: "test.manual:k1",
  });
  const first = testNotification("medium");
  const second = testNotification("medium");
  assert.equal(first.title, "Test notification (medium)");
  assert.notEqual(first.dedupeKey, second.dedupeKey);
  assert.match(first.dedupeKey, /^test\.manual:\S+$/);
  assert.equal("source" in first, false);
});
