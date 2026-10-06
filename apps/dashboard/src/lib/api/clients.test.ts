import assert from "node:assert/strict";
import test from "node:test";

import {
  createCalendarClient,
  createFinanceClient,
  createInboxClient,
  createTasksClient,
} from "./index";

test("each generated service client accepts an explicit base URL", () => {
  const url = "http://example.test";

  assert.ok(createFinanceClient(url));
  assert.ok(createTasksClient(url));
  assert.ok(createInboxClient(url));
  assert.ok(createCalendarClient(url));
});
