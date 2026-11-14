/**
 * Devices, tokens, and MCP (P21-06), in the auth flow suite
 * (docs/operations/ci.md#the-auth-flow-suite): pairing a phone (the QR
 * code and the Custom Tab, expiry, single use, the rate limit), device
 * keys and the watch's child key, revoking them (and their push
 * subscriptions), a banned account's keys, signing out everywhere and
 * ./setup.sh sign-out-all (with --keep-devices), personal API tokens
 * (scopes, expiry, the development tool's actor, and that they cannot
 * manage the account), MCP's OAuth from registration to revocation, the
 * identity token's mfa claim, and a paired phone doing every native call
 * with legacy access off. Against a throwaway PostgreSQL
 * (AUTH_TEST_DATABASE_URL); tools/test-auth runs it.
 */
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { after, before, describe, test } from "node:test";

import { checkRequest } from "../../src/lib/auth/check";
import { FIRST_USER_ID } from "../../src/lib/auth/constants";
import {
  parseTrustedKeys,
  verifyIdentityToken,
} from "../../src/lib/auth/identity";
import { processKeyCleanup } from "../../src/lib/auth/keys";
import {
  consentRequest,
  oauthLoginContinuation,
} from "../../src/lib/auth/oauth-pages";
import * as cli from "../../scripts/lifeos-auth.mjs";
import { totp } from "./authenticators";
import { adminUrl, Browser, PUBLIC_URL, type Reply, sql } from "./harness";
import { seedUser } from "./seed";
import {
  enableTotp,
  password,
  securityRows,
  signIn,
  type World,
  world,
} from "./world";

const admin = adminUrl();
const skip = admin ? false : "AUTH_TEST_DATABASE_URL is not set";

const MCP = `${PUBLIC_URL}/api/mcp`;
const ANDROID_UA =
  "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36";

/** A request as a native client makes it: no cookie, no Origin. */
async function native(
  w: World,
  method: string,
  path: string,
  options: {
    key?: string;
    body?: unknown;
    form?: Record<string, string>;
    headers?: Record<string, string>;
    address?: string;
  } = {},
): Promise<Reply> {
  const headers = new Headers({
    host: new URL(PUBLIC_URL).host,
    "x-real-ip": options.address ?? "203.0.113.7",
    "user-agent": "okhttp/4.12.0",
    ...options.headers,
  });
  if (options.key) headers.set("authorization", `Bearer ${options.key}`);
  let body: string | undefined;
  if (options.form) {
    headers.set("content-type", "application/x-www-form-urlencoded");
    body = new URLSearchParams(options.form).toString();
  } else if (options.body !== undefined) {
    headers.set("content-type", "application/json");
    body = JSON.stringify(options.body);
  }
  const response = await w.fetcher(
    new Request(new URL(path, PUBLIC_URL), {
      method,
      headers,
      body,
      redirect: "manual",
    }),
  );
  const text = await response.text();
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    data = text;
  }
  return { status: response.status, data, headers: response.headers };
}

/** The gateway's auth check for a native client's request. */
function nativeCheck(
  w: World,
  method: string,
  uri: string,
  key?: string,
  extra: Record<string, string> = {},
) {
  return native(w, "GET", "/internal/auth/check", {
    key,
    headers: {
      "x-original-method": method,
      "x-original-uri": uri,
      ...extra,
    },
  });
}

function claims(w: World, reply: Reply) {
  const token = reply.headers.get("x-life-os-identity");
  assert.ok(token, `no identity token (status ${reply.status})`);
  const trusted = parseTrustedKeys(
    JSON.stringify({ keys: [w.keys.publicJwk] }),
  );
  const verified = verifyIdentityToken(token, trusted);
  assert.ok(verified, "the identity token does not verify");
  return verified;
}

async function pairingCode(b: Browser, body: object = {}) {
  const r = await b.req("/api/account/devices/pairing-codes", {
    method: "POST",
    body,
  });
  assert.equal(r.status, 201, JSON.stringify(r.data));
  return r.data as { code: string; expiresAt: string; link: string };
}

async function pairPhone(w: World, b: Browser, name = "Pixel 9") {
  const { code } = await pairingCode(b);
  const r = await native(w, "POST", "/api/auth/devices/pair", {
    body: { code, name },
    address: `198.18.${Math.floor(Math.random() * 250)}.${Math.floor(Math.random() * 250)}`,
  });
  assert.equal(r.status, 200, JSON.stringify(r.data));
  return r.data as { key: string; keyId: string };
}

async function makeToken(b: Browser, body: Record<string, unknown>) {
  const r = await b.req("/api/account/tokens", { method: "POST", body });
  assert.equal(r.status, 201, JSON.stringify(r.data));
  return r.data as { token: string; id: string; actor: string | null };
}

function pkce() {
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  return { verifier, challenge };
}

function decodeJwt(token: string) {
  const [h, p] = token.split(".");
  return {
    header: JSON.parse(Buffer.from(h, "base64url").toString()),
    payload: JSON.parse(Buffer.from(p, "base64url").toString()),
  };
}

function searchParams(location: string) {
  const url = new URL(location, PUBLIC_URL);
  const params: Record<string, string | string[]> = {};
  for (const key of new Set(url.searchParams.keys())) {
    const values = url.searchParams.getAll(key);
    params[key] = values.length > 1 ? values : values[0];
  }
  return { url, params };
}

