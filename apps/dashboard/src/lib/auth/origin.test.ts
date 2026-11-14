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
  assert.equal(
    safeNext("/calendar?date=2026-11-14#event"),
    "/calendar?date=2026-11-14#event",
  );
  assert.equal(safeNext(["/food", "/tasks"]), "/food");
  assert.equal(safeNext(undefined), "/");
  assert.equal(safeNext(""), "/");
  assert.equal(safeNext("tasks"), "/");
  assert.equal(safeNext("https://evil.example"), "/");
  assert.equal(safeNext("javascript:alert(1)"), "/");
  assert.equal(safeNext("/login?next=/x"), "/");
  assert.equal(safeNext("/setup"), "/");
  assert.equal(safeNext("/reset/abc"), "/");
  // Another site, however it is spelled: browsers drop tabs and newlines
  // and read a backslash as a slash.
  for (const attempt of [
    "//evil.example/x",
    "/\\evil.example",
    "/\\/evil.example",
    "\\/evil.example",
    "/\t/evil.example",
    "/\n/evil.example",
    "/\r/evil.example",
    "/\t\t/evil.example",
    "/\u0000/evil.example",
    "/\u007f/evil.example",
    "/%09/evil.example".replace("%09", "\t"),
  ]) {
    assert.equal(safeNext(attempt), "/", JSON.stringify(attempt));
  }
  // Still encoded (the query was decoded once already), they stay paths on
  // Life OS.
  assert.equal(safeNext("/%2F%2Fevil.example"), "/%2F%2Fevil.example");
  assert.equal(safeNext("/%09/evil.example"), "/%09/evil.example");
  assert.equal(safeNext("/%5Cevil.example"), "/%5Cevil.example");
  for (const attempt of [
    "/%2F%2Fevil.example",
    "/%09/evil.example",
    "/%5C%5Cevil.example",
  ]) {
    assert.equal(
      new URL(safeNext(attempt), "https://lifeos.example.com").origin,
      "https://lifeos.example.com",
      attempt,
    );
  }
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

test("the token cache hands out only young tokens, and re-mints when the version moves", () => {
  const cache = new TokenCache();
  let minted = 0;
  const mint = () => `token-${++minted}`;
  assert.equal(cache.get("s1", "v1", mint, 1000), "token-1");
  // More than 4 of its 5 minutes left: reused.
  assert.equal(cache.get("s1", "v1", mint, 1059), "token-1");
  // 4 or less: a slow upload could outlive it, so a new one.
  assert.equal(cache.get("s1", "v1", mint, 1061), "token-2");
  assert.equal(cache.get("s1", "v2", mint, 1062), "token-3");
  cache.forget("s1");
  assert.equal(cache.get("s1", "v2", mint, 1063), "token-4");
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
