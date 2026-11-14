import assert from "node:assert/strict";
import test from "node:test";

import { hostList, onOff, readAuthEnv } from "./env";
import { TokenCache } from "./identity";
import {
  anonymousWriteAllowed,
  cookieWriteAllowed,
  isSecureContext,
  requestHost,
} from "./origin";
import { isFresh, serverRole } from "./roles";
import { safeNext } from "./safe-next";
import { normalizeSetupCode, newSetupCode } from "./store";
import { LoginThrottle } from "./throttle";

const own = (extra: Record<string, string> = {}) =>
  new Headers({ host: "lifeos.example.com", ...extra });

test("a cookie write needs Life OS's own origin", () => {
  assert.equal(cookieWriteAllowed("GET", own()), true);
  assert.equal(
    cookieWriteAllowed("POST", own({ origin: "https://lifeos.example.com" })),
    true,
  );
  // The port does not count: the gateway passes the host without it.
  assert.equal(
    cookieWriteAllowed(
      "POST",
      new Headers({ host: "127.0.0.1", origin: "http://127.0.0.1:3000" }),
    ),
    true,
  );
  assert.equal(
    cookieWriteAllowed("POST", own({ origin: "https://evil.example" })),
    false,
  );
  assert.equal(
    cookieWriteAllowed("POST", own({ origin: "https://ntfy.example.com" })),
    false,
  );
  assert.equal(
    cookieWriteAllowed("DELETE", own({ "sec-fetch-site": "cross-site" })),
    false,
  );
  assert.equal(
    cookieWriteAllowed("PATCH", own({ "sec-fetch-site": "same-site" })),
    false,
  );
  assert.equal(
    cookieWriteAllowed("PUT", own({ "sec-fetch-site": "same-origin" })),
    true,
  );
  // The installed app's share sheet: a navigation the person started.
  assert.equal(
    cookieWriteAllowed(
      "POST",
      own({ "sec-fetch-site": "none", origin: "null" }),
    ),
    true,
  );
  // A cookie and no browser signal at all.
  assert.equal(cookieWriteAllowed("POST", own()), false);
});

test("without a credential, only a browser on another site is refused", () => {
  assert.equal(anonymousWriteAllowed("POST", own()), true);
  assert.equal(
    anonymousWriteAllowed("POST", own({ origin: "https://evil.example" })),
    false,
  );
  assert.equal(
    anonymousWriteAllowed("POST", own({ "sec-fetch-site": "cross-site" })),
    false,
  );
  assert.equal(
    anonymousWriteAllowed("POST", own({ "sec-fetch-site": "none" })),
    true,
  );
});

test("the request's host and its secure context", () => {
  assert.equal(
    requestHost(new Headers({ host: "LifeOS.Example.com:443" })),
    "lifeos.example.com",
  );
  assert.equal(
    requestHost(
      new Headers({ host: "dashboard:3000", "x-forwarded-host": "10.8.0.1" }),
    ),
    "10.8.0.1",
  );
  assert.equal(
    isSecureContext(own({ origin: "https://lifeos.example.com" })),
    true,
  );
  assert.equal(
    isSecureContext(new Headers({ origin: "http://localhost:3000" })),
    true,
  );
  assert.equal(
    isSecureContext(new Headers({ origin: "http://10.8.0.1:3000" })),
    false,
  );
});

test("settings: host lists, on and off, and what is required", () => {
  assert.deepEqual(hostList(" 10.8.0.1:3000, Box.Local ,"), [
    "10.8.0.1",
    "box.local",
  ]);
  assert.equal(onOff("ON", false), true);
  assert.equal(onOff("off", true), false);
  assert.equal(onOff("", true), true);
  assert.throws(() => readAuthEnv({}), /LIFE_OS_PUBLIC_URL is not set/);
  assert.throws(
    () => readAuthEnv({ LIFE_OS_PUBLIC_URL: "https://x.example" }),
    /AUTH_DATABASE_URL is not set/,
  );
});

test("the next page after signing in stays on Life OS", () => {
  assert.equal(safeNext("/tasks?view=week"), "/tasks?view=week");
  assert.equal(safeNext(undefined), "/");
  assert.equal(safeNext("https://evil.example"), "/");
  assert.equal(safeNext("//evil.example/x"), "/");
  assert.equal(safeNext("/login?next=/x"), "/");
});

test("setup codes are typed without caring about case or dashes", () => {
  const code = newSetupCode();
  assert.match(code, /^[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}$/);
  assert.doesNotMatch(code, /[01IO]/);
  assert.equal(
    normalizeSetupCode(code.toLowerCase().replace(/-/g, " ")),
    code.replace(/-/g, ""),
  );
});

test("roles and freshness", () => {
  assert.equal(serverRole("owner"), "owner");
  assert.equal(serverRole("root"), "member");
  assert.equal(serverRole(null), "member");
  const now = Date.now();
  assert.equal(
    isFresh({ createdAt: new Date(now - 11 * 3600_000) }, now),
    true,
  );
  assert.equal(
    isFresh({ createdAt: new Date(now - 13 * 3600_000) }, now),
    false,
  );
});

test("the token cache re-mints near expiry and when the version moves", () => {
  const cache = new TokenCache();
  let minted = 0;
  const mint = () => `token-${++minted}`;
  assert.equal(cache.get("s1", "v1", mint, 1000), "token-1");
  assert.equal(cache.get("s1", "v1", mint, 1100), "token-1");
  assert.equal(cache.get("s1", "v1", mint, 1000 + 241), "token-2");
  assert.equal(cache.get("s1", "v2", mint, 1000 + 242), "token-3");
  cache.forget("s1");
  assert.equal(cache.get("s1", "v2", mint, 1000 + 243), "token-4");
});

test("failed sign-ins slow down per username and address", () => {
  const throttle = new LoginThrottle();
  const t = 1_000_000;
  for (let i = 0; i < 5; i += 1) throttle.failed("Anna", "1.2.3.4", t);
  assert.equal(throttle.waitMs("anna", "1.2.3.4", t), 15_000);
  assert.equal(throttle.waitMs("anna", "5.6.7.8", t), 0);
  throttle.failed("anna", "1.2.3.4", t);
  assert.equal(throttle.waitMs("anna", "1.2.3.4", t), 30_000);
  for (let i = 0; i < 20; i += 1) throttle.failed("anna", "1.2.3.4", t);
  assert.equal(throttle.waitMs("anna", "1.2.3.4", t), 15 * 60_000);
  throttle.succeeded("anna", "1.2.3.4");
  assert.equal(throttle.waitMs("anna", "1.2.3.4", t), 0);
});
