import assert from "node:assert/strict";
import test from "node:test";

import {
  activityHref,
  actorLabel,
  buildTrace,
  calendarHref,
  EVENT_TYPES,
  emptyState,
  eventLabel,
  eventTime,
  fullSummary,
  isDevActor,
  listQuery,
  parseFilters,
  refreshChanges,
  selectedEventId,
  sourceDescription,
  sourceLabel,
  subjectLabel,
  type ActivityEvent,
} from "./activity-log";

let clock = Date.parse("2026-10-09T10:00:00Z");
function event(
  id: string,
  type: string,
  options: Partial<ActivityEvent> = {},
): ActivityEvent {
  clock += 1000;
  const at = new Date(clock).toISOString();
  return {
    id,
    type,
    source: type.split(".")[0],
    subject: { type: "task", id: "task-1" },
    correlationId: "job-1",
    actor: { kind: "user" },
    outcome: "succeeded",
    summary: type,
    data: {},
    occurredAt: at,
    recordedAt: at,
    ...options,
  };
}

test("a selected event shows the chain that led to it and what it led to", () => {
  const captured = event("a", "inbox.item.captured");
  const started = event("b", "inbox.processing.started", { causationId: "a" });
  const requested = event("c", "mcp.action.requested", {
    causationId: "b",
    data: { tool: "task_create" },
  });
  const created = event("d", "task.created", { causationId: "c" });
  const completedAction = event("e", "mcp.action.completed", {
    causationId: "c",
  });
  const processed = event("f", "inbox.processing.completed", {
    causationId: "b",
  });
  const resolved = event("g", "inbox.item.resolved", { causationId: "f" });
  const later = event("h", "task.completed", { correlationId: "request-9" });
  const workflow = [
    resolved,
    processed,
    completedAction,
    created,
    requested,
    started,
    captured,
  ];

  const trace = buildTrace(requested, workflow, [later, created]);

  assert.deepEqual(
    trace.upstream.map((entry) => entry.id),
    ["a", "b"],
  );
  assert.deepEqual(
    trace.downstream.map((entry) => entry.id),
    ["d", "e"],
  );
  assert.deepEqual(
    trace.related.map((entry) => entry.id),
    ["h"],
  );
  assert.equal(trace.roots.length, 1);
  const [root] = trace.roots;
  assert.equal(root.event.id, "a");
  assert.equal(root.relation, "upstream");
  const [startedNode] = root.children;
  assert.deepEqual(
    startedNode.children.map((child) => [child.event.id, child.relation]),
    [
      ["c", "selected"],
      ["f", "other"],
    ],
  );
  assert.equal(trace.externalCauseId, undefined);
});

test("a follow-up capture links the earlier capture both ways", () => {
  // "Buy milk" (its own workflow), then "Actually oat milk", whose
  // processing renamed the item and recorded the follow-up about the milk
  // capture in its own workflow.
  const milk = { type: "inbox_item", id: "milk" };
  const captured = event("m1", "inbox.item.captured", {
    subject: milk,
    correlationId: "milk",
  });
  const resolved = event("m2", "inbox.item.resolved", {
    subject: milk,
    correlationId: "milk",
  });
  const completed = event("o1", "inbox.processing.completed", {
    subject: { type: "processing_attempt", id: "attempt-2" },
    correlationId: "oat",
  });
  const followedUp = event("o2", "inbox.item.followed_up", {
    subject: milk,
    correlationId: "oat",
    causationId: "o1",
    summary: "“Actually oat milk” changed what “Buy milk” did",
  });
  assert.equal(eventLabel(followedUp), "Earlier capture followed up");
  assert.ok(EVENT_TYPES.includes("inbox.item.followed_up"));

  // From the follow-up: what led to it, and the milk capture's history.
  const fromFollowUp = buildTrace(
    followedUp,
    [completed, followedUp],
    [captured, resolved, followedUp],
  );
  assert.deepEqual(
    fromFollowUp.upstream.map((entry) => entry.id),
    ["o1"],
  );
  assert.deepEqual(
    fromFollowUp.related.map((entry) => entry.id),
    ["m1", "m2"],
  );
  // From the milk capture: the follow-up in the other workflow.
  const fromMilk = buildTrace(
    captured,
    [captured, resolved],
    [captured, resolved, followedUp],
  );
  assert.deepEqual(
    fromMilk.related.map((entry) => entry.id),
    ["o2"],
  );
});

test("a cause recorded outside the loaded workflow is reported, not invented", () => {
  const orphan = event("x", "task.updated", { causationId: "missing" });
  const trace = buildTrace(orphan, [], []);
  assert.equal(trace.externalCauseId, "missing");
  assert.deepEqual(trace.upstream, []);
  assert.equal(trace.roots[0].event.id, "x");
});

