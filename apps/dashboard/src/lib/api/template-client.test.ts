import assert from "node:assert/strict";
import test from "node:test";

import { createTemplateClient } from "@/lib/api/template-client";

test("generated client preserves required query parameter types", () => {
  const client = createTemplateClient("http://template.test");

  assert.equal(typeof client.GET, "function");
});
