import assert from "node:assert/strict";
import test from "node:test";

import {
  accountLabel,
  bankFailure,
  bankState,
  callbackStep,
  consentText,
  liveConnection,
  needsReconnect,
  nextSyncText,
  syncNowState,
  syncText,
  withoutBankParam,
  type BankConnection,
  type BankConnectionList,
  consentFailureText,
} from "./banking";

const now = new Date("2026-10-10T10:00:00Z");
const timeZone = "Europe/Berlin";

const connection = (
  overrides: Partial<BankConnection> = {},
): BankConnection => ({
  id: "c1",
  provider: "enablebanking",
  institution: "Deutsche Bank",
  country: "DE",
  status: "connected",
  consentValidUntil: "2027-04-08T10:00:00Z",
  accounts: [
    {
      id: "iban-1",
      name: "Das Junge Konto",
      currency: "EUR",
      kind: "bank",
      ibanMasked: "DE89…3000",
    },
  ],
  lastSyncAt: "2026-10-10T08:00:00Z",
  lastSuccessAt: "2026-10-10T08:00:00Z",
  nextSyncAt: "2026-10-10T10:00:00Z",
  consecutiveFailures: 0,
  lastError: null,
  lastCounts: {
    accounts: { created: 0, updated: 1 },
    transactions: {
      created: 3,
      updated: 0,
      unchanged: 170,
      removedPending: 0,
      skipped: 0,
    },
    fetched: 173,
    ignored: 0,
  },
  unattendedReadsToday: 1,
  manualSyncAvailableAt: null,
  waitingForFinance: false,
  connectedAt: "2026-10-10T08:00:00Z",
  disconnectedAt: null,
  createdAt: "2026-10-10T08:00:00Z",
  updatedAt: "2026-10-10T08:00:00Z",
  ...overrides,
});

const list = (items: BankConnection[]): BankConnectionList => ({
  configured: true,
  setupProblem: null,
  schedule: {
    times: ["06:00", "12:00", "18:00", "23:00"],
    timeZone,
    unattendedPerDay: 4,
    manualSyncMinutes: 10,
  },
  items,
  nextCursor: null,
  lastConsentFailure: null,
});

test("the live connection is the bank's, never a disconnected one", () => {
  const old = connection({ id: "old", status: "disconnected" });
  assert.equal(liveConnection(list([old])), undefined);
  assert.equal(liveConnection(list([old, connection()]))?.id, "c1");
});

test("the card state follows setup, then status and consent", () => {
  assert.equal(bankState(false, connection(), now), "not_configured");
  assert.equal(bankState(true, undefined, now), "not_connected");
  assert.equal(bankState(true, connection(), now), "connected");
  assert.equal(
    bankState(
      true,
      connection({ consentValidUntil: "2026-10-15T10:00:00Z" }),
      now,
    ),
    "expiring",
  );
  assert.equal(
    bankState(true, connection({ status: "expired" }), now),
    "expired",
  );
  assert.equal(bankState(true, connection({ status: "error" }), now), "error");
  assert.equal(
    bankState(true, connection({ status: "pending" }), now),
    "pending",
  );
  assert.ok(needsReconnect("expired") && needsReconnect("expiring"));
  assert.ok(!needsReconnect("connected") && !needsReconnect("not_connected"));
});

test("accounts show their masked IBAN", () => {
  assert.equal(
    accountLabel(connection().accounts[0]),
    "Das Junge Konto (DE89…3000)",
  );
  assert.equal(
    accountLabel({ id: "x", name: "Card", currency: "EUR", kind: "card" }),
    "Card",
  );
});

test("the consent is described by how soon it ends", () => {
  assert.equal(
    consentText(connection(), timeZone, now),
    "Access valid until 8 Apr 2027",
  );
  assert.equal(
    consentText(
      connection({ consentValidUntil: "2026-10-13T09:00:00Z" }),
      timeZone,
      now,
    ),
    "Access ends in 3 days (13 Oct 2026)",
  );
  assert.equal(
    consentText(
      connection({ consentValidUntil: "2026-10-10T20:00:00Z" }),
      timeZone,
      now,
    ),
    "Access ends 10 Oct, 22:00 CEST",
  );
  assert.equal(
    consentText(
      connection({ consentValidUntil: "2026-10-01T20:00:00Z" }),
      timeZone,
      now,
    ),
    "Access ended on 1 Oct 2026",
  );
});

