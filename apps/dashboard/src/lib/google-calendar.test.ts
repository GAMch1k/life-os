import assert from "node:assert/strict";
import test from "node:test";

import {
  calendarDetail,
  callbackStep,
  connectFailure,
  exportDetail,
  type GoogleCalendar,
  type GoogleConnection,
  googleState,
  missingPermissions,
  replaceCalendar,
  syncDetail,
  syncSummary,
  withoutGoogleParam,
} from "./google-calendar";

const connection = (
  overrides: Partial<GoogleConnection> = {},
): GoogleConnection => ({
  configured: true,
  status: "connected",
  accountEmail: "owner@example.com",
  scopes: [
    "https://www.googleapis.com/auth/calendar.calendarlist.readonly",
    "https://www.googleapis.com/auth/calendar.events",
    "https://www.googleapis.com/auth/calendar.app.created",
  ],
  missingScopes: [],
  connectedAt: "2026-10-10T10:00:00Z",
  lastError: null,
  exportEnabled: true,
  export: { pending: 0, failed: 0 },
  ...overrides,
});

const calendar = (
  summary: string,
  overrides: Partial<GoogleCalendar> = {},
): GoogleCalendar => ({
  id: `id-${summary}`,
  googleCalendarId: `${summary.toLowerCase()}@group.calendar.google.com`,
  summary,
  color: "#9fe1e7",
  accessRole: "writer",
  primary: false,
  syncEnabled: false,
  lifeOs: false,
  sync: { state: "off", consecutiveFailures: 0, syncing: false },
  createdAt: "2026-10-10T10:00:00Z",
  updatedAt: "2026-10-10T10:00:00Z",
  ...overrides,
});

test("the dialog state follows configuration first, then the status", () => {
  assert.equal(googleState(connection()), "connected");
  assert.equal(googleState(connection({ status: "expired" })), "expired");
  assert.equal(
    googleState(connection({ configured: false, status: "connected" })),
    "not_configured",
  );
  assert.equal(
    googleState(connection({ status: "not_connected", accountEmail: null })),
    "not_connected",
  );
});

test("missing permissions are named in plain words", () => {
  assert.deepEqual(missingPermissions(connection()), []);
  assert.deepEqual(
    missingPermissions(
      connection({
        missingScopes: [
          "https://www.googleapis.com/auth/calendar.events",
          "https://example.test/unknown",
        ],
      }),
    ),
    ["see and edit events in your calendars", "https://example.test/unknown"],
  );
});

test("calendars are described and counted", () => {
  assert.equal(
    calendarDetail(calendar("Owner", { primary: true, accessRole: "owner" })),
    "Primary · Owner",
  );
  assert.equal(
    calendarDetail(calendar("Holidays", { accessRole: "reader" })),
    "Read only",
  );
  assert.equal(
    calendarDetail(calendar("Busy", { accessRole: "freeBusyReader" })),
    "Free/busy only",
  );
  assert.equal(syncSummary([]), "Google lists no calendars.");
  assert.equal(
    syncSummary([calendar("A"), calendar("B")]),
    "No calendar is chosen to sync.",
  );
  assert.equal(
    syncSummary([calendar("A", { syncEnabled: true }), calendar("B")]),
    "1 of 2 calendars chosen to sync.",
  );
  const list = [calendar("A"), calendar("B")];
  const updated = replaceCalendar(list, { ...list[1], syncEnabled: true });
  assert.deepEqual(
    updated.map((item) => item.syncEnabled),
    [false, true],
  );
  assert.equal(list[1].syncEnabled, false);
});

test("the callback exchanges a code with its state, or explains why not", () => {
  assert.deepEqual(
    callbackStep({ code: "4/0Abc", state: "s1", scope: "x" } as never),
    {
      kind: "exchange",
      code: "4/0Abc",
      state: "s1",
    },
  );
  assert.deepEqual(callbackStep({ code: ["4/0Abc", "other"], state: ["s1"] }), {
    kind: "exchange",
    code: "4/0Abc",
    state: "s1",
  });
  const denied = callbackStep({ error: "access_denied", state: "s1" });
  assert.equal(denied.kind, "error");
  assert.match(
    (denied as { message: string }).message,
    /did not allow Life OS/,
  );
  const other = callbackStep({ error: "server_error" });
  assert.match((other as { message: string }).message, /\(server_error\)/);
  for (const query of [
    {},
    { code: "c" },
    { state: "s" },
    { code: "", state: "s" },
  ]) {
    const step = callbackStep(query);
    assert.equal(step.kind, "error");
    assert.match((step as { message: string }).message, /Connect Google/);
  }
});

