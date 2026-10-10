import assert from "node:assert/strict";
import test from "node:test";

import { packColumns } from "./masonry";

/** Where each column ends: the bottom of its last card. */
function columnBottoms(layout: ReturnType<typeof packColumns>) {
  const bottoms = new Map<number, number>();
  for (const placement of layout.placements) {
    bottoms.set(placement.column, placement.top + placement.span);
  }
  return [...bottoms.values()];
}

test("cards keep their order, each under the column that is shortest at that moment", () => {
  // The Status page's case: a long first card, a short second one.
  const layout = packColumns([600, 120, 200, 150, 90], 2, 16);
  assert.deepEqual(
    layout.placements.map(({ column, top }) => ({ column, top })),
    [
      { column: 0, top: 0 },
      { column: 1, top: 0 },
      // Under the short card, not below the long one.
      { column: 1, top: 136 },
      { column: 1, top: 352 },
      // The right column (502) is still shorter than the left one (600).
      { column: 1, top: 518 },
    ],
  );
  // Read top to bottom, then left to right, the cards are in their order.
  const reading = layout.placements
    .map((placement, index) => ({ ...placement, index }))
    .sort((a, b) => a.top - b.top || a.column - b.column)
    .map(({ index }) => index);
  assert.deepEqual(reading, [0, 1, 2, 3, 4]);
});

test("a tie goes to the left column", () => {
  const layout = packColumns([100, 100, 50, 50], 2, 10);
  assert.deepEqual(
    layout.placements.map(({ column, top }) => ({ column, top })),
    [
      { column: 0, top: 0 },
      { column: 1, top: 0 },
      { column: 0, top: 110 },
      { column: 1, top: 110 },
    ],
  );
});

test("only each column's last card stretches, so the columns end level", () => {
  const layout = packColumns([600, 120, 200, 150], 2, 16);
  assert.equal(layout.height, 600);
  assert.deepEqual(
    layout.placements.map(({ span, last }) => ({ span, last })),
    [
      { span: 600, last: true },
      { span: 120, last: false },
      { span: 200, last: false },
      // 352 + 150 = 502: grows by 98 to end with the left column.
      { span: 248, last: true },
    ],
  );
  assert.deepEqual(columnBottoms(layout), [600, 600]);
});

test("columns that already end level stretch nothing", () => {
  const layout = packColumns([100, 300, 120, 40], 2, 20);
  // Left: 100, then 120 at 120 (ends 240), then 40 at 260 (ends 300).
  assert.equal(layout.height, 300);
  assert.deepEqual(layout.placements[1], {
    column: 1,
    top: 0,
    span: 300,
    last: true,
  });
  assert.deepEqual(layout.placements[3], {
    column: 0,
    top: 260,
    span: 40,
    last: true,
  });
  assert.deepEqual(columnBottoms(layout), [300, 300]);
});

test("three columns fill left to right, then the shortest", () => {
  const layout = packColumns([50, 30, 40, 10], 3, 5);
  assert.deepEqual(
    layout.placements.map(({ column, top }) => ({ column, top })),
    [
      { column: 0, top: 0 },
      { column: 1, top: 0 },
      { column: 2, top: 0 },
      { column: 1, top: 35 },
    ],
  );
  assert.deepEqual(columnBottoms(layout), [50, 50, 50]);
});

test("one column stacks the cards in order with the gap between them", () => {
  for (const columns of [1, 0]) {
    const layout = packColumns([100, 50, 80], columns, 16);
    assert.deepEqual(layout, {
      placements: [
        { column: 0, top: 0, span: 100, last: false },
        { column: 0, top: 116, span: 50, last: false },
        { column: 0, top: 182, span: 80, last: true },
      ],
      height: 262,
    });
  }
});

test("fewer cards than columns leave the other columns empty", () => {
  assert.deepEqual(packColumns([70], 2, 16), {
    placements: [{ column: 0, top: 0, span: 70, last: true }],
    height: 70,
  });
});

test("no cards make an empty layout", () => {
  assert.deepEqual(packColumns([], 2, 16), { placements: [], height: 0 });
});
