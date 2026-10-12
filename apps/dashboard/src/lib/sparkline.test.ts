import assert from "node:assert/strict";
import test from "node:test";

import {
  downsampleMax,
  downsampleMin,
  highestPoint,
  lowestPoint,
  sparkGeometry,
} from "./sparkline";

const at = (minute: number) =>
  new Date(Date.UTC(2026, 9, 11, 0, minute)).toISOString();

test("downsampleMin keeps each span's lowest point, and ends with now", () => {
  const samples = Array.from({ length: 13 }, (_, minute) => ({
    at: at(minute),
    value: minute === 4 ? 1 : 10 + minute,
  }));
  const points = downsampleMin(samples, 4);
  assert.equal(points.length, 4);
  assert.deepEqual(points[0], { at: at(0), value: 10 });
  assert.deepEqual(points[1], { at: at(4), value: 1 });
  assert.equal(points[2].at, at(8));
  assert.deepEqual(points[3], { at: at(12), value: 22 });
  // Fewer samples than buckets are kept as they are.
  assert.equal(downsampleMin(samples.slice(0, 2), 3).length, 2);
});

test("downsampleMax keeps each span's highest point, and ends with now", () => {
  const samples = Array.from({ length: 13 }, (_, minute) => ({
    at: at(minute),
    value: minute === 5 ? 100 : 10 - minute / 2,
  }));
  const points = downsampleMax(samples, 4);
  assert.equal(points.length, 4);
  assert.deepEqual(points[0], { at: at(0), value: 10 });
  assert.deepEqual(points[1], { at: at(5), value: 100 });
  assert.deepEqual(points[2], { at: at(8), value: 6 });
  assert.deepEqual(points[3], { at: at(12), value: 4 });
  assert.equal(downsampleMax(samples.slice(0, 2), 3).length, 2);
});

test("highestPoint finds the earliest highest point", () => {
  assert.equal(highestPoint([]), undefined);
  assert.deepEqual(
    highestPoint([
      { at: at(0), value: 2 },
      { at: at(1), value: 5 },
      { at: at(2), value: 5 },
    ]),
    { at: at(1), value: 5 },
  );
});

test("lowestPoint finds the earliest lowest point", () => {
  assert.equal(lowestPoint([]), undefined);
  assert.deepEqual(
    lowestPoint([
      { at: at(0), value: 5 },
      { at: at(1), value: 2 },
      { at: at(2), value: 2 },
    ]),
    { at: at(1), value: 2 },
  );
});

test("sparkGeometry maps values from 0 at the bottom to max at the top", () => {
  const { line, area, xs, ys } = sparkGeometry(
    [
      { at: at(0), value: 0 },
      { at: at(1), value: 50 },
      { at: at(2), value: 100 },
      { at: at(3), value: 150 },
    ],
    100,
    300,
    48,
  );
  assert.deepEqual(xs, [0, 100, 200, 300]);
  // Values above max are drawn at the top.
  assert.deepEqual(ys, [48, 24, 0, 0]);
  assert.equal(line, "M0 48 L100 24 L200 0 L300 0");
  assert.equal(area, "M0 48 L100 24 L200 0 L300 0 L300 48 L0 48 Z");
  assert.equal(sparkGeometry([], 100, 300, 48).area, "");
});
