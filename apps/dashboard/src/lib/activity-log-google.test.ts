import assert from "node:assert/strict";
import test from "node:test";

import {
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
