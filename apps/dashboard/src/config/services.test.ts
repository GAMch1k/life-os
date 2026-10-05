import assert from "node:assert/strict";
import test from "node:test";

import { normalizeServiceUrl } from "./services";

test("normalizes HTTP and HTTPS service URLs", () => {
  assert.equal(
    normalizeServiceUrl("SERVICE_URL", "http://localhost:8080/"),
    "http://localhost:8080",
  );
  assert.equal(
    normalizeServiceUrl("SERVICE_URL", "https://example.test/api/"),
    "https://example.test/api",
  );
});

test("rejects non-HTTP protocols", () => {
  assert.throws(
    () => normalizeServiceUrl("SERVICE_URL", "javascript:alert(1)"),
    /must use HTTP or HTTPS/,
  );
  assert.throws(
    () => normalizeServiceUrl("SERVICE_URL", "mailto:user@example.test"),
    /must use HTTP or HTTPS/,
  );
});

test("rejects credentials, query strings, and fragments", () => {
  assert.throws(
    () =>
      normalizeServiceUrl("SERVICE_URL", "https://user:secret@example.test"),
    /must not contain credentials/,
  );
  assert.throws(
    () =>
      normalizeServiceUrl("SERVICE_URL", "https://example.test?token=secret"),
    /must not contain a query string or fragment/,
  );
  assert.throws(
    () => normalizeServiceUrl("SERVICE_URL", "https://example.test/#section"),
    /must not contain a query string or fragment/,
  );
});

test("does not echo an invalid value in an error", () => {
  const secret = "definitely-not-a-url secret-value";
  assert.throws(
    () => normalizeServiceUrl("SERVICE_URL", secret),
    (error: unknown) =>
      error instanceof Error && !error.message.includes("secret-value"),
  );
});