describe("pairing a phone", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world(admin!);
    await signIn(w.owner, "owner");
  });
  after(() => w?.close());

  test("the QR code's link, redeemed once for the phone's own key", async () => {
    const pairing = await pairingCode(w.owner);
    assert.match(pairing.code, /^[A-HJ-NP-Z2-9]{4}(-[A-HJ-NP-Z2-9]{4}){3}$/);
    const link = new URL(pairing.link);
    assert.equal(link.protocol, "lifeos:");
    assert.equal(
      `${link.protocol}//${link.host}${link.pathname}`,
      "lifeos://pair",
    );
    assert.equal(link.searchParams.get("host"), PUBLIC_URL);
    assert.equal(link.searchParams.get("code"), pairing.code);
    const life = new Date(pairing.expiresAt).getTime() - Date.now();
    assert.ok(life > 4 * 60_000 && life <= 5 * 60_000, `lives ${life} ms`);

    const r = await native(w, "POST", "/api/auth/devices/pair", {
      // Typed by hand: case and dashes do not matter.
      body: {
        code: pairing.code.replace(/-/g, "").toLowerCase(),
        name: " Pixel  9 ",
      },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.match(r.data.key, /^lo_dev_[A-Za-z]{64}$/);
    assert.equal(r.data.kind, "phone");
    assert.equal(r.data.name, "Pixel 9");
    assert.equal(r.data.user.id, FIRST_USER_ID);
    assert.equal(r.data.user.username, "Owner");
    assert.equal(r.data.publicUrl, PUBLIC_URL);
    // Only its hash is stored.
    const { rows } = await w.rt.pool.query(
      "SELECT key FROM apikey WHERE id = $1",
      [r.data.keyId],
    );
    assert.notEqual(rows[0].key, r.data.key);
    assert.ok(!rows[0].key.includes(r.data.key.slice(7, 20)));

    const again = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code: pairing.code, name: "Another phone" },
    });
    assert.equal(again.status, 400);
    assert.equal(again.data.error.code, "pairing_code_invalid");

    const check = await nativeCheck(
      w,
      "GET",
      "/api/tasks/v1/tasks",
      r.data.key,
    );
    assert.equal(check.status, 204);
    const c = claims(w, check);
    assert.equal(c.via, "device");
    assert.equal(c.sid, r.data.keyId);
    assert.equal(c.sub, FIRST_USER_ID);
    assert.equal(c.mfa, undefined);
    assert.equal(c.scp, undefined);

    const paired = (await securityRows(w)).filter(
      (e) => e.kind === "device_paired",
    );
    assert.equal(paired.length, 1);
    assert.equal(paired[0].detail, "phone:qr");
    assert.equal(paired[0].via, "device");
  });

  test("a code expires after 5 minutes", async () => {
    const { code } = await pairingCode(w.owner);
    await sql(
      w.db.url,
      "UPDATE pairing_codes SET created_at = now() - interval '6 minutes', expires_at = now() - interval '1 minute'",
    );
    const r = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code, name: "Late phone" },
      address: "203.0.113.20",
    });
    assert.equal(r.status, 400);
    assert.equal(r.data.error.code, "pairing_code_invalid");
  });

  test("an account has at most 5 unused codes; a sixth replaces the oldest", async () => {
    const codes = [];
    for (let i = 0; i < 6; i += 1)
      codes.push((await pairingCode(w.owner)).code);
    const { rows } = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM pairing_codes WHERE used_at IS NULL AND expires_at > now()",
    );
    assert.equal(rows[0].n, 5);
    const oldest = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code: codes[0], name: "Phone" },
      address: "203.0.113.21",
    });
    assert.equal(oldest.status, 400);
  });

  test("wrong codes are limited per address, and logged grouped", async () => {
    const address = "203.0.113.50";
    const tries = [];
    for (let i = 0; i < 6; i += 1) {
      tries.push(
        await native(w, "POST", "/api/auth/devices/pair", {
          body: { code: "AAAA-BBBB-CCCC-DDDD", name: "Guess" },
          address,
        }),
      );
    }
    assert.deepEqual(
      tries.map((r) => r.status),
      [400, 400, 400, 400, 400, 429],
    );
    assert.equal(tries[5].data.error.code, "too_many_attempts");
    assert.ok(Number(tries[5].headers.get("retry-after")) > 0);
    // Another address is not slowed down.
    const other = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code: "AAAA-BBBB-CCCC-DDDD", name: "Guess" },
      address: "203.0.113.51",
    });
    assert.equal(other.status, 400);
    const refused = (await securityRows(w)).filter(
      (e) => e.kind === "pairing_refused",
    );
    const fromAddress = refused.find((e) => e.address === address);
    assert.ok(fromAddress);
    assert.equal(fromAddress.count, 5);
    assert.equal(fromAddress.detail, "pairing_code_invalid");
  });

  test("the Custom Tab's code needs the app's verifier", async () => {
    const { verifier, challenge } = pkce();
    const pairing = await pairingCode(w.owner, {
      challenge,
      state: "state-12345",
    });
    const link = new URL(pairing.link);
    assert.equal(link.searchParams.get("state"), "state-12345");
    let r = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code: pairing.code, name: "Pixel" },
      address: "203.0.113.60",
    });
    assert.equal(r.status, 400, "without the verifier");
    r = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code: pairing.code, name: "Pixel", verifier: pkce().verifier },
      address: "203.0.113.61",
    });
    assert.equal(r.status, 400, "with another verifier");
    r = await native(w, "POST", "/api/auth/devices/pair", {
      body: { code: pairing.code, name: "Pixel", verifier },
      address: "203.0.113.62",
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const paired = (await securityRows(w)).filter(
      (e) => e.kind === "device_paired",
    );
    assert.equal(paired.at(-1)?.detail, "phone:sign_in");
  });

  test("the pairing endpoints check what they are sent", async () => {
    let r = await w.owner.req("/api/account/devices/pairing-codes", {
      method: "POST",
      body: { challenge: "too-short" },
    });
    assert.equal(r.status, 400);
    r = await w.owner.req("/api/account/devices/pairing-codes", {
      method: "POST",
      body: { challenge: pkce().challenge, state: "a b" },
    });
    assert.equal(r.status, 400);
    const { code } = await pairingCode(w.owner);
    for (const body of [
      { code },
      { code, name: "" },
      { code, name: "x".repeat(65) },
      { code, name: "bell\u0007" },
      { code, name: "Phone", verifier: "short" },
    ]) {
      r = await native(w, "POST", "/api/auth/devices/pair", {
        body,
        address: "203.0.113.70",
      });
      assert.equal(r.status, 400, JSON.stringify(body));
      assert.equal(r.data.error.code, "bad_request");
    }
  });

  test("only a real sign-in makes codes; never a browser on another site", async () => {
    const legacy = await native(
      w,
      "POST",
      "/api/account/devices/pairing-codes",
      {
        body: {},
      },
    );
    assert.equal(legacy.status, 401);
    const { key } = await pairPhone(w, w.owner);
    const withKey = await native(
      w,
      "POST",
      "/api/account/devices/pairing-codes",
      {
        key,
        body: {},
      },
    );
    assert.equal(withKey.status, 401);
    assert.equal(withKey.data.error.code, "sign_in_required");
    const foreign = await w.owner.req("/api/account/devices/pairing-codes", {
      method: "POST",
      body: {},
      headers: { origin: "https://evil.example" },
    });
    assert.equal(foreign.status, 403);
    const pairFromSite = await w.browser().req("/api/auth/devices/pair", {
      method: "POST",
      body: { code: "AAAA-BBBB-CCCC-DDDD", name: "x" },
      headers: { origin: "https://evil.example" },
    });
    assert.equal(pairFromSite.status, 403);
    assert.equal(pairFromSite.data.error.code, "forbidden_origin");
  });
});

