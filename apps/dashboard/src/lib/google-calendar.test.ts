import assert from "node:assert/strict";
import test from "node:test";

import {
  calendarDetail,
  callbackStep,
  connectFailure,
  type GoogleCalendar,
  type GoogleConnection,
  googleState,
  missingPermissions,
  replaceCalendar,
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
