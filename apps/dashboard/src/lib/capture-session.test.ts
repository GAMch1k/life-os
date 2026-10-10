import assert from "node:assert/strict";
import test from "node:test";

import { announceCapture, onCapture } from "./capture-session";
import type { InboxItem } from "./inbox-summary";

const item = (id: string): InboxItem => ({
  id,
  content: `Capture ${id}`,
  correlationId: id,
  source: "manual",
  state: "raw",
  processingMetadata: {},
  createdAt: "2026-10-10T10:00:00Z",
  updatedAt: "2026-10-10T10:00:00Z",
});

test("every listener hears each announced capture until it unsubscribes", () => {
  const card: string[] = [];
  const other: string[] = [];
  const stopCard = onCapture((captured) => card.push(captured.id));
  const stopOther = onCapture((captured) => other.push(captured.id));
  announceCapture(item("a"));
  stopOther();
  announceCapture(item("b"));
  stopCard();
  announceCapture(item("c"));
  assert.deepEqual(card, ["a", "b"]);
  assert.deepEqual(other, ["a"]);
});

test("announcing with nobody listening is harmless", () => {
  assert.doesNotThrow(() => announceCapture(item("d")));
});
