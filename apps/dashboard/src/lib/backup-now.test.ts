import assert from "node:assert/strict";
import test from "node:test";

import type { components } from "./api/generated/backup";
import {
  afterPoll,
  progressMessage,
  resultMessage,
  startError,
} from "./backup-now";

type BackupStatus = components["schemas"]["BackupStatus"];
type BackupRun = components["schemas"]["BackupRun"];

const progress = {
  id: "20261011T120000Z",
  trigger: "manual" as const,
  scope: "all" as const,
  actor: "user",
  startedAt: "2026-10-11T12:00:00Z",
  databases: ["finance_db", "tasks_db", "food_db"],
  completed: 1,
  current: "tasks_db",
};

const finished = (overrides: Partial<BackupRun> = {}): BackupRun => ({
  id: "20261011T120000Z",
  trigger: "manual",
  scope: "all",
  result: "succeeded",
  startedAt: "2026-10-11T12:00:00Z",
  finishedAt: "2026-10-11T12:00:04Z",
  durationSeconds: 4.24,
  totalBytes: 3_000,
  databases: [
    {
      service: "finance",
      name: "finance_db",
      result: "succeeded",
      bytes: 1_000,
      tables: 2,
      rows: 3,
    },
    {
      service: "tasks",
      name: "tasks_db",
      result: "succeeded",
      bytes: 2_000,
      tables: 2,
      rows: 3,
    },
  ],
  ...overrides,
});

const status = (overrides: Partial<BackupStatus> = {}): BackupStatus => ({
  schedule: {
    time: "03:15",
    timeZone: "Europe/Berlin",
    nextRunAt: "2026-10-12T01:15:00Z",
    nextTrigger: "schedule",
    restoreCheckDay: "sunday",
  },
  retention: { daily: 7, weekly: 4, monthly: 12, failedDays: 3 },
  storage: { runs: 1, totalBytes: 3_000, freeBytes: 1e11, sizeBytes: 2e11 },
  databases: ["finance_db", "tasks_db"],
  ...overrides,
});

test("progress and results read as sentences", () => {
  assert.equal(progressMessage(progress), "Backing up tasks_db (1 of 3 done)…");
  assert.equal(
    progressMessage({ ...progress, current: undefined, completed: 0 }),
    "Backing up (0 of 3 done)…",
  );
  assert.deepEqual(resultMessage(finished()), {
    ok: true,
    message: "Backed up 2 databases in 4.2 s.",
  });
  assert.deepEqual(
    resultMessage(
      finished({ result: "failed", error: "1 of 2 databases failed" }),
    ),
    { ok: false, message: "The backup failed: 1 of 2 databases failed." },
  );
  assert.deepEqual(resultMessage(finished({ result: "interrupted" })), {
    ok: false,
    message: "The backup was interrupted.",
  });
});

test("following a backup until it ends", () => {
  assert.deepEqual(afterPoll(status({ running: progress }), progress.id), {
    kind: "running",
    message: "Backing up tasks_db (1 of 3 done)…",
  });
  assert.deepEqual(afterPoll(status({ lastRun: finished() }), progress.id), {
    kind: "done",
    ok: true,
    message: "Backed up 2 databases in 4.2 s.",
  });
  // After a 409 it follows whatever runs, and ends when nothing does.
  assert.equal(
    afterPoll(status({ running: progress }), undefined).kind,
    "running",
  );
  assert.deepEqual(
    afterPoll(
      status({
        runningRestoreCheck: {
          id: "20261011T120100Z",
          runId: "20261011T011500Z",
          trigger: "schedule",
          startedAt: "2026-10-11T12:01:00Z",
          databases: ["finance_db"],
          completed: 0,
        },
      }),
      undefined,
    ),
    {
      kind: "running",
      message: "A restore check is running; a backup can start when it ends.",
    },
  );
  assert.deepEqual(
    afterPoll(
      status({ lastRun: finished({ id: "20261010T011500Z" }) }),
      undefined,
    ),
    {
      kind: "done",
      ok: true,
      message: "Finished; the card shows the result.",
    },
  );
});

test("why a backup could not start", () => {
  assert.equal(
    startError(undefined),
    "The backup service could not be reached.",
  );
  assert.equal(
    startError(503, "Service unavailable"),
    "The backup service could not be reached.",
  );
  assert.equal(
    startError(400, "Writes must say who makes them"),
    "Writes must say who makes them",
  );
  assert.equal(startError(404), "The backup could not start (404).");
});
