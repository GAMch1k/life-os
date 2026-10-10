import assert from "node:assert/strict";
import { test } from "node:test";

import {
  currentMonth,
  monthLabel,
  monthRange,
  shiftMonth,
  summaryRows,
} from "./finance-summary";

test("currentMonth uses the owner's timezone, not UTC", () => {
  // 23:30 UTC on 31 October is already November in Berlin.
  const instant = new Date("2026-10-31T23:30:00Z");
  assert.equal(currentMonth(instant, "UTC"), "2026-10");
  assert.equal(currentMonth(instant, "Europe/Berlin"), "2026-11");
});

test("monthLabel names the month without shifting it", () => {
  assert.equal(monthLabel("2026-10"), "October 2026");
  assert.equal(monthLabel("2027-01"), "January 2027");
});

test("shiftMonth crosses years in both directions", () => {
  assert.equal(shiftMonth("2026-10", 1), "2026-11");
  assert.equal(shiftMonth("2026-12", 1), "2027-01");
  assert.equal(shiftMonth("2026-01", -1), "2025-12");
  assert.equal(shiftMonth("2026-03", -14), "2025-01");
});

test("monthRange spans the whole month, leap days included", () => {
  assert.deepEqual(monthRange("2026-10"), {
    from: "2026-10-01",
    to: "2026-10-31",
  });
  assert.deepEqual(monthRange("2028-02"), {
    from: "2028-02-01",
    to: "2028-02-29",
  });
});

const eur = (amount: number) => ({ amount, currency: "EUR" });

test("summaryRows shows each category's larger side against the largest", () => {
  const rows = summaryRows(
    [
      {
        categoryId: "groceries",
        categoryName: "Groceries",
        categoryKind: "expense",
        income: eur(0),
        spending: eur(20000),
        net: eur(-20000),
        transactionCount: 4,
      },
      {
        categoryId: null,
        categoryName: null,
        categoryKind: null,
        income: eur(500),
        spending: eur(5000),
        net: eur(-4500),
        transactionCount: 2,
      },
      {
        categoryId: "salary",
        categoryName: "Salary",
        categoryKind: "income",
        income: eur(40000),
        spending: eur(0),
        net: eur(40000),
        transactionCount: 1,
      },
    ],
    [
      {
        id: "groceries",
        name: "Groceries",
        kind: "expense",
        color: "#22c55e",
        archived: false,
        archivedAt: null,
        createdAt: "2026-10-01T00:00:00Z",
        updatedAt: "2026-10-01T00:00:00Z",
      },
    ],
  );
  assert.deepEqual(
    rows.map(({ key, name, direction, amount, share, color }) => ({
      key,
      name,
      direction,
      amount,
      share,
      color,
    })),
    [
      {
        key: "groceries",
        name: "Groceries",
        direction: "out",
        amount: 20000,
        share: 0.5,
        color: "#22c55e",
      },
      {
        key: "uncategorized",
        name: "Uncategorized",
        direction: "out",
        amount: 5000,
        share: 0.125,
        color: null,
      },
      {
        key: "salary",
        name: "Salary",
        direction: "in",
        amount: 40000,
        share: 1,
        color: null,
      },
    ],
  );
});

test("summaryRows has no bars when nothing moved", () => {
  assert.deepEqual(summaryRows([]), []);
  const [row] = summaryRows([
    {
      categoryId: "transfer",
      categoryName: "Transfer",
      categoryKind: "transfer",
      income: eur(0),
      spending: eur(0),
      net: eur(0),
      transactionCount: 1,
    },
  ]);
  assert.equal(row.share, 0);
});
