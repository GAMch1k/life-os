import assert from "node:assert/strict";
import test from "node:test";

import {
  activityHref,
  actorLabel,
  buildTrace,
  emptyState,
  eventLabel,
  eventTime,
  listQuery,
  parseFilters,
  selectedEventId,
  sourceDescription,
  sourceLabel,
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
