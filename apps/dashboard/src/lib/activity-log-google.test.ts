import assert from "node:assert/strict";
import test from "node:test";

import {
  actorLabel,
  calendarHref,
  eventLabel,
  sourceDescription,
  subjectLabel,
} from "./activity-log";

test("Google connection events are labelled with the account", () => {
  for (const [type, label] of [
    ["calendar.google.connected", "Google Calendar connected"],
    ["calendar.google.disconnected", "Google Calendar disconnected"],
    ["calendar.google.expired", "Google Calendar access expired"],
  ]) {
    assert.equal(
      eventLabel({ type, data: { accountEmail: "owner@example.com" } }),
      `${label}: owner@example.com`,
    );
    assert.equal(eventLabel({ type, data: {} }), label);
  }
  assert.equal(subjectLabel("google_connection"), "Google connection");
  assert.equal(
    calendarHref(
      {
        type: "calendar.google.expired",
        subject: { type: "google_connection", id: "x" },
        data: {},
      },
      "Europe/Berlin",
    ),
    "/calendar",
  );
  assert.match(sourceDescription("calendar") ?? "", /Google Calendar/);
});

test("Google calendar syncs are labelled like feed refreshes, with their own actor", () => {
  assert.equal(
    eventLabel({
      type: "calendar.source.refreshed",
      data: {
        sourceName: "Family",
        sourceKind: "google",
        outcome: "imported",
        counts: { inserted: 2, updated: 1, removed: 0, restored: 0 },
      },
    }),
    "Source refreshed: Family — 2 added, 1 changed",
  );
  assert.equal(
    eventLabel({
      type: "calendar.source.removed",
      data: { sourceName: "Family", removedEvents: 12 },
    }),
    "Source removed: Family — 12 events removed",
  );
  assert.equal(
    eventLabel({ type: "calendar.source.removed", data: {} }),
    "Source removed",
  );
  assert.equal(
    actorLabel({ kind: "system", name: "google-calendar" }),
    "Google Calendar sync",
  );
});