describe("device keys and the watch's key", { skip }, () => {
  let w: World;
  const deleted: string[] = [];
  before(async () => {
    w = await world(admin!, {
      legacy: true,
      notifications: async (input, init) => {
        const url = new URL(String(input));
        assert.equal(init?.method, "DELETE");
        deleted.push(url.pathname.split("/").at(-1)!);
        return new Response(null, { status: 204 });
      },
    });
    await signIn(w.owner, "owner");
  });
  after(() => w?.close());

  test("a present but invalid credential never falls back to legacy access or the cookie", async () => {
    // Legacy access is on here: no credential is the owner.
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks")).status,
      204,
    );
    for (const bad of [
      `lo_dev_${"A".repeat(64)}`,
      `lo_tok_${"A".repeat(64)}`,
      "lo_dev_short",
      "not-a-key",
      "a.b.c",
      "",
    ]) {
      const r = await nativeCheck(w, "GET", "/api/tasks/v1/tasks", undefined, {
        authorization: `Bearer ${bad}`,
      });
      assert.equal(r.status, 401, `Bearer ${bad}`);
      assert.equal(r.headers.get("x-life-os-auth-error"), "unauthenticated");
    }
    // With the owner's valid cookie as well: still refused.
    const withCookie = await w.owner.check("GET", "/api/tasks/v1/tasks", {
      authorization: `Bearer lo_dev_${"B".repeat(64)}`,
    });
    assert.equal(withCookie.status, 401);
    // Another scheme is no Life OS credential (a proxy's Basic auth).
    const basic = await nativeCheck(
      w,
      "GET",
      "/api/tasks/v1/tasks",
      undefined,
      {
        authorization: "Basic dXNlcjpwYXNz",
      },
    );
    assert.equal(basic.status, 204);
  });

  test("a device key on every route but MCP's; writes need no Origin", async () => {
    const { key } = await pairPhone(w, w.owner);
    for (const [method, uri, status] of [
      ["GET", "/api/tasks/v1/tasks", 204],
      ["POST", "/api/inbox/v1/inbox-items", 204],
      ["DELETE", "/api/notifications/v1/push/subscriptions/x", 204],
      ["GET", "/android/updates", 204],
      ["GET", "/api/integrations/banking/v1/connections", 204],
      ["POST", "/api/mcp/mcp", 403],
    ] as const) {
      const r = await nativeCheck(w, method, uri, key);
      assert.equal(r.status, status, `${method} ${uri}`);
    }
    // The account API: who it is, but nothing to manage.
    const info = await native(w, "GET", "/api/account", { key });
    assert.equal(info.status, 200);
    assert.equal(info.data.via, "device");
    for (const path of [
      "/api/account/devices",
      "/api/account/tokens",
      "/api/account/sessions",
    ]) {
      const r = await native(w, "GET", path, { key });
      assert.equal(r.status, 401, path);
      assert.equal(r.data.error.code, "sign_in_required");
    }
    const adminApi = await native(w, "GET", "/api/admin/security-log", { key });
    assert.equal(adminApi.status, 403);
  });

  test("the phone makes one key for its watch, which a new one replaces", async () => {
    const { key: phone, keyId: phoneId } = await pairPhone(
      w,
      w.owner,
      "Phone A",
    );
    let r = await native(w, "POST", "/api/auth/devices/child", {
      key: phone,
      body: { name: "Galaxy Watch8" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.kind, "watch");
    assert.equal(r.data.parentKeyId, phoneId);
    const watch = r.data.key as string;
    const watchCheck = await nativeCheck(
      w,
      "POST",
      "/api/voice/v1/audio-jobs",
      watch,
    );
    assert.equal(watchCheck.status, 204);
    assert.equal(claims(w, watchCheck).via, "device");

    r = await native(w, "POST", "/api/auth/devices/child", {
      key: watch,
      body: { name: "Watch of a watch" },
    });
    assert.equal(r.status, 403);
    assert.equal(r.data.error.code, "not_a_phone_key");

    r = await native(w, "GET", "/api/auth/devices/me", { key: watch });
    assert.equal(r.status, 200);
    assert.equal(r.data.kind, "watch");
    assert.equal(r.data.parentKeyId, phoneId);
    assert.equal(r.data.user.id, FIRST_USER_ID);

    deleted.length = 0;
    r = await native(w, "POST", "/api/auth/devices/child", {
      key: phone,
      body: { name: "New watch" },
    });
    assert.equal(r.status, 200);
    const replaced = r.data.key as string;
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", watch)).status,
      401,
    );
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", replaced)).status,
      204,
    );
    await w.rt.keyCleanup!();
    assert.equal(deleted.length, 1, "the old watch's push subscriptions");

    const list = await w.owner.req("/api/account/devices");
    const mine = list.data.devices.filter(
      (d: { id: string; parentId: string | null }) =>
        d.id === phoneId || d.parentId === phoneId,
    );
    assert.deepEqual(
      mine
        .map((d: { kind: string; name: string }) => `${d.kind}:${d.name}`)
        .sort(),
      ["phone:Phone A", "watch:New watch"],
    );
  });

  test("revoking a phone in Settings revokes its watch and deletes their push subscriptions", async () => {
    const { key: phone, keyId: phoneId } = await pairPhone(
      w,
      w.owner,
      "Phone B",
    );
    const child = await native(w, "POST", "/api/auth/devices/child", {
      key: phone,
      body: { name: "Watch B" },
    });
    const watch = child.data.key as string;
    // A use is noted (at most once a minute).
    await nativeCheck(w, "GET", "/api/tasks/v1/tasks", phone);
    await new Promise((resolve) => setTimeout(resolve, 50));
    const used = await w.rt.pool.query(
      'SELECT "lastRequest" FROM apikey WHERE id = $1',
      [phoneId],
    );
    assert.ok(used.rows[0].lastRequest);

    deleted.length = 0;
    const r = await w.owner.req(`/api/account/devices/${phoneId}`, {
      method: "DELETE",
    });
    assert.equal(r.status, 204);
    for (const key of [phone, watch]) {
      assert.equal(
        (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status,
        401,
      );
    }
    await w.rt.keyCleanup!();
    assert.equal(deleted.length, 2);
    assert.ok(deleted.includes(phoneId));
    const left = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM device_key_cleanup",
    );
    assert.equal(left.rows[0].n, 0);
    const revoked = (await securityRows(w)).filter(
      (e) => e.kind === "device_revoked",
    );
    assert.deepEqual(
      revoked
        .slice(-2)
        .map((e) => e.detail)
        .sort(),
      ["phone", "watch"],
    );
    // A token is not a device: the devices endpoint does not revoke it.
    const token = await makeToken(w.owner, {
      name: "script",
      scopes: ["tasks:read"],
    });
    const wrongKind = await w.owner.req(`/api/account/devices/${token.id}`, {
      method: "DELETE",
    });
    assert.equal(wrongKind.status, 404);
  });

  test("Notifications down: the cleanup waits and is retried", async () => {
    const { key, keyId } = await pairPhone(w, w.owner, "Phone C");
    const r = await native(w, "POST", "/api/auth/devices/sign-out", { key });
    assert.equal(r.status, 204);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status,
      401,
    );
    await w.rt.keyCleanup!();
    // Pretend the first try failed, then let a later run catch up.
    await w.rt.pool.query(
      "INSERT INTO device_key_cleanup (key_id) VALUES ($1)",
      [keyId],
    );
    const down = await processKeyCleanup(w.rt.pool, async () => false);
    assert.equal(down, 0);
    const pending = await w.rt.pool.query(
      "SELECT attempts FROM device_key_cleanup WHERE key_id = $1",
      [keyId],
    );
    assert.equal(pending.rows[0].attempts, 1);
    await w.rt.pool.query(
      "UPDATE device_key_cleanup SET last_attempt_at = now() - interval '1 minute'",
    );
    deleted.length = 0;
    await w.rt.keyCleanup!();
    assert.deepEqual(deleted, [keyId]);
  });

  test("a banned account's keys are refused, and disabling it revokes them", async () => {
    const memberId = await seedUser(w.db.url, { username: "carl", password });
    const carl = w.browser();
    assert.equal((await signIn(carl, "carl")).status, 200);
    const { key } = await pairPhone(w, carl, "Carl's phone");
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status,
      204,
    );
    // Banned behind the API's back: the check reads the ban with the key.
    await sql(w.db.url, `UPDATE "user" SET banned = true WHERE id = $1`, [
      memberId,
    ]);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status,
      401,
    );
    await sql(w.db.url, `UPDATE "user" SET banned = false WHERE id = $1`, [
      memberId,
    ]);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status,
      204,
    );

    // The owner disables the account: the key is deleted too.
    const owner = w.browser();
    await signIn(owner, "owner");
    const { uri } = await enableTotp(owner);
    void uri;
    const r = await owner.req("/api/auth/admin/ban-user", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const keys = await w.rt.pool.query(
      `SELECT count(*)::int AS n FROM apikey WHERE "referenceId" = $1`,
      [memberId],
    );
    assert.equal(keys.rows[0].n, 0);
    // A code the banned account made works no more either.
    const { rows } = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM pairing_codes WHERE user_id = $1",
      [memberId],
    );
    assert.equal(rows[0].n, 0);
  });
});

