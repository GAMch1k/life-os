import assert from "node:assert/strict";
import test from "node:test";

import { unstable_doesMiddlewareMatch } from "next/experimental/testing/server";
import { NextRequest } from "next/server";

import { config, proxy } from "./proxy";

const matches = (url: string) =>
  unstable_doesMiddlewareMatch({ config, url: `http://lifeos.test${url}` });

test("the proxy checks the app's pages and routes", () => {
  for (const path of [
    "/",
    "/tasks",
    "/settings/security",
    "/settings/email",
    "/settings/security-log",
    "/admin/security-log",
    "/admin/diagnostics",
    "/api/status",
    "/api/backups/v1/status",
    "/api/tasks/v1/tasks",
    "/share/about",
    "/share/saved",
  ]) {
    assert.equal(matches(path), true, path);
  }
});

test("the proxy leaves out what is public or checks itself", () => {
  for (const path of [
    "/login",
    "/setup",
    "/reset/abc",
    "/forgot-password",
    "/verify-email/abc",
    "/api/auth/email/forgot-password",
    "/api/auth/sign-in/username",
    "/api/account",
    "/api/admin/security-log",
    "/internal/auth/check",
    "/privacy",
    "/sw.js",
    "/app.webmanifest",
    "/icons/icon-192.png",
    "/_next/static/chunk.js",
  ]) {
    assert.equal(matches(path), false, path);
  }
});

// Next.js copies the body of a POST the proxy matches, and caps the copy at
// 10 MiB: a share of two or three photos must never pass through it.
test("a share from the installed app does not go through the proxy", () => {
  assert.equal(matches("/share"), false);
});

// Refused before the sign-in check, with or without a credential (and so
// without the auth database these tests do not have).
test("encoded separators in a path are refused before anything else", async () => {
  for (const path of [
    "/api/tasks/http:%2F%2F192.0.2.1%2Fx.html%3F/..%2F..%2F..%2F..%2F..",
    "/api/tasks/http:%2f%2f192.0.2.1%2fx/..%2f..",
    "/api/finance/..%2F..%2F..%2Fx",
    "/api/inbox/v1%5Cinbox-items",
    "/api/tasks/http:%5C%5C192.0.2.1%5Cx",
    "/api/integrations/recipe-import/http:%2F%2F192.0.2.1",
    "/api/backups/v1%2Fstatus",
    "/tasks/%2F%2F192.0.2.1",
  ]) {
    assert.equal(matches(path), true, path);
    const response = await proxy(new NextRequest(`http://lifeos.test${path}`));
    assert.equal(response.status, 400, path);
    const body = (await response.json()) as { error: { code: string } };
    assert.equal(body.error.code, "forbidden_path", path);
  }
});
