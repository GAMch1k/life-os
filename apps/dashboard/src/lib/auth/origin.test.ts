import assert from "node:assert/strict";
import test from "node:test";

import { hostList, onOff, readAuthEnv } from "./env";
import { TokenCache } from "./identity";
import {
  anonymousWriteAllowed,
  cookieWriteAllowed,
  isLoopbackLogin,
  isLoopbackOrigin,
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
      new Headers({ host: "10.8.0.1", origin: "http://10.8.0.1:3000" }),
    ),
    true,
  );
  // So on loopback, where another web app on this machine shares the host
  // name, only Sec-Fetch-Site tells Life OS's own page from another port's.
  const loopback = (extra: Record<string, string>) =>
    new Headers({ host: "127.0.0.1", ...extra });
  assert.equal(
    cookieWriteAllowed("POST", loopback({ origin: "http://127.0.0.1:3000" })),
    false,
  );
  assert.equal(
    cookieWriteAllowed(
      "POST",
      loopback({
        origin: "http://127.0.0.1:8080",
        "sec-fetch-site": "same-site",
      }),
    ),
    false,
  );
  assert.equal(
    cookieWriteAllowed(
      "POST",
      loopback({
        origin: "http://127.0.0.1:3000",
        "sec-fetch-site": "same-origin",
      }),
    ),
    true,
  );
  assert.equal(
    anonymousWriteAllowed(
      "POST",
      loopback({ origin: "http://127.0.0.1:8080" }),
    ),
    false,
  );
  assert.equal(
    anonymousWriteAllowed(
      "POST",
      loopback({
        origin: "http://127.0.0.1:8080",
        "sec-fetch-site": "same-site",
      }),
    ),
    false,
  );
  assert.equal(
    anonymousWriteAllowed(
      "POST",
      loopback({
        origin: "http://localhost:3000",
        host: "localhost",
        "sec-fetch-site": "same-origin",
      }),
    ),
    true,
  );
  // Sec-Fetch-Site is not enough on its own: Origin must still be Life
  // OS's host, with the cookie and without.
  assert.equal(
    cookieWriteAllowed(
      "POST",
      loopback({
        origin: "https://elsewhere.example",
        "sec-fetch-site": "same-origin",
      }),
    ),
    false,
  );
  assert.equal(
    anonymousWriteAllowed(
      "POST",
      loopback({
        origin: "https://elsewhere.example",
        "sec-fetch-site": "same-origin",
      }),
    ),
    false,
  );
  // A client that is not a browser (no Origin, no Sec-Fetch-Site) stays
  // as it was; behind HTTPS, loopback is judged as any host.
  assert.equal(anonymousWriteAllowed("POST", loopback({})), true);
  assert.equal(
    cookieWriteAllowed(
      "POST",
      loopback({
        origin: "https://127.0.0.1",
        "x-forwarded-proto": "https",
      }),
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

test("a null Origin needs Sec-Fetch-Site same-origin or none, with the cookie and without", () => {
  // A plain-HTTP LAN or WireGuard address, which is not a secure context:
  // browsers send no Sec-Fetch-Site there, so a sandboxed iframe on any web
  // page (or a cross-origin redirect) posts with Origin: null and nothing
  // else. Under legacy access it would write as the owner.
  const lan = (extra: Record<string, string> = {}) =>
    new Headers({
      host: "dashboard:3000",
      "x-forwarded-host": "10.66.66.1",
      "x-forwarded-proto": "http",
      ...extra,
    });
  const loopback = (extra: Record<string, string> = {}) =>
    new Headers({ host: "127.0.0.1", ...extra });
  for (const headers of [lan, loopback, own]) {
    const where = headers().get("x-forwarded-host") ?? headers().get("host");
    for (const method of ["POST", "PUT", "PATCH", "DELETE"]) {
      assert.equal(
        anonymousWriteAllowed(method, headers({ origin: "null" })),
        false,
        `${where} ${method}, no credential`,
      );
      assert.equal(
        cookieWriteAllowed(method, headers({ origin: "null" })),
        false,
        `${where} ${method}, the cookie`,
      );
    }
    for (const site of ["same-origin", "none"]) {
      // A navigation the person started (the share sheet), or Life OS's
      // own page.
      assert.equal(
        anonymousWriteAllowed(
          "POST",
          headers({ origin: "null", "sec-fetch-site": site }),
        ),
        true,
        `${where} ${site}, no credential`,
      );
      assert.equal(
        cookieWriteAllowed(
          "POST",
          headers({ origin: "null", "sec-fetch-site": site }),
        ),
        true,
        `${where} ${site}, the cookie`,
      );
    }
    for (const site of ["cross-site", "same-site"]) {
      assert.equal(
        anonymousWriteAllowed(
          "POST",
          headers({ origin: "null", "sec-fetch-site": site }),
        ),
        false,
        `${where} ${site}`,
      );
    }
    // Reads are not writes.
    assert.equal(
      anonymousWriteAllowed("GET", headers({ origin: "null" })),
      true,
    );
  }
  // A client that is not a browser sends no Origin at all: the phone app's
  // native calls (pairing), scripts, and MCP clients, unchanged.
  assert.equal(anonymousWriteAllowed("POST", lan()), true);
  assert.equal(anonymousWriteAllowed("POST", loopback()), true);
  // Nor is an empty Origin header a null one.
  assert.equal(anonymousWriteAllowed("POST", lan({ origin: "" })), true);
  // Life OS's own page at that address still writes, as does a foreign
  // one's refusal stay.
  assert.equal(
    anonymousWriteAllowed("POST", lan({ origin: "http://10.66.66.1:3001" })),
    true,
  );
  assert.equal(
    anonymousWriteAllowed("POST", lan({ origin: "http://evil.example" })),
    false,
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

test("loopback login: this machine's own names over plain HTTP, nothing else", () => {
  const via = (host: string, extra: Record<string, string> = {}) =>
    new Headers({ host: "dashboard:3000", "x-forwarded-host": host, ...extra });
  for (const host of ["127.0.0.1", "localhost", "LOCALHOST", "[::1]"]) {
    assert.equal(isLoopbackLogin(via(host)), true, host);
  }
  for (const host of [
    "lifeos.example.com",
    "10.8.0.1",
    "evil.localhost",
    "127.0.0.1.nip.io",
    "localhost.example.com",
    "127.0.0.2",
  ]) {
    assert.equal(isLoopbackLogin(via(host)), false, host);
  }
  // Behind HTTPS (Cloudflare's tunnel sets the proto): the Secure cookie.
  assert.equal(
    isLoopbackLogin(via("localhost", { "x-forwarded-proto": "https" })),
    false,
  );
  assert.equal(isLoopbackOrigin("http://127.0.0.1:3000"), true);
  assert.equal(isLoopbackOrigin("http://[::1]:3000"), true);
  assert.equal(isLoopbackOrigin("http://evil.localhost:3000"), false);
  assert.equal(isLoopbackOrigin("http://10.8.0.1:3000"), false);
  assert.equal(isLoopbackOrigin("null"), false);
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
    decodeURIComponent("/%09/evil.example"),
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