describe("signing out everywhere", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world(admin!);
  });
  after(() => w?.close());

  const host = async (...argv: string[]) => {
    const lines: string[] = [];
    const out = {
      log: (s: string) => lines.push(s),
      error: (s: string) => lines.push(s),
    };
    const code = await cli.main(
      argv,
      { AUTH_DATABASE_URL: w.db.url, LIFE_OS_PUBLIC_URL: PUBLIC_URL },
      out,
    );
    return { code, text: lines.join("\n") };
  };

  test("Settings: every session, device, token, and connected app of the account, at once", async () => {
    const browser = w.browser();
    await signIn(browser, "owner");
    const other = w.browser();
    await signIn(other, "owner");
    const { key } = await pairPhone(w, browser);
    const token = await makeToken(browser, {
      name: "script",
      scopes: ["tasks:read"],
    });
    const r = await browser.req("/api/account/sign-out-everywhere", {
      method: "POST",
    });
    assert.equal(r.status, 204);
    assert.match(
      r.headers.get("set-cookie") ?? "",
      /__Host-lifeos\.session=;.*Max-Age=0/i,
    );
    assert.equal((await other.check()).status, 401);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status,
      401,
    );
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", token.token)).status,
      401,
    );
    const cleanup = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM device_key_cleanup",
    );
    assert.equal(cleanup.rows[0].n, 1);
    const entry = (await securityRows(w)).find(
      (e) => e.kind === "signed_out_everywhere",
    );
    assert.equal(entry?.actor_user_id, FIRST_USER_ID);
  });

  test("./setup.sh sign-out-all ends keys and tokens too; --keep-devices keeps the devices", async () => {
    const browser = w.browser();
    await signIn(browser, "owner");
    const phone = await pairPhone(w, browser, "Kept phone");
    const token = await makeToken(browser, {
      name: "script",
      scopes: ["tasks:read"],
    });
    let printed = await host("sign-out-all", "--keep-devices");
    assert.equal(printed.code, 0, printed.text);
    assert.match(printed.text, /revoked 1 API token/);
    assert.match(printed.text, /Paired phones and watches stay signed in/);
    assert.equal((await browser.check()).status, 401);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", phone.key)).status,
      204,
    );
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", token.token)).status,
      401,
    );

    printed = await host("sign-out-all");
    assert.equal(printed.code, 0, printed.text);
    assert.match(printed.text, /Signed out 1 paired phone/);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", phone.key)).status,
      401,
    );
    const cleanup = await w.rt.pool.query(
      "SELECT 1 FROM device_key_cleanup WHERE key_id = $1",
      [phone.keyId],
    );
    assert.equal(cleanup.rowCount, 1);
  });
});

