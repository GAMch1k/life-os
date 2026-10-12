import assert from "node:assert/strict";
import test from "node:test";

import type { BankConnection, BankConnectionList } from "./banking";
import {
  BANK_RECONNECT,
  BANK_SYNC_RETRY,
  bankPart,
  statusAlerts,
  toRuntimeStatus,
  type RuntimeSignals,
} from "./runtime-status";

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
  lastCounts: null,
  unattendedReadsToday: 1,
  manualSyncAvailableAt: null,
  waitingForFinance: false,
  transactionsSince: "2026-07-14",
  historyRead: null,
  connectedAt: "2026-10-10T08:00:00Z",
  disconnectedAt: null,
  createdAt: "2026-10-10T08:00:00Z",
  updatedAt: "2026-10-10T08:00:00Z",
  ...overrides,
});

const list = (
  items: BankConnection[],
  overrides: Partial<BankConnectionList> = {},
): BankConnectionList => ({
  configured: true,
  setupProblem: null,
  schedule: {
    times: ["06:00", "12:00", "18:00", "23:00"],
    timeZone,
    unattendedPerDay: 4,
    manualSyncMinutes: 10,
    historyDays: 730,
  },
  items,
  nextCursor: null,
  lastConsentFailure: null,
  ...overrides,
});

const signals = (
  bankConnections: BankConnectionList | null | undefined,
): RuntimeSignals => ({
  processor: null,
  inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
  voice: {
    queuedCount: 0,
    processingCount: 0,
    retryableCount: 0,
    workerStalled: false,
    whisperReady: true,
  },
  bankConnections,
  services: [{ name: "Banking", ready: true }],
});

const bankCheck = (bankConnections: BankConnectionList | null) =>
  toRuntimeStatus(signals(bankConnections), now, timeZone).checks.find(
    (check) => check.id === "banking",
  )!;

test("the bank check is left out when not read", () => {
  assert.equal(
    toRuntimeStatus(signals(undefined), now, timeZone).checks.some(
      (check) => check.id === "banking",
    ),
    false,
  );
});

test("no bank set up or connected is neutral, and needs nothing", () => {
  const unset = bankCheck(list([], { configured: false }));
  assert.equal(unset.state, "neutral");
  assert.equal(unset.summary, "No bank connection is set up.");
  const none = bankCheck(
    list([connection({ status: "disconnected", nextSyncAt: null })]),
  );
  assert.equal(none.state, "neutral");
  assert.equal(none.summary, "No bank is connected.");
  assert.equal(
    statusAlerts(toRuntimeStatus(signals(list([])), now, timeZone)).some(
      (check) => check.id === "banking",
    ),
    false,
  );
});

test("an incomplete setup is a warning naming the problem", () => {
  const check = bankCheck(
    list([], {
      configured: false,
      setupProblem:
        "Enable Banking is not configured: LIFE_OS_BANKING_TOKEN_KEY is not set",
    }),
  );
  assert.equal(check.state, "warning");
  assert.match(check.detail ?? "", /LIFE_OS_BANKING_TOKEN_KEY/);
});

test("a healthy connection is ok with its facts", () => {
  const check = bankCheck(list([connection()]));
  assert.equal(check.state, "ok");
  assert.equal(check.summary, "Deutsche Bank: Connected and syncing.");
  const part = check.parts![0];
  assert.deepEqual(
    part.facts.map((fact) => fact.label),
    ["Accounts", "Last sync", "Next sync", "Access until"],
  );
  assert.equal(part.facts[0].value, "Das Junge Konto (DE89…3000)");
});

test("a consent ending soon is shown but stays ok, so the monitor sends nothing", () => {
  const expiring = bankCheck(
    list([connection({ consentValidUntil: "2026-10-15T10:00:00Z" })]),
  );
  assert.equal(expiring.state, "ok");
  assert.equal(
    expiring.summary,
    "Deutsche Bank access ends on 15 Oct 2026, 12:00 CEST.",
  );
  assert.equal(expiring.action, undefined);
  assert.equal(expiring.parts![0].state, "ok");
  assert.equal(
    statusAlerts(
      toRuntimeStatus(
        signals(
          list([connection({ consentValidUntil: "2026-10-10T12:00:00Z" })]),
        ),
        now,
        timeZone,
      ),
    ).some((check) => check.id === "banking"),
    false,
  );
});

test("an expired or refused consent is a warning with an action", () => {
  const expired = bankCheck(
    list([
      connection({
        status: "expired",
        nextSyncAt: null,
        lastError: "Deutsche Bank access expired on Sat 8 Apr 2027.",
      }),
    ]),
  );
  assert.equal(expired.state, "warning");
  assert.equal(
    expired.summary,
    "Deutsche Bank access expired; transactions no longer sync.",
  );
  assert.equal(
    expired.detail,
    "Deutsche Bank access expired on Sat 8 Apr 2027.",
  );
  assert.equal(expired.action, BANK_RECONNECT);

  const refused = bankCheck(list([connection({ status: "error" })]));
  assert.equal(refused.state, "warning");
  assert.equal(refused.action, BANK_RECONNECT);
});

test("failures warn only after several in a row", () => {
  const once = bankPart(
    connection({ consecutiveFailures: 1, lastError: "x" }),
    now,
    timeZone,
  );
  assert.equal(once.state, "ok");
  const check = bankCheck(
    list([connection({ consecutiveFailures: 3, lastError: "ASPSP_ERROR" })]),
  );
  assert.equal(check.state, "warning");
  assert.equal(
    check.summary,
    "Syncing Deutsche Bank keeps failing: 3 attempts in a row.",
  );
  assert.equal(check.action, BANK_SYNC_RETRY);
});

test("banking unreadable is a warning", () => {
  const check = bankCheck(null);
  assert.equal(check.state, "warning");
  assert.equal(check.summary, "The bank connection is unavailable.");
});
