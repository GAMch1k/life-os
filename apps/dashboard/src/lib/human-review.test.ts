import assert from "node:assert/strict";
import test from "node:test";

import { reviewTotal } from "./human-review";

test("the review count is the backlog's, never fewer than are listed", () => {
  assert.equal(reviewTotal([1, 2], 7), 7);
  // The backlog lags behind the list, or could not be read.
  assert.equal(reviewTotal([1, 2, 3], 2), 3);
  assert.equal(reviewTotal([1], undefined), 1);
  assert.equal(reviewTotal([], undefined), 0);
  // Without a list, only the backlog knows.
  assert.equal(reviewTotal(null, 4), 4);
  assert.equal(reviewTotal(null, undefined), undefined);
});