describe("personal API tokens", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world(admin!, { legacy: true });
    await signIn(w.owner, "owner");
  });
  after(() => w?.close());

  test("made with a name, scopes, and an expiry, and shown once", async () => {
    const r = await w.owner.req("/api/account/tokens", {
      method: "POST",
      body: {
        name: "nightly export",
        scopes: ["tasks:read", "food:read", "food:write"],
      },
    });
    assert.equal(r.status, 201, JSON.stringify(r.data));
    assert.match(r.data.token, /^lo_tok_[A-Za-z]{64}$/);
    assert.deepEqual(r.data.scopes, ["tasks:read", "food:write"]);
    assert.equal(r.data.devTool, false);
    const days =
      (new Date(r.data.expiresAt).getTime() - Date.now()) / 86_400_000;
    assert.ok(days > 89.9 && days <= 90, `expires in ${days} days`);
    const list = await w.owner.req("/api/account/tokens");
    const listed = list.data.tokens.find(
      (t: { id: string }) => t.id === r.data.id,
    );
    assert.ok(listed);
    assert.equal(listed.token, undefined, "never listed again");
    assert.equal(listed.start, r.data.token.slice(0, 11));
    const made = (await securityRows(w)).filter(
      (e) => e.kind === "token_created",
    );
    assert.equal(made.length, 1);
  });

  test("names, scopes, and expiry are checked", async () => {
    const inYear = (days: number) =>
      new Date(Date.now() + days * 86_400_000).toISOString();
    for (const body of [
      { scopes: ["tasks:read"] },
      { name: "x", scopes: [] },
      { name: "x", scopes: ["admin:write"] },
      { name: "x", scopes: ["tasks:delete"] },
      { name: "x", scopes: "tasks:read" },
      { name: "x", scopes: ["tasks:read"], expiresAt: inYear(367) },
      {
        name: "x",
        scopes: ["tasks:read"],
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      },
      { name: "x", scopes: ["tasks:read"], expiresAt: "tomorrow" },
    ]) {
      const r = await w.owner.req("/api/account/tokens", {
        method: "POST",
        body,
      });
      assert.equal(r.status, 400, JSON.stringify(body));
    }
    const year = await makeToken(w.owner, {
      name: "yearly",
      scopes: ["tasks:read"],
      expiresAt: inYear(365),
    });
    assert.ok(year.token);
  });

  test("the check lets a token only where its scopes say", async () => {
    const { token } = await makeToken(w.owner, {
      name: "reader",
      scopes: ["tasks:read", "food:write"],
    });
    for (const [method, uri, status] of [
      ["GET", "/api/tasks/v1/tasks", 204],
      ["POST", "/api/tasks/v1/tasks", 403],
      ["GET", "/api/food/v1/shopping-items", 204],
      ["POST", "/api/food/v1/shopping-items", 204],
      ["POST", "/api/integrations/recipe-import/v1/imports", 204],
      ["GET", "/api/finance/v1/accounts", 403],
      ["GET", "/api/integrations/banking/v1/connections", 403],
      ["GET", "/android/updates", 403],
      ["POST", "/api/mcp/mcp", 403],
      ["GET", "/api/status", 403],
    ] as const) {
      const r = await nativeCheck(w, method, uri, token);
      assert.equal(r.status, status, `${method} ${uri}`);
      if (status === 403) {
        assert.equal(
          r.headers.get("x-life-os-auth-error"),
          "insufficient_scope",
        );
      }
    }
    const ok = await nativeCheck(w, "GET", "/api/tasks/v1/tasks", token);
    const c = claims(w, ok);
    assert.equal(c.via, "token");
    assert.deepEqual(c.scp, ["tasks:read", "food:write"]);
    assert.equal(c.dev, undefined);
    assert.equal(c.mfa, undefined);
  });

  test("a development tool's token acts as dev:<name>", async () => {
    const made = await makeToken(w.owner, {
      name: "claude code",
      scopes: ["tasks:write"],
      devTool: true,
    });
    assert.equal(made.actor, "dev:claude-code");
    const r = await nativeCheck(w, "POST", "/api/tasks/v1/tasks", made.token);
    assert.equal(r.status, 204);
    assert.equal(claims(w, r).dev, "claude-code");
    const entry = (await securityRows(w))
      .filter((e) => e.kind === "token_created")
      .at(-1);
    assert.equal(entry?.detail, "dev_tool");
  });

  test("a token cannot manage the account, devices, or tokens; it can sign itself out", async () => {
    const { token } = await makeToken(w.owner, {
      name: "everything",
      scopes: ["tasks:write", "notifications:write"],
    });
    for (const [method, path] of [
      ["GET", "/api/account/tokens"],
      ["POST", "/api/account/tokens"],
      ["GET", "/api/account/devices"],
      ["POST", "/api/account/devices/pairing-codes"],
      ["GET", "/api/account/sessions"],
      ["POST", "/api/account/legacy-access"],
      ["POST", "/api/account/sign-out-everywhere"],
    ] as const) {
      const r = await native(w, method, path, {
        key: token,
        ...(method === "POST"
          ? { body: { name: "x", scopes: ["tasks:read"] } }
          : {}),
      });
      assert.equal(r.status, 401, `${method} ${path}`);
      assert.equal(r.data.error.code, "sign_in_required");
    }
    // Better Auth reads no bearer: a token is no session there.
    const session = await native(w, "GET", "/api/auth/get-session", {
      key: token,
    });
    assert.ok(
      session.data === null || session.data === "null",
      JSON.stringify(session.data),
    );
    const child = await native(w, "POST", "/api/auth/devices/child", {
      key: token,
      body: { name: "x" },
    });
    assert.equal(child.status, 403);
    const out = await native(w, "POST", "/api/auth/devices/sign-out", {
      key: token,
    });
    assert.equal(out.status, 204);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", token)).status,
      401,
    );
    const revoked = (await securityRows(w)).filter(
      (e) => e.kind === "token_revoked",
    );
    assert.equal(revoked.at(-1)?.detail, "token:self");
  });

  test("an expired or revoked token is refused", async () => {
    const expiring = await makeToken(w.owner, {
      name: "short",
      scopes: ["tasks:read"],
    });
    await sql(
      w.db.url,
      `UPDATE apikey SET "expiresAt" = now() - interval '1 second' WHERE id = $1`,
      [expiring.id],
    );
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", expiring.token))
        .status,
      401,
    );
    const list = await w.owner.req("/api/account/tokens");
    assert.equal(
      list.data.tokens.find((t: { id: string }) => t.id === expiring.id)
        .expired,
      true,
    );
    const revoking = await makeToken(w.owner, {
      name: "gone",
      scopes: ["tasks:read"],
    });
    const r = await w.owner.req(`/api/account/tokens/${revoking.id}`, {
      method: "DELETE",
    });
    assert.equal(r.status, 204);
    assert.equal(
      (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", revoking.token))
        .status,
      401,
    );
  });
});

