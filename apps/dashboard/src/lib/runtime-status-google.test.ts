import assert from "node:assert/strict";
import test from "node:test";

import type { GoogleConnection } from "./google-calendar";
import {
  GOOGLE_RECONNECT,
  googlePart,
  statusAlerts,
  toRuntimeStatus,
  type RuntimeSignals,
} from "./runtime-status";

const now = new Date("2026-10-10T12:00:00Z");

const signals = (
  googleConnection: GoogleConnection | null | undefined,
): RuntimeSignals => ({
  processor: null,
  inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
  voice: null,
  calendarSources: [],
  googleConnection,
  services: [],
});

const google = (
  overrides: Partial<GoogleConnection> = {},
): GoogleConnection => ({
  configured: true,
  status: "connected",
  accountEmail: "owner@example.com",
  scopes: [],
  missingScopes: [],
  connectedAt: "2026-10-10T08:00:00Z",
  lastError: null,
  ...overrides,
});

const calendarCheck = (connection: GoogleConnection | null | undefined) =>
  toRuntimeStatus(signals(connection), now, "Europe/Berlin").checks.find(
    (check) => check.id === "calendar",
  )!;

test("a connected Google account is a healthy part of the Calendar check", () => {
  const check = calendarCheck(google());
  assert.equal(check.state, "ok");
  assert.equal(check.summary, "No calendar feeds are subscribed.");
  const part = check.parts?.find((entry) => entry.id === "google");
  assert.equal(part?.title, "Google Calendar");
  assert.equal(part?.state, "ok");
  assert.equal(part?.summary, "Connected as owner@example.com.");
  assert.deepEqual(part?.facts, [
    { label: "Account", value: "owner@example.com" },
    { label: "Connected", value: "10 Oct 2026, 10:00 CEST" },
  ]);
});

test("missing permissions show as a fact", () => {
  const part = googlePart(
    google({
      missingScopes: ["https://www.googleapis.com/auth/calendar.app.created"],
    }),
    "Europe/Berlin",
  );
  assert.equal(part?.state, "ok");
  assert.deepEqual(part?.facts.at(-1), {
    label: "Not granted",
    value: "manage the calendars Life OS creates",
  });
});

test("not connected is neutral; not set up shows nothing", () => {
  const notConnected = calendarCheck(
    google({ status: "not_connected", accountEmail: null, connectedAt: null }),
  );
  assert.equal(notConnected.state, "ok");
  assert.equal(
    notConnected.parts?.[0].summary,
    "Not connected. Connect Google in Calendar to sync it.",
  );
  const unset = calendarCheck(
    google({
      configured: false,
      status: "not_connected",
      accountEmail: null,
    }),
  );
  assert.equal(unset.parts, undefined);
  assert.equal(calendarCheck(null).parts, undefined);
  assert.equal(calendarCheck(undefined).parts, undefined);
});

test("expired or refused access needs a reconnect", () => {
  for (const status of ["expired", "error"] as const) {
    const lastError =
      "Google refused the request (invalid_grant: Token has been expired or revoked.): reconnect Google in Calendar.";
    const status_ = toRuntimeStatus(
      signals(google({ status, lastError })),
      now,
      "Europe/Berlin",
    );
    const check = status_.checks.find((entry) => entry.id === "calendar")!;
    assert.equal(check.state, "warning");
    assert.equal(check.action, GOOGLE_RECONNECT);
    assert.match(
      check.summary,
      status === "expired"
        ? /Google access for owner@example.com expired or was revoked/
        : /Google refused the connection for owner@example.com/,
    );
    assert.equal(check.parts?.[0].detail, lastError);
    assert.ok(statusAlerts(status_).some((alert) => alert.id === "calendar"));
  }
});

test("a connection the server can no longer use needs its settings back", () => {
  const check = calendarCheck(google({ configured: false }));
  assert.equal(check.state, "warning");
  assert.match(check.action ?? "", /LIFE_OS_GOOGLE_/);
});
