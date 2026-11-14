import assert from "node:assert/strict";
import test from "node:test";

import { unstable_doesMiddlewareMatch } from "next/experimental/testing/server";

import { config } from "./proxy";

const matches = (url: string) =>
  unstable_doesMiddlewareMatch({ config, url: `http://lifeos.test${url}` });

test("the proxy checks the app's pages and routes", () => {
  for (const path of [
    "/",
    "/tasks",
    "/settings/security",
    "/api/status",
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
    "/api/auth/sign-in/username",
    "/api/account",
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