test("causation cycles cannot loop the trace", () => {
  const first = event("p", "task.updated", { causationId: "q" });
  const second = event("q", "task.updated", { causationId: "p" });
  const trace = buildTrace(first, [first, second], []);
  assert.deepEqual(
    trace.upstream.map((entry) => entry.id),
    ["q"],
  );
});

test("filters are parsed defensively and map to the Activity API query", () => {
  const filters = parseFilters({
    source: "tasks",
    outcome: "exploded",
    fromDate: "2026-10-01",
    toDate: "2026-10-09",
    type: ["task.completed", "ignored"],
    subjectType: "  ",
  });
  assert.deepEqual(filters, {
    source: "tasks",
    fromDate: "2026-10-01",
    toDate: "2026-10-09",
    type: "task.completed",
  });
  assert.deepEqual(listQuery(filters, "UTC", 25), {
    source: "tasks",
    subjectType: undefined,
    subjectId: undefined,
    type: "task.completed",
    outcome: undefined,
    correlationId: undefined,
    from: "2026-10-01T00:00:00.000Z",
    to: "2026-10-10T00:00:00.000Z",
    cursor: undefined,
    limit: 25,
  });
  assert.deepEqual(parseFilters({ fromDate: "yesterday" }), {});
});

test("filter days are the owner's calendar days", () => {
  // Berlin is UTC+2 in October; the inclusive "to" day ends at its midnight.
  const query = listQuery(
    { fromDate: "2026-10-09", toDate: "2026-10-09" },
    "Europe/Berlin",
  );
  assert.equal(query.from, "2026-10-08T22:00:00.000Z");
  assert.equal(query.to, "2026-10-09T22:00:00.000Z");
  // Across the end of summer time the day is 25 hours long.
  const fallBack = listQuery(
    { fromDate: "2026-10-25", toDate: "2026-10-25" },
    "Europe/Berlin",
  );
  assert.equal(fallBack.from, "2026-10-24T22:00:00.000Z");
  assert.equal(fallBack.to, "2026-10-25T23:00:00.000Z");
});

test("event times render in the owner's timezone, not UTC", () => {
  const at = "2026-10-09T21:21:30.679Z";
  assert.equal(eventTime(at, "Europe/Berlin"), "9 Oct 2026, 23:21:30 CEST");
  assert.equal(eventTime(at, "UTC"), "9 Oct 2026, 21:21:30 UTC");
  assert.equal(
    eventTime("2026-12-09T21:21:30Z", "Europe/Berlin"),
    "9 Dec 2026, 22:21:30 CET",
  );
});

/** pkg/activity's Preview: collapse whitespace, then shorten with "…". */
function preview(text: string, limit: number) {
  const runes = Array.from(text.split(/\s+/).filter(Boolean).join(" "));
  return runes.length > limit
    ? `${runes.slice(0, limit - 1).join("")}…`
    : runes.join("");
}

test("a shortened summary expands to the longer text kept in the data", () => {
  const error =
    'planning failed: run Codex CLI: exit status 1: WARNING: proceeding, even though we could not create PATH aliases: Refusing to create helper binaries under temporary dir "/tmp"\nReading additional input from stdin...  OpenAI Codex v0.160.0';
  assert.equal(
    fullSummary({
      summary: `Processing will be retried: ${preview(error, 160)}`,
      data: { attemptId: "a-1", tools: [], errorSummary: error },
    }),
    `Processing will be retried: ${error}`,
  );

  // The shortened part can sit inside the summary, not only at its end.
  const content =
    "Remember to renew the passport before the trip in November and book the appointment at the consulate";
  assert.equal(
    fullSummary({
      summary: `Captured “${preview(content, 80)}” from voice`,
      data: { source: "voice", content: preview(content, 280) },
    }),
    `Captured “${content}” from voice`,
  );
});

test("a summary without more text in its data does not expand", () => {
  assert.equal(
    fullSummary({ summary: "Processing completed", data: {} }),
    undefined,
  );
  // The data has no longer copy of the shortened error.
  assert.equal(
    fullSummary({
      summary: `task_create failed: ${preview("x".repeat(300), 160)}`,
      data: { tool: "task_create" },
    }),
    undefined,
  );
  // An ellipsis the user typed is not a shortening.
  const typed = "Wait… call the dentist about the appointment tomorrow";
  assert.equal(
    fullSummary({
      summary: `Captured “${typed}” from manual`,
      data: { source: "manual", content: typed },
    }),
    undefined,
  );
});