test("the sync line says how syncing goes", () => {
  assert.deepEqual(syncText(connection(), timeZone), {
    text: "Synced 10 Oct, 10:00 CEST · 3 new transactions",
    failing: false,
  });
  assert.deepEqual(
    syncText(
      connection({
        lastCounts: {
          ...connection().lastCounts!,
          transactions: {
            created: 0,
            updated: 0,
            unchanged: 3,
            removedPending: 0,
            skipped: 0,
          },
        },
      }),
      timeZone,
    ).text,
    "Synced 10 Oct, 10:00 CEST · up to date",
  );
  assert.deepEqual(
    syncText(
      connection({
        consecutiveFailures: 2,
        lastError:
          "Deutsche Bank could not be read: Enable Banking answered 500 ASPSP_ERROR. Retrying automatically.",
      }),
      timeZone,
    ),
    {
      text: "Deutsche Bank could not be read: Enable Banking answered 500 ASPSP_ERROR. Retrying automatically. Last successful sync 10 Oct, 10:00 CEST.",
      failing: true,
    },
  );
  assert.equal(
    syncText(
      connection({ status: "pending", lastSuccessAt: null, lastCounts: null }),
      timeZone,
    ).text,
    "Importing your transactions…",
  );
  assert.match(
    syncText(
      connection({ waitingForFinance: true, consecutiveFailures: 1 }),
      timeZone,
    ).text,
    /^Read from the bank; waiting for Finance/,
  );
  assert.equal(nextSyncText(connection(), timeZone), "Next sync 12:00 CEST");
  assert.equal(
    nextSyncText(connection({ nextSyncAt: null }), timeZone),
    undefined,
  );
});

test("sync now waits for the throttle and an active connection", () => {
  assert.deepEqual(syncNowState(connection(), timeZone, now), {
    allowed: true,
  });
  assert.deepEqual(
    syncNowState(
      connection({ manualSyncAvailableAt: "2026-10-10T10:08:00Z" }),
      timeZone,
      now,
    ),
    { allowed: false, reason: "Synced moments ago; again from 12:08 CEST." },
  );
  assert.equal(
    syncNowState(connection({ status: "expired" }), timeZone, now).allowed,
    false,
  );
});

test("a refusal is reported to banking with its state and description, never a code", () => {
  assert.deepEqual(
    callbackStep({
      error: "server_error",
      error_description: "  Internal\n error  ",
      state: "s1",
      code: "never",
    }),
    {
      kind: "error",
      message:
        "The bank did not complete the consent (server_error). Internal error",
      report: {
        error: "server_error",
        state: "s1",
        errorDescription: "Internal error",
      },
    },
  );
  assert.deepEqual(
    (callbackStep({ error: "access_denied" }) as { report?: unknown }).report,
    { error: "access_denied" },
  );
  // Visiting the page without the bank's answer reports nothing.
  assert.equal((callbackStep({}) as { report?: unknown }).report, undefined);
});

test("the last failed consent is described with its time and reason", () => {
  assert.equal(
    consentFailureText(
      {
        at: "2026-10-10T18:19:00Z",
        error: "server_error",
        description: "Internal error",
        institution: "Deutsche Bank",
        stateMatched: true,
      },
      "Europe/Berlin",
    ),
    "Last attempt failed on 10 Oct, 20:19 CEST: server_error — Internal error",
  );
  assert.equal(
    consentFailureText(
      {
        at: "2026-10-10T18:19:00Z",
        error: "access_denied",
        description: null,
        institution: null,
        stateMatched: false,
      },
      "Europe/Berlin",
    ),
    "Last attempt failed on 10 Oct, 20:19 CEST: access_denied",
  );
});

test("the callback exchanges the code or explains the bank's answer", () => {
  assert.deepEqual(callbackStep({ code: "c", state: "s" }), {
    kind: "exchange",
    code: "c",
    state: "s",
  });
  assert.deepEqual(callbackStep({ code: ["c", "d"], state: ["s"] }), {
    kind: "exchange",
    code: "c",
    state: "s",
  });
  assert.equal(
    callbackStep({ error: "access_denied", state: "s" }).kind,
    "error",
  );
  assert.match(
    (callbackStep({ error: "access_denied" }) as { message: string }).message,
    /did not give consent/,
  );
  assert.match(
    (callbackStep({ error: "x".repeat(100) }) as { message: string }).message,
    /\(x{64}\)/,
  );
  assert.equal(
    (
      callbackStep({
        error: "server_error",
        error_description: "  Consent\n failed at the bank ",
      }) as { message: string }
    ).message,
    "The bank did not complete the consent (server_error). Consent failed at the bank",
  );
  assert.match(
    (callbackStep({}) as { message: string }).message,
    /Connect Deutsche Bank/,
  );
});

test("failures are explained in plain words", () => {
  assert.match(
    bankFailure(400, { error: { code: "invalid_state", message: "x" } })
      .message,
    /expired or was already used/,
  );
  assert.match(
    bankFailure(503, { error: { code: "banking_not_configured" } }).message,
    /LIFE_OS_ENABLE_BANKING_APP_ID/,
  );
  assert.equal(
    bankFailure(502, {
      error: {
        code: "bank_unavailable",
        message: "deutsche Bank could not be read",
      },
    }).message,
    "Deutsche Bank could not be read.",
  );
  assert.equal(
    bankFailure(503, undefined).message,
    "Banking is unavailable right now. Try again.",
  );
});

test("the connected marker is dropped once shown", () => {
  assert.equal(withoutBankParam("?bank=connected"), "");
  assert.equal(
    withoutBankParam("?month=2026-10&bank=connected"),
    "?month=2026-10",
  );
});
