import assert from "node:assert/strict";
import test from "node:test";

import type { ActivityEvent } from "./activity-log";
import {
  diagnosticFields,
  diagnosticsText,
  eventDataText,
} from "./event-diagnostics";

const event: ActivityEvent = {
  id: "0f1e2d3c-4b5a-4968-8776-655443322110",
  type: "task.created",
  source: "tasks",
  subject: { type: "task", id: "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d" },
  correlationId: "corr-42",
  causationId: "11111111-2222-4333-8444-555555555555",
  actor: { kind: "user", name: "owner" },
  outcome: "succeeded",
  summary: "Created  the task\n“Buy milk”",
  data: { title: "Buy milk", tags: ["shop"], due: null },
  occurredAt: "2026-10-16T18:45:10.123Z",
  recordedAt: "2026-10-16T18:45:10.456Z",
};

test("the whole diagnostics: every field on its own line, then the data as JSON", () => {
  assert.equal(
    diagnosticsText(event),
    [
      "Event id: 0f1e2d3c-4b5a-4968-8776-655443322110",
      "Type: task.created",
      "Source: tasks",
      "Subject: task/7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d",
      "Correlation: corr-42",
      "Caused by: 11111111-2222-4333-8444-555555555555",
      "Actor: user:owner",
      "Occurred: 2026-10-16T18:45:10.123Z",
      "Recorded: 2026-10-16T18:45:10.456Z",
      "Outcome: succeeded",
      "Summary: Created the task “Buy milk”",
      "",
      "Event data:",
      "{",
      '  "title": "Buy milk",',
      '  "tags": [',
      '    "shop"',
      "  ],",
      '  "due": null',
      "}",
    ].join("\n"),
  );
});

test("the fields read as the view shows them, without a cause or actor name", () => {
  const started: ActivityEvent = {
    ...event,
    causationId: null,
    actor: { kind: "system" },
  };
  const fields = Object.fromEntries(diagnosticFields(started));
  assert.equal(fields["Caused by"], "—");
  assert.equal(fields.Actor, "system");
  assert.deepEqual(Object.keys(fields), [
    "Event id",
    "Type",
    "Source",
    "Subject",
    "Correlation",
    "Caused by",
    "Actor",
    "Occurred",
    "Recorded",
  ]);
  const text = diagnosticsText(started);
  assert.match(text, /^Caused by: —$/m);
  assert.match(text, /^Actor: system$/m);
});

test("the data alone is the JSON the view shows", () => {
  assert.equal(eventDataText({ data: {} }), "{}");
  assert.equal(eventDataText(event), JSON.stringify(event.data, null, 2));
  assert.ok(diagnosticsText(event).endsWith(eventDataText(event)));
});