test("connection failures say what to do next", () => {
  const body = (code: string, message = "server message") => ({
    error: { code, message },
  });
  assert.match(
    connectFailure(400, body("invalid_state")).message,
    /expired or was already used/,
  );
  assert.match(
    connectFailure(400, body("google_code_rejected")).message,
    /did not accept the sign-in/,
  );
  assert.match(
    connectFailure(403, body("google_scope_missing")).message,
    /allow every permission/,
  );
  const unconfigured = connectFailure(503, body("google_not_configured"));
  assert.match(unconfigured.message, /LIFE_OS_GOOGLE_CLIENT_ID/);
  assert.equal(unconfigured.retry, false);
  assert.match(
    connectFailure(502, body("google_unavailable")).message,
    /could not be reached/,
  );
  assert.equal(
    connectFailure(409, body("google_expired", "reconnect Google in Calendar"))
      .message,
    "Reconnect Google in Calendar.",
  );
  assert.equal(
    connectFailure(503, { error: { code: "service_unavailable" } }).message,
    "Calendar is unavailable right now. Try again.",
  );
  assert.equal(
    connectFailure(400, undefined).message,
    "Google could not be connected.",
  );
});

test("the connected marker leaves the rest of the query alone", () => {
  assert.equal(withoutGoogleParam("?google=connected"), "");
  assert.equal(
    withoutGoogleParam("?view=week&google=connected&date=2026-10-14"),
    "?view=week&date=2026-10-14",
  );
  assert.equal(withoutGoogleParam(""), "");
});

test("a chosen calendar says how its sync is going", () => {
  assert.equal(syncDetail(calendar("Off"), "Europe/Berlin"), undefined);
  const counts = {
    feedEvents: 43,
    imported: 42,
    inserted: 0,
    updated: 1,
    removed: 0,
    restored: 0,
    unchanged: 41,
  };
  assert.deepEqual(
    syncDetail(
      calendar("Work", {
        syncEnabled: true,
        sync: { state: "pending", consecutiveFailures: 0, syncing: true },
      }),
      "Europe/Berlin",
    ),
    { text: "Waiting for its first sync…", failing: false },
  );
  assert.deepEqual(
    syncDetail(
      calendar("Work", {
        syncEnabled: true,
        sync: {
          state: "synced",
          consecutiveFailures: 0,
          syncing: false,
          lastSyncedAt: "2026-10-10T10:05:00Z",
          lastCounts: counts,
        },
      }),
      "Europe/Berlin",
    ),
    { text: "Synced 10 Oct, 12:05 CEST · 42 events", failing: false },
  );
  assert.deepEqual(
    syncDetail(
      calendar("Work", {
        syncEnabled: true,
        sync: {
          state: "failing",
          consecutiveFailures: 2,
          syncing: false,
          lastSyncedAt: "2026-10-10T10:05:00Z",
          lastError: "Google is unavailable: Calendar API returned HTTP 503",
          lastCounts: counts,
        },
      }),
      "Europe/Berlin",
    ),
    {
      text: "Sync failed: Google is unavailable: Calendar API returned HTTP 503. Retrying automatically. Last synced 10 Oct, 12:05 CEST.",
      failing: true,
    },
  );
});

test("the Life OS calendar is named as Life OS's", () => {
  assert.equal(
    calendarDetail(calendar("Life OS", { accessRole: "owner", lifeOs: true })),
    "Created by Life OS · Owner",
  );
});

test("the export line says how exporting Life OS events goes", () => {
  const lifeOsCalendar = {
    googleCalendarId: "lifeos@group.calendar.google.com",
    summary: "Life OS",
    calendarId: "c",
  };
  assert.deepEqual(
    exportDetail(connection({ exportEnabled: false }), "Europe/Berlin"),
    { text: "Off: events created in Life OS stay in Life OS.", failing: false },
  );
  assert.match(
    exportDetail(connection(), "Europe/Berlin").text,
    /creates its “Life OS” calendar/,
  );
  assert.deepEqual(
    exportDetail(
      connection({
        export: {
          pending: 0,
          failed: 0,
          lastError:
            "Life OS could not create its calendar in Google: HTTP 403",
        },
      }),
      "Europe/Berlin",
    ),
    {
      text: "Life OS could not create its calendar in Google: HTTP 403",
      failing: true,
    },
  );
  assert.deepEqual(
    exportDetail(
      connection({ lifeOsCalendar, export: { pending: 2, failed: 0 } }),
      "Europe/Berlin",
    ),
    { text: "2 events waiting to be exported to “Life OS”…", failing: false },
  );
  assert.deepEqual(
    exportDetail(
      connection({
        lifeOsCalendar,
        export: {
          pending: 0,
          failed: 1,
          lastError: "Google is unavailable: HTTP 503",
        },
      }),
      "Europe/Berlin",
    ),
    {
      text: "1 event could not be exported: Google is unavailable: HTTP 503. Retrying automatically.",
      failing: true,
    },
  );
  assert.deepEqual(
    exportDetail(
      connection({
        lifeOsCalendar,
        export: {
          pending: 0,
          failed: 0,
          lastExportedAt: "2026-10-10T10:05:00Z",
        },
      }),
      "Europe/Berlin",
    ),
    {
      text: "Up to date in “Life OS” · last export 10 Oct, 12:05 CEST",
      failing: false,
    },
  );
});
