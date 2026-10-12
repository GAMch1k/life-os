import assert from "node:assert/strict";
import { test } from "node:test";

import {
  type PaydaySettings,
  type PaydaySuggestion,
  offeredSuggestion,
  paydayBody,
  paydayDraft,
  suggestionLabel,
  suggestionNote,
} from "./finance-payday";

const suggestion: PaydaySuggestion = {
  payerMatch: "ACME Robotics GmbH",
  typicalAmount: { amount: 310000, currency: "EUR" },
  payments: 3,
  lastDate: "2026-09-29",
};

const settings = (overrides: Partial<PaydaySettings> = {}): PaydaySettings => ({
  enabled: false,
  payerMatch: null,
  minAmount: null,
  currency: "EUR",
  suggestion,
  updatedAt: "2026-10-12T09:00:00Z",
  ...overrides,
});

test("the draft shows the saved setting", () => {
  assert.deepEqual(paydayDraft(settings(), "en-GB"), {
    enabled: false,
    payer: "",
    minimum: "",
  });
  assert.deepEqual(
    paydayDraft(
      settings({
        enabled: true,
        payerMatch: "ACME Robotics GmbH",
        minAmount: { amount: 100000, currency: "EUR" },
      }),
      "en-GB",
    ),
    { enabled: true, payer: "ACME Robotics GmbH", minimum: "1000.00" },
  );
});

test("the body needs a payer when on, and a minimum of at least one cent", () => {
  assert.deepEqual(
    paydayBody(
      { enabled: true, payer: "  ACME Robotics GmbH ", minimum: "1000" },
      "EUR",
    ).body,
    { enabled: true, payerMatch: "ACME Robotics GmbH", minAmountMinor: 100000 },
  );
  assert.deepEqual(
    paydayBody({ enabled: false, payer: " ", minimum: "" }, "EUR").body,
    { enabled: false, payerMatch: null, minAmountMinor: null },
  );
  const off = paydayBody({ enabled: true, payer: "", minimum: "0" }, "EUR");
  assert.equal(off.body, undefined);
  assert.match(off.errors.payer ?? "", /who pays your salary/);
  assert.match(off.errors.minimum ?? "", /like 1000\.00/);
  assert.match(
    paydayBody({ enabled: true, payer: "x".repeat(201), minimum: "" }, "EUR")
      .errors.payer ?? "",
    /200 characters/,
  );
  assert.match(
    paydayBody({ enabled: false, payer: "", minimum: "abc" }, "EUR").errors
      .minimum ?? "",
    /leave it empty/,
  );
});

test("the suggestion is offered until the payer says it", () => {
  const draft = { enabled: false, payer: "", minimum: "" };
  assert.equal(offeredSuggestion(suggestion, draft), suggestion);
  assert.equal(
    offeredSuggestion(suggestion, { ...draft, payer: " acme  robotics gmbh" }),
    undefined,
  );
  assert.equal(offeredSuggestion(null, draft), undefined);
  assert.equal(suggestionLabel(suggestion), "Use ACME Robotics GmbH");
  assert.equal(
    suggestionNote(suggestion, "en-GB"),
    "About €3,100.00 a month, 3 months in a row, last on 29 Sept",
  );
});