describe("the mfa claim", { skip }, () => {
  let w: World;
  let uri: string;
  before(async () => {
    w = await world(admin!, { legacy: true });
  });
  after(() => w?.close());

  test("a password-only session has none; the session that turned TOTP on has it", async () => {
    const plain = w.browser();
    await signIn(plain, "owner");
    assert.equal(claims(w, await plain.check()).mfa, undefined);
    const enabling = w.browser();
    await signIn(enabling, "owner");
    ({ uri } = await enableTotp(enabling));
    assert.equal(claims(w, await enabling.check()).mfa, true);
    // The session from before stays without it.
    assert.equal(claims(w, await plain.check()).mfa, undefined);
  });

  test("a sign-in with the authenticator app or a recovery code has it", async () => {
    const b = w.browser();
    const r = await signIn(b, "owner");
    assert.equal(r.data.twoFactorRedirect, true);
    const v = await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(uri) },
    });
    assert.equal(v.status, 200, JSON.stringify(v.data));
    assert.equal(claims(w, await b.check()).mfa, true);
    assert.equal(claims(w, await b.check()).via, "session");
  });

  test("device keys and tokens never pass an admin gate; a two-factor session does", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(uri) },
    });
    assert.equal((await b.req("/api/admin/security-log")).status, 200);
    const { key } = await pairPhone(w, b);
    const token = await makeToken(b, { name: "t", scopes: ["tasks:read"] });
    for (const credential of [key, token.token]) {
      const r = await native(w, "GET", "/api/admin/security-log", {
        key: credential,
      });
      assert.ok([401, 403].includes(r.status), `${r.status}`);
    }
    const status = await native(w, "GET", "/api/status", { key });
    assert.equal(status.status, 200);
    assert.equal(status.data.view, "member");
    const tokenStatus = await native(w, "GET", "/api/status", {
      key: token.token,
    });
    assert.equal(tokenStatus.status, 403);
    // The owner's session without the second factor is told to sign in
    // with it.
    await sql(w.db.url, `UPDATE session SET "secondFactor" = false`);
    w.rt.tokens.clear();
    const refused = await b.req("/api/admin/security-log");
    assert.equal(refused.status, 403);
    assert.equal(refused.data.error.code, "second_factor_sign_in_required");
    const statusView = await b.req("/api/status");
    assert.equal(statusView.data.view, "member");
    assert.equal(statusView.data.reason, "second_factor_sign_in_required");
    const adminEndpoint = await b.req("/api/auth/admin/list-users");
    assert.equal(adminEndpoint.status, 403);
    assert.equal(adminEndpoint.data.code, "SECOND_FACTOR_SIGN_IN_REQUIRED");
  });
});