test("links keep the active filters and apply overrides", () => {
  const filters = { source: "inbox", cursor: "abc" };
  assert.equal(
    activityHref(filters, { cursor: undefined, event: "e1" }),
    "/activity?source=inbox&event=e1",
  );
  assert.equal(activityHref({}), "/activity");
});

test("only a UUID can select an event", () => {
  assert.equal(
    selectedEventId({ event: "9C3B6F3E-1D1F-4E55-9C59-3A8A0B6A7E10" }),
    "9c3b6f3e-1d1f-4e55-9c59-3a8a0b6a7e10",
  );
  assert.equal(selectedEventId({ event: "../../etc" }), undefined);
});

test("the agent's domain says what it records", () => {
  assert.equal(sourceLabel("mcp"), "Agent changes");
  assert.match(sourceDescription("mcp")!, /through MCP/);
  assert.match(sourceDescription("mcp")!, /Human Review requests.*Inbox/);
  assert.equal(sourceDescription("tasks"), undefined);
  assert.equal(sourceDescription(undefined), undefined);
});

test("an empty list explains itself for the active filters", () => {
  assert.deepEqual(emptyState({}), { message: "No activity recorded yet." });
  assert.deepEqual(emptyState({ source: "tasks" }), {
    message: "No activity matches these filters.",
  });
  // The agent's domain is empty until the agent changes something; its
  // decisions are under Inbox, in the same range.
  assert.deepEqual(
    emptyState({ source: "mcp", fromDate: "2026-10-01", cursor: "abc" }),
    {
      message: "The agent hasn't changed anything in this range.",
      link: {
        label: "Show Inbox activity",
        href: "/activity?source=inbox&fromDate=2026-10-01",
      },
    },
  );
  assert.equal(
    emptyState({ source: "mcp" }).message,
    "The agent hasn't changed anything yet.",
  );
  assert.deepEqual(emptyState({ source: "mcp", outcome: "failed" }), {
    message: "No activity matches these filters.",
  });
});

test("processor work reads as the Inbox processor", () => {
  assert.equal(
    actorLabel({ kind: "agent", name: "codex-inbox-processor" }),
    "Inbox processor (Codex)",
  );
  assert.equal(actorLabel({ kind: "agent", name: "mcp" }), "Agent via MCP");
  assert.equal(actorLabel({ kind: "user" }), "You");
});

test("development tools are named apart from the owner and the agent", () => {
  assert.equal(
    actorLabel({ kind: "dev", name: "claude-code" }),
    "Developer tool (claude-code)",
  );
  assert.equal(actorLabel({ kind: "dev" }), "Developer tool");
  assert.equal(isDevActor({ kind: "dev", name: "claude-code" }), true);
  assert.equal(isDevActor({ kind: "user" }), false);
  assert.equal(isDevActor({ kind: "agent", name: "mcp" }), false);
});

test("agent actions name their tool", () => {
  assert.equal(
    eventLabel({ type: "mcp.action.requested", data: { tool: "task_create" } }),
    "Agent action requested: task_create",
  );
  assert.equal(
    eventLabel({ type: "calendar.event.moved", data: {} }),
    "Calendar event moved",
  );
});

test("calendar event changes read with the event's title", () => {
  for (const [type, label] of [
    ["calendar.event.created", "Event created: Dentist"],
    ["calendar.event.updated", "Event updated: Dentist"],
    ["calendar.event.rescheduled", "Event rescheduled: Dentist"],
    ["calendar.event.cancelled", "Event cancelled: Dentist"],
    ["calendar.event.restored", "Event restored: Dentist"],
    ["calendar.event.deleted", "Event deleted: Dentist"],
  ]) {
    assert.equal(eventLabel({ type, data: { title: "Dentist" } }), label);
    // The Event filter lists the plain label.
    assert.ok(EVENT_TYPES.includes(type));
    assert.equal(eventLabel({ type, data: {} }), label.split(":")[0]);
  }
});

test("a deleted Task reads with its title, other Task changes as before", () => {
  assert.equal(
    eventLabel({ type: "task.deleted", data: { title: "Pay rent" } }),
    "Task deleted: Pay rent",
  );
  // The Event filter lists the plain label.
  assert.ok(EVENT_TYPES.includes("task.deleted"));
  assert.equal(eventLabel({ type: "task.deleted", data: {} }), "Task deleted");
  assert.equal(
    eventLabel({ type: "task.completed", data: { title: "Pay rent" } }),
    "Task completed",
  );
});

