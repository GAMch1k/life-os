import assert from "node:assert/strict";
import test from "node:test";

import { toRuntimeStatus } from "./runtime-status";
import { runtimeStatusResponse } from "./status-response";

const status = toRuntimeStatus(
  {
    processor: null,
    inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
    voice: {
      queuedCount: 0,
      processingCount: 0,
      retryableCount: 0,
      workerStalled: false,
      whisperReady: true,
    },
    services: [
      { name: "Tasks", ready: false },
      { name: "Monitor", ready: true },
    ],
  },
  new Date("2026-10-10T12:00:00Z"),
  "Europe/Berlin",
);

test("GET /api/status returns the Status page's checks as uncached JSON", async () => {
  const response = runtimeStatusResponse(status);

  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.match(response.headers.get("content-type") ?? "", /application\/json/);
  const body = await response.json();
  assert.deepEqual(body, JSON.parse(JSON.stringify(status)));
  assert.equal(body.checkedAt, "2026-10-10T12:00:00.000Z");
  assert.equal(body.overall, "problem");
});

test("each check carries what the monitor notifies with", async () => {
  const body = await runtimeStatusResponse(status).json();
  const byId = Object.fromEntries(
    body.checks.map((check: { id: string }) => [check.id, check]),
  );

  assert.deepEqual(Object.keys(byId), [
    "processing",
    "inbox",
    "voice",
    "services",
  ]);
  assert.equal(byId.processing.title, "Automatic processing");
  assert.equal(byId.processing.state, "problem");
  assert.equal(byId.processing.summary, "The Inbox processor is not running.");
  assert.match(byId.processing.action, /Start the processor service/);
  assert.equal(byId.services.state, "problem");
  assert.equal(byId.services.summary, "Tasks is not ready.");
  assert.equal(byId.inbox.state, "ok");
  assert.equal(byId.inbox.action, undefined);
});
