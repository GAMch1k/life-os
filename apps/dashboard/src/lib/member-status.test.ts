import assert from "node:assert/strict";
import test from "node:test";

import { isMemberStatus, memberLine, memberStatus } from "./member-status";
import { toRuntimeStatus } from "./runtime-status";

const at = new Date("2026-11-14T12:00:00Z");

test("everything works: the line, and no checks", () => {
  const status = memberStatus(
    toRuntimeStatus(
      {
        inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
        services: [{ name: "Tasks", ready: true }],
      },
      at,
      "Europe/Berlin",
    ),
  );
  assert.deepEqual(status, {
    view: "member",
    checkedAt: "2026-11-14T12:00:00.000Z",
    timeZone: "Europe/Berlin",
    overall: "ok",
    problems: [],
    checks: [],
  });
  assert.equal(memberLine(status.problems), "Everything works.");
  assert.equal(isMemberStatus(status), true);
});

test("a module with a problem is named once; the server's own checks are not", () => {
  const full = toRuntimeStatus(
    {
      processor: null,
      inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
      services: [
        { name: "Calendar", ready: false },
        { name: "Calendar feeds", ready: false },
        { name: "Google Calendar", ready: true },
        { name: "Host health", ready: false },
        { name: "Backup", ready: false },
        { name: "Monitor", ready: false },
        { name: "Recipe import", ready: false },
      ],
    },
    at,
  );
  assert.equal(isMemberStatus(full), false);
  const status = memberStatus(full, "second_factor_required");
  // The processor could not be read: AI processing has a problem.
  assert.deepEqual(status.problems, ["AI processing", "Calendar", "Food"]);
  assert.equal(status.overall, "problem");
  assert.equal(status.reason, "second_factor_required");
  assert.equal(
    memberLine(status.problems),
    "AI processing, Calendar, and Food have a problem.",
  );
  const text = JSON.stringify(status);
  for (const detail of ["Host health", "Backup", "Monitor", "not ready"]) {
    assert.ok(!text.includes(detail), `${detail} reached a member`);
  }
});

test("a warning leaves the line at everything works", () => {
  const full = toRuntimeStatus(
    {
      inbox: { pendingCount: 3, retryingCount: 2, humanReviewCount: 0 },
      services: [{ name: "Inbox", ready: true }],
    },
    at,
  );
  assert.equal(memberStatus(full).overall, "ok");
});

test("the line names one, two, or more modules", () => {
  assert.equal(memberLine(["Calendar"]), "Calendar has a problem.");
  assert.equal(
    memberLine(["Calendar", "Food"]),
    "Calendar and Food have a problem.",
  );
});
