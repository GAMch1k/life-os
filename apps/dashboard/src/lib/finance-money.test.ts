import assert from "node:assert/strict";
import { test } from "node:test";

import {
  amountInput,
  balanceBreakdown,
  directionOf,
  formatMoney,
  minorDigits,
  parseAmount,
  signed,
} from "./finance-money";

/** Intl uses narrow and non-breaking spaces; tests compare plain text. */
const plain = (text: string) => text.replace(/[  ]/g, " ");

test("minorDigits follows the currency", () => {
  assert.equal(minorDigits("EUR"), 2);
  assert.equal(minorDigits("JPY"), 0);
  assert.equal(minorDigits("KWD"), 3);
});

test("formatMoney shows minor units in the locale's format", () => {
  const coffee = { amount: -1250, currency: "EUR" };
  assert.equal(formatMoney(coffee, "en-GB"), "-€12.50");
  assert.equal(plain(formatMoney(coffee, "de-DE")), "-12,50 €");
  assert.equal(
    plain(formatMoney({ amount: 123456, currency: "EUR" }, "de-DE")),
    "1.234,56 €",
  );
  assert.equal(
    formatMoney({ amount: 1500, currency: "JPY" }, "en-GB"),
    "JP¥1,500",
  );
});

test("formatMoney marks money in with a plus when signed", () => {
  assert.equal(
    formatMoney({ amount: 200000, currency: "EUR" }, "en-GB", { signed: true }),
    "+€2,000.00",
  );
  assert.equal(
    formatMoney({ amount: -99, currency: "EUR" }, "en-GB", { signed: true }),
    "-€0.99",
  );
  assert.equal(
    formatMoney({ amount: 0, currency: "EUR" }, "en-GB", { signed: true }),
    "€0.00",
  );
});

test("amountInput is the editable size in the locale, without sign", () => {
  assert.equal(amountInput(-123450, "EUR", "en-GB"), "1234.50");
  assert.equal(amountInput(-123450, "EUR", "de-DE"), "1234,50");
  assert.equal(amountInput(1500, "JPY", "de-DE"), "1500");
});

test("parseAmount reads both decimal marks to minor units", () => {
  assert.equal(parseAmount("12.50", "EUR"), 1250);
  assert.equal(parseAmount("12,50", "EUR"), 1250);
  assert.equal(parseAmount("12,5", "EUR"), 1250);
  assert.equal(parseAmount("12", "EUR"), 1200);
  assert.equal(parseAmount(".5", "EUR"), 50);
  assert.equal(parseAmount("0,99", "EUR"), 99);
});

test("parseAmount reads grouped thousands", () => {
  assert.equal(parseAmount("1,234.56", "EUR"), 123456);
  assert.equal(parseAmount("1.234,56", "EUR"), 123456);
  assert.equal(parseAmount("1 234,56", "EUR"), 123456);
  assert.equal(parseAmount("1 234,56", "EUR"), 123456);
  assert.equal(parseAmount("1.234", "EUR"), 123400);
  assert.equal(parseAmount("1,234,567", "EUR"), 123456700);
  assert.equal(parseAmount("1.234.567,89", "EUR"), 123456789);
});

test("parseAmount keeps the sign and ignores currency marks", () => {
  assert.equal(parseAmount("-12,50", "EUR"), -1250);
  assert.equal(parseAmount("-€12.50", "EUR"), -1250);
  assert.equal(parseAmount("12,50 €", "EUR"), 1250);
  assert.equal(parseAmount("+5", "EUR"), 500);
  assert.equal(parseAmount("-0", "EUR"), 0);
});

test("parseAmount refuses text that is not an amount", () => {
  for (const text of ["", " ", "abc", "12abc34", "1,23,4", "12,345,67", "-"]) {
    assert.equal(parseAmount(text, "EUR"), undefined, text);
  }
});

test("parseAmount refuses more decimals than the currency has", () => {
  assert.equal(parseAmount("1.2345", "EUR"), undefined);
  assert.equal(parseAmount("1,234.567", "EUR"), undefined);
  assert.equal(parseAmount("12.5", "JPY"), undefined);
  assert.equal(parseAmount("1.234", "JPY"), 1234);
  assert.equal(parseAmount("99999999999999999999", "EUR"), undefined);
});

test("directionOf and signed agree that money out is negative", () => {
  assert.equal(directionOf(-1), "out");
  assert.equal(directionOf(1), "in");
  assert.equal(signed(1250, "out"), -1250);
  assert.equal(signed(-1250, "in"), 1250);
  assert.equal(signed(-1250, "out"), -1250);
});

test("balanceBreakdown splits a balance with pending money", () => {
  const booked = { amount: 25105, currency: "EUR" };
  const holds = { amount: -14535, currency: "EUR" };
  assert.equal(
    balanceBreakdown(booked, holds, "en-GB"),
    "Booked €251.05 · Pending -€145.35",
  );
  assert.equal(
    plain(balanceBreakdown(booked, holds, "de-DE")!),
    "Booked 251,05 € · Pending -145,35 €",
  );
  // A pending refund is money in.
  assert.equal(
    balanceBreakdown(booked, { amount: 500, currency: "EUR" }, "en-GB"),
    "Booked €251.05 · Pending +€5.00",
  );
  // Nothing pending: the balance is all booked, so there is no line.
  assert.equal(
    balanceBreakdown(booked, { amount: 0, currency: "EUR" }, "en-GB"),
    undefined,
  );
});