test("a source refresh reads as what it changed", () => {
  const refreshed = (data: Record<string, unknown>) =>
    eventLabel({
      type: "calendar.source.refreshed",
      data: { sourceName: "ZAW", ...data },
    });
  assert.equal(
    refreshed({
      outcome: "imported",
      counts: { inserted: 3, updated: 0, removed: 1, restored: 0 },
    }),
    "Source refreshed: ZAW — 3 added, 1 cancelled",
  );
  assert.equal(
    refreshed({
      outcome: "imported",
      counts: { inserted: 0, updated: 2, removed: 0, restored: 1 },
    }),
    "Source refreshed: ZAW — 2 changed, 1 restored",
  );
  assert.equal(
    refreshed({ outcome: "imported", counts: { inserted: 0, unchanged: 21 } }),
    "Source refreshed: ZAW — no changes",
  );
  assert.equal(
    refreshed({ outcome: "not_modified" }),
    "Source refreshed: ZAW — unchanged",
  );
  assert.equal(refreshChanges(undefined), "no changes");
  assert.equal(
    eventLabel({
      type: "calendar.source.refresh_failed",
      data: { sourceName: "ZAW", error: "the feed answered HTTP 500" },
    }),
    "Source refresh failed: ZAW",
  );
  assert.equal(
    eventLabel({ type: "calendar.source.refreshed", data: {} }),
    "Source refreshed",
  );
});

test("the Calendar domain, its entities, and feed actors are named", () => {
  assert.equal(sourceLabel("calendar"), "Calendar");
  assert.match(sourceDescription("calendar") ?? "", /Google Calendar/);
  // Feed refreshes come from the calendar-feeds integration.
  assert.equal(sourceLabel("calendar-feeds"), "Calendar feeds");
  assert.match(
    sourceDescription("calendar-feeds") ?? "",
    /subscribed calendar feed/,
  );
  assert.equal(
    actorLabel({ kind: "system", name: "ics-zaw" }),
    "Calendar feed (zaw)",
  );
  assert.equal(
    actorLabel({ kind: "system", name: "ics-source" }),
    "Calendar feed",
  );
  assert.equal(
    actorLabel({ kind: "dev", name: "e2e" }),
    "Developer tool (e2e)",
  );
  assert.equal(actorLabel({ kind: "user" }), "You");
});

test("calendar entries link to the event's day in the owner's timezone", () => {
  const entry = (
    type: string,
    data: Record<string, unknown>,
    subjectType = "calendar_event",
  ) => ({ type, data, subject: { type: subjectType, id: "event-1" } });
  // 23:30 UTC on 16 Oct is already 17 Oct in Berlin.
  const late = entry("calendar.event.created", {
    startAt: "2026-10-16T23:30:00Z",
  });
  assert.equal(
    calendarHref(late, "Europe/Berlin"),
    "/calendar?date=2026-10-17",
  );
  assert.equal(calendarHref(late, "UTC"), "/calendar?date=2026-10-16");
  // All-day dates are the owner's days already.
  assert.equal(
    calendarHref(
      entry("calendar.event.rescheduled", {
        allDay: true,
        startDate: "2026-10-20",
      }),
      "America/New_York",
    ),
    "/calendar?date=2026-10-20",
  );
  assert.equal(
    calendarHref(entry("calendar.event.updated", {}), "UTC"),
    "/calendar",
  );
  // A deleted event is no longer in the Calendar.
  assert.equal(
    calendarHref(
      entry("calendar.event.deleted", { startAt: "2026-10-16T09:00:00Z" }),
      "UTC",
    ),
    undefined,
  );
  assert.equal(
    calendarHref(
      entry("calendar.source.refreshed", {}, "calendar_source"),
      "UTC",
    ),
    "/calendar",
  );
  assert.equal(
    calendarHref(entry("task.created", {}, "task"), "UTC"),
    undefined,
  );
});

test("notifications read with their title and the channel used", () => {
  assert.equal(sourceLabel("notifications"), "Notifications");
  assert.equal(subjectLabel("notification"), "Notification");
  assert.equal(
    eventLabel({
      type: "notification.created",
      data: { title: "Inbox needs review" },
    }),
    "Notification created: Inbox needs review",
  );
  assert.equal(
    eventLabel({
      type: "notification.delivered",
      data: { title: "Inbox needs review", channel: "ntfy" },
    }),
    "Notification delivered: Inbox needs review (ntfy)",
  );
  assert.equal(
    eventLabel({
      type: "notification.delivery_failed",
      data: { title: "Inbox needs review", channel: "ntfy" },
    }),
    "Notification delivery failed: Inbox needs review (ntfy)",
  );
  for (const type of [
    "notification.created",
    "notification.delivered",
    "notification.delivery_failed",
  ]) {
    assert.ok(EVENT_TYPES.includes(type));
  }
  assert.equal(
    eventLabel({ type: "notification.delivery_failed", data: {} }),
    "Notification delivery failed",
  );
});