describe("MCP over OAuth", { skip }, () => {
  let w: World;
  let clientId: string;
  const redirect = "http://127.0.0.1:33418/callback";
  let access: string;
  let refresh: string;
  before(async () => {
    w = await world(admin!);
    await signIn(w.owner, "owner");
  });
  after(() => w?.close());

  test("MCP without a token: 401 with the resource metadata to start OAuth", async () => {
    const r = await nativeCheck(w, "POST", "/api/mcp/mcp");
    assert.equal(r.status, 401);
    const challenge = r.headers.get("www-authenticate") ?? "";
    assert.match(
      challenge,
      /^Bearer resource_metadata="https:\/\/lifeos\.test\/\.well-known\/oauth-protected-resource\/api\/mcp"/,
    );
    assert.match(challenge, /scope="mcp:read mcp:write offline_access"/);
    // Other routes carry none.
    const tasks = await nativeCheck(w, "GET", "/api/tasks/v1/tasks");
    assert.equal(tasks.headers.get("www-authenticate"), null);
  });

  test("the protected resource's metadata names the resource and the server", async () => {
    for (const path of [
      "/.well-known/oauth-protected-resource/api/mcp",
      "/.well-known/oauth-protected-resource",
    ]) {
      const r = await native(w, "GET", path);
      assert.equal(r.status, 200, path);
      assert.equal(r.data.resource, MCP);
      assert.deepEqual(r.data.authorization_servers, [PUBLIC_URL]);
      assert.deepEqual(r.data.scopes_supported, [
        "mcp:read",
        "mcp:write",
        "offline_access",
      ]);
    }
    assert.equal(
      (await native(w, "GET", "/.well-known/oauth-protected-resource/x"))
        .status,
      404,
    );
  });

  test("the authorization server's metadata at the site root", async () => {
    const r = await native(w, "GET", "/.well-known/oauth-authorization-server");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.issuer, PUBLIC_URL);
    assert.equal(
      r.data.authorization_endpoint,
      `${PUBLIC_URL}/api/auth/oauth2/authorize`,
    );
    assert.equal(r.data.token_endpoint, `${PUBLIC_URL}/api/auth/oauth2/token`);
    assert.equal(
      r.data.registration_endpoint,
      `${PUBLIC_URL}/api/auth/oauth2/register`,
    );
    assert.deepEqual(r.data.code_challenge_methods_supported, ["S256"]);
    assert.ok(r.data.scopes_supported.includes("mcp:write"));
    assert.ok(r.data.token_endpoint_auth_methods_supported.includes("none"));
    assert.ok(!r.data.grant_types_supported.includes("client_credentials"));
  });

  test("dynamic client registration: loopback redirects only over plain HTTP", async () => {
    const register = (uris: string[]) =>
      native(w, "POST", "/api/auth/oauth2/register", {
        body: {
          client_name: "Scripted MCP client",
          redirect_uris: uris,
          token_endpoint_auth_method: "none",
          grant_types: ["authorization_code", "refresh_token"],
          response_types: ["code"],
          scope: "mcp:read mcp:write offline_access",
        },
        address: `198.51.100.${10 + Math.floor(Math.random() * 200)}`,
      });
    let r = await register([redirect]);
    assert.ok([200, 201].includes(r.status), JSON.stringify(r.data));
    clientId = r.data.client_id;
    assert.ok(clientId);
    assert.equal(r.data.client_secret, undefined);
    for (const ok of [
      "http://localhost:4242/cb",
      "http://[::1]:4242/cb",
      "https://claude.ai/api/mcp/auth_callback",
    ]) {
      r = await register([ok]);
      assert.ok(
        [200, 201].includes(r.status),
        `${ok}: ${JSON.stringify(r.data)}`,
      );
    }
    for (const bad of [
      "http://evil.example/cb",
      "http://10.0.0.5/cb",
      "http://127.0.0.1.evil.example/cb",
    ]) {
      r = await register([bad]);
      assert.equal(r.status, 400, `${bad}: ${JSON.stringify(r.data)}`);
    }
    const registered = (await securityRows(w)).filter(
      (e) => e.kind === "oauth_client_registered",
    );
    assert.ok(registered.length >= 4);
  });

  test("authorize with PKCE, sign in, consent, and the code for tokens", async () => {
    const { verifier, challenge } = pkce();
    const query = new URLSearchParams({
      response_type: "code",
      client_id: clientId,
      redirect_uri: redirect,
      scope: "mcp:read mcp:write offline_access",
      state: "xyz-state",
      code_challenge: challenge,
      code_challenge_method: "S256",
      resource: MCP,
    });
    const navigation = { accept: "text/html", "sec-fetch-mode": "navigate" };
    // Not signed in: to the login page, with the request signed.
    const stranger = w.browser();
    let r = await stranger.req(`/api/auth/oauth2/authorize?${query}`, {
      headers: navigation,
    });
    assert.equal(r.status, 302, JSON.stringify(r.data));
    const login = searchParams(r.headers.get("location")!);
    assert.equal(login.url.pathname, "/login");
    const next = await oauthLoginContinuation(w.rt, login.params);
    assert.ok(next?.startsWith("/api/auth/oauth2/authorize?"), next ?? "");
    // A request changed on the way is not continued.
    assert.equal(
      await oauthLoginContinuation(w.rt, {
        ...login.params,
        redirect_uri: "http://127.0.0.1:1/x",
      }),
      null,
    );

    // Signed in, the login page goes on: to the consent page.
    r = await w.owner.req(next!, { headers: navigation });
    assert.equal(r.status, 302, JSON.stringify(r.data));
    const consent = searchParams(r.headers.get("location")!);
    assert.equal(consent.url.pathname, "/oauth/consent");
    const request = await consentRequest(w.rt, consent.params);
    assert.equal(request.ok, true);
    assert.ok(request.ok);
    assert.equal(request.clientName, "Scripted MCP client");
    assert.equal(request.redirectHost, "127.0.0.1:33418");
    assert.deepEqual(request.scopes, ["mcp:read", "mcp:write"]);
    assert.equal(request.offlineAccess, true);
    const forged = await consentRequest(w.rt, {
      ...consent.params,
      scope: "mcp:write",
    });
    assert.equal(forged.ok, false);

    r = await w.owner.req("/api/auth/oauth2/consent", {
      method: "POST",
      body: {
        accept: true,
        scope: "mcp:read mcp:write offline_access",
        oauth_query: request.oauthQuery,
      },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const back = new URL(r.data.url);
    assert.equal(`${back.origin}${back.pathname}`, redirect);
    assert.equal(back.searchParams.get("state"), "xyz-state");
    const code = back.searchParams.get("code")!;
    assert.ok(code);
    const given = (await securityRows(w)).filter(
      (e) => e.kind === "oauth_consent_given",
    );
    assert.equal(given.at(-1)?.detail, "mcp:read_write");

    // Without the verifier, the code is refused.
    r = await native(w, "POST", "/api/auth/oauth2/token", {
      form: {
        grant_type: "authorization_code",
        code,
        redirect_uri: redirect,
        client_id: clientId,
        code_verifier: pkce().verifier,
        resource: MCP,
      },
    });
    assert.equal(r.status, 400, JSON.stringify(r.data));

    // A new code, since a refused exchange may burn it.
    r = await w.owner.req(`/api/auth/oauth2/authorize?${query}`, {
      headers: navigation,
    });
    assert.equal(r.status, 302);
    const code2 = new URL(r.headers.get("location")!).searchParams.get("code");
    assert.ok(code2, r.headers.get("location") ?? "");
    r = await native(w, "POST", "/api/auth/oauth2/token", {
      form: {
        grant_type: "authorization_code",
        code: code2!,
        redirect_uri: redirect,
        client_id: clientId,
        code_verifier: verifier,
        resource: MCP,
      },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    access = r.data.access_token;
    refresh = r.data.refresh_token;
    assert.ok(refresh, "a refresh token (offline_access)");
    assert.equal(r.data.expires_in, 900);
    const jwt = decodeJwt(access);
    assert.equal(jwt.header.alg, "EdDSA");
    assert.equal(jwt.header.typ, "at+jwt");
    assert.equal(jwt.payload.aud, MCP);
    assert.equal(jwt.payload.iss, PUBLIC_URL);
    assert.equal(jwt.payload.sub, FIRST_USER_ID);
  });

  test("the access token is the person on MCP, with the grant's scopes, and nowhere else", async () => {
    const r = await nativeCheck(w, "POST", "/api/mcp/mcp", access);
    assert.equal(r.status, 204);
    const c = claims(w, r);
    assert.equal(c.via, "mcp");
    assert.equal(c.sub, FIRST_USER_ID);
    assert.ok((c.scp as string[]).includes("tasks:write"));
    assert.ok((c.scp as string[]).includes("food:write"));
    assert.equal(c.mfa, undefined);
    const grant = await w.rt.pool.query(
      `SELECT id FROM "oauthConsent" WHERE "clientId" = $1`,
      [clientId],
    );
    assert.equal(c.sid, grant.rows[0].id);
    for (const uri of ["/api/tasks/v1/tasks", "/android/updates"]) {
      assert.equal((await nativeCheck(w, "GET", uri, access)).status, 401, uri);
    }
    // Tampered: refused, with the challenge saying so.
    const [h, p, s] = access.split(".");
    const forged = `${h}.${Buffer.from(
      JSON.stringify({
        ...decodeJwt(access).payload,
        sub: "00000000-0000-4000-8000-000000000009",
      }),
    ).toString("base64url")}.${s}`;
    void p;
    const bad = await nativeCheck(w, "POST", "/api/mcp/mcp", forged);
    assert.equal(bad.status, 401);
    assert.match(
      bad.headers.get("www-authenticate") ?? "",
      /error="invalid_token"/,
    );
  });

  test("refresh tokens rotate", async () => {
    const form = {
      grant_type: "refresh_token",
      refresh_token: refresh,
      client_id: clientId,
      resource: MCP,
    };
    let r = await native(w, "POST", "/api/auth/oauth2/token", { form });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.notEqual(r.data.refresh_token, refresh);
    const rotated = r.data.refresh_token;
    access = r.data.access_token;
    assert.equal(
      (await nativeCheck(w, "POST", "/api/mcp/mcp", access)).status,
      204,
    );
    // The old one is spent.
    r = await native(w, "POST", "/api/auth/oauth2/token", { form });
    assert.equal(r.status, 400, JSON.stringify(r.data));
    refresh = rotated;
  });

  test("Connected apps lists it; disconnecting works at once, for the access and the refresh token", async () => {
    const list = await w.owner.req("/api/account/connected-apps");
    assert.equal(list.status, 200);
    const app = list.data.apps.find(
      (a: { clientId: string }) => a.clientId === clientId,
    );
    assert.ok(app);
    assert.equal(app.name, "Scripted MCP client");
    // Disconnecting the rotated refresh token's grant also covers it.
    const r = await w.owner.req(`/api/account/connected-apps/${clientId}`, {
      method: "DELETE",
    });
    assert.equal(r.status, 204);
    assert.equal(
      (await nativeCheck(w, "POST", "/api/mcp/mcp", access)).status,
      401,
    );
    const again = await native(w, "POST", "/api/auth/oauth2/token", {
      form: {
        grant_type: "refresh_token",
        refresh_token: refresh,
        client_id: clientId,
        resource: MCP,
      },
    });
    assert.equal(again.status, 400);
    const revoked = (await securityRows(w)).filter(
      (e) => e.kind === "oauth_consent_revoked",
    );
    assert.equal(revoked.length, 1);
  });
});

describe(
  "legacy access off: a paired phone and the TWA do what the phone did",
  { skip },
  () => {
    let w: World;
    before(async () => {
      w = await world(admin!, { legacy: false });
      await signIn(w.owner, "owner");
    });
    after(() => w?.close());

    test("every native call, the watch's relay, and the downloads, with the keys", async () => {
      const { key: phone } = await pairPhone(w, w.owner);
      const child = await native(w, "POST", "/api/auth/devices/child", {
        key: phone,
        body: { name: "Galaxy Watch8" },
      });
      const watch = child.data.key as string;
      const calls: [string, string][] = [
        ["GET", "/api/tasks/v1/tasks?completed=false&limit=20"],
        [
          "POST",
          "/api/tasks/v1/tasks/0f8fad5b-d9cb-469f-a165-70867728950e/complete",
        ],
        [
          "GET",
          "/api/calendar/v1/events?from=2026-11-14T00%3A00%3A00Z&to=2026-11-15T00%3A00%3A00Z",
        ],
        ["POST", "/api/inbox/v1/inbox-items"],
        ["POST", "/api/inbox/v1/media"],
        ["POST", "/api/voice/v1/audio-jobs"],
        [
          "GET",
          "/api/voice/v1/audio-jobs/0f8fad5b-d9cb-469f-a165-70867728950e",
        ],
        ["GET", "/api/notifications/v1/push/config"],
        ["POST", "/api/notifications/v1/push/subscriptions"],
        [
          "DELETE",
          "/api/notifications/v1/push/subscriptions/0f8fad5b-d9cb-469f-a165-70867728950e",
        ],
        ["GET", "/android/updates"],
        ["GET", "/android/lifeos-phone-0.6.0-12.apk"],
        ["GET", "/android/lifeos-wear-0.6.0-12.apk"],
      ];
      for (const [method, uri] of calls) {
        assert.equal(
          (await nativeCheck(w, method, uri)).status,
          401,
          `no key: ${method} ${uri}`,
        );
        for (const [who, key] of [
          ["phone", phone],
          ["watch", watch],
        ]) {
          const r = await nativeCheck(w, method, uri, key, {
            "user-agent": ANDROID_UA,
          });
          assert.equal(r.status, 204, `${who}: ${method} ${uri}`);
        }
      }
    });

    test("the TWA's cookie: the share sheet and web push, as the person", async () => {
      // The share sheet's POST: a same-origin navigation of the installed app.
      const share = await checkRequest(
        w.rt,
        "POST",
        new Headers({
          cookie: w.owner.cookieHeader(),
          "sec-fetch-site": "same-origin",
          host: "lifeos.test",
        }),
      );
      assert.equal(share.status, 204);
      assert.ok(share.status === 204 && share.principal.via === "session");
      const push = await w.owner.check(
        "POST",
        "/api/notifications/v1/push/subscriptions",
      );
      assert.equal(push.status, 204);
      // From another site, never.
      const foreign = await w.owner.check(
        "POST",
        "/api/notifications/v1/push/subscriptions",
        {
          origin: "https://evil.example",
        },
      );
      assert.equal(foreign.status, 403);
    });

    test("the account's own pages need the web sign-in", async () => {
      assert.equal(
        (await native(w, "GET", "/api/account/devices")).status,
        401,
      );
      assert.equal(
        (
          await native(w, "POST", "/api/auth/devices/pair", {
            body: { code: "AAAA-BBBB-CCCC-DDDD", name: "x" },
            address: "203.0.113.99",
          })
        ).status,
        400,
      );
      // The owner's own key cannot turn legacy access back on, nor can the web.
      const on = await w.owner.req("/api/account/legacy-access", {
        method: "POST",
        body: { enabled: true },
      });
      assert.equal(on.status, 403);
      assert.equal(on.data.error.code, "host_only");
    });
  },
);
