import assert from "node:assert/strict";
import test from "node:test";

import type { GoogleCalendar, GoogleConnection } from "./google-calendar";
import {
  GOOGLE_RECONNECT,
  GOOGLE_SYNC_RETRY,
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

const synced = (
  summary: string,
  sync: Partial<GoogleCalendar["sync"]> = {},
  syncEnabled = true,
): GoogleCalendar => ({
  id: `id-${summary}`,
  googleCalendarId: `${summary.toLowerCase()}@example.com`,
  summary,
  color: null,
  accessRole: "owner",
  primary: false,
  syncEnabled,
  sync: {
    state: syncEnabled ? "synced" : "off",
    consecutiveFailures: 0,
    syncing: false,
    lastSyncedAt: syncEnabled ? "2026-10-10T11:58:00Z" : null,
    ...sync,
  },
  createdAt: "2026-10-10T08:00:00Z",
  updatedAt: "2026-10-10T08:00:00Z",
});

test("the Google part shows what syncs and when it last did", () => {
  const part = googlePart(google(), "Europe/Berlin", [
    synced("Owner", { lastSyncedAt: "2026-10-10T11:56:00Z" }),
    synced("Family"),
    synced("Holidays", {}, false),
  ]);
  assert.equal(part?.state, "ok");
  assert.equal(part?.summary, "Connected as owner@example.com.");
  assert.deepEqual(part?.facts.slice(2), [
    { label: "Syncing", value: "Owner, Family" },
    { label: "Last sync", value: "10 Oct 2026, 13:58 CEST" },
  ]);
  const none = googlePart(google(), "Europe/Berlin", [
    synced("Holidays", {}, false),
  ]);
  assert.deepEqual(none?.facts.slice(2), [
    { label: "Syncing", value: "no calendar chosen" },
  ]);
  const first = googlePart(google(), "Europe/Berlin", [
    synced("Owner", { state: "pending", lastSyncedAt: null }),
  ]);
  assert.deepEqual(first?.facts.at(-1), {
    label: "Last sync",
    value: "not yet",
  });
});

test("a failed sync is retried quietly; one that keeps failing needs attention", () => {
  const lastError = "Google is unavailable: Calendar API returned HTTP 503";
  const once = googlePart(google(), "Europe/Berlin", [
    synced("Owner", { state: "failing", consecutiveFailures: 1, lastError }),
  ]);
  assert.equal(once?.state, "ok");
  assert.equal(
    once?.summary,
    "Connected as owner@example.com. The last sync of Owner failed; it is retried automatically.",
  );
  assert.equal(once?.detail, lastError);

  const status = toRuntimeStatus(
    {
      ...signals(google()),
      googleCalendars: [
        synced("Owner", {
          state: "failing",
          consecutiveFailures: 4,
          lastError,
        }),
        synced("Family"),
      ],
    },
    now,
    "Europe/Berlin",
  );
  const check = status.checks.find((entry) => entry.id === "calendar")!;
  assert.equal(check.state, "warning");
  assert.equal(
    check.summary,
    "Syncing Owner keeps failing: 4 attempts in a row.",
  );
  assert.equal(check.action, GOOGLE_SYNC_RETRY);
  assert.equal(check.parts?.[0].detail, lastError);
  assert.ok(statusAlerts(status).some((alert) => alert.id === "calendar"));
});
