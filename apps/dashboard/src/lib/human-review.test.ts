import assert from "node:assert/strict";
import test from "node:test";

import {
  replacementMetadata,
  replacementNotice,
  reviewTotal,
} from "./human-review";
import { captureMetadata } from "./time-zone";

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

test("an edited capture names the one it replaces next to the timezone", () => {
  const id = "0b9d4c1e-6f3a-4c55-9a43-2f1d7e8c9b10";
  assert.deepEqual(captureMetadata(replacementMetadata(id), "Europe/Berlin"), {
    replacesItemId: id,
    timeZone: "Europe/Berlin",
  });
  // Without a browser timezone it still names the replaced capture.
  assert.deepEqual(captureMetadata(replacementMetadata(id), null), {
    replacesItemId: id,
  });
});

test("the edit notice says what happened to the edit and the original", () => {
  assert.equal(
    replacementNotice("text"),
    "Captured your edit; the original was discarded.",
  );
  // A recording only becomes a capture once Voice has transcribed it.
  assert.match(replacementNotice("voice"), /once transcribed/);
  assert.match(replacementNotice("voice"), /original was discarded/);
});
