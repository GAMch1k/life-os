/**
 * The auth flow suite (docs/operations/ci.md#the-auth-flow-suite): the login
 * trial's 45 checks (P21-01) as P21-03 answers them, and what P21-03 adds:
 * the setup code, legacy access, the auth check and its identity tokens,
 * CSRF and Origin, rate limits, a banned user, signing everyone out, and the
 * host's commands. Against a throwaway PostgreSQL (AUTH_TEST_DATABASE_URL);
 * tools/test-auth runs it, and CI's integration job runs that.
 */
import assert from "node:assert/strict";
import { after, before, describe, test } from "node:test";

import { getMigrations } from "better-auth/db/migration";
import { verifyPassword as betterAuthVerify } from "better-auth/crypto";

import { FIRST_SPACE_ID, FIRST_USER_ID } from "../../src/lib/auth/constants";
import {
  createSigner,
  parseTrustedKeys,
  verifyIdentityToken,
} from "../../src/lib/auth/identity";
import { hashPassword, verifyPassword } from "../../src/lib/auth/password";
import { createSetupCode } from "../../src/lib/auth/store";
import { SoftAuthenticator, totp } from "./authenticators";
import {
  adminUrl,
  app,
  Browser,
  freshDatabase,
  PUBLIC_URL,
  type Reply,
  runtime,
  type RuntimeOptions,
  sql,
  testKeys,
} from "./harness";
import * as cli from "../../scripts/lifeos-auth.mjs";
import { seedUser } from "./seed";

const admin = adminUrl();
const skip = admin ? false : "AUTH_TEST_DATABASE_URL is not set";
const password = "correct horse battery staple 42";

type World = Awaited<ReturnType<typeof world>>;

/** A database, the auth runtime, and the owner's account, signed in. */
async function world(options: RuntimeOptions & { owner?: boolean } = {}) {
  const db = await freshDatabase(admin!);
  const keys = options.keys ?? testKeys();
  const rt = runtime(db.url, { ...options, keys });
  const fetcher = app(rt);
  const owner = new Browser(fetcher);
  if (options.owner !== false) {
    const code = await createSetupCode(rt.pool);
    const r = await owner.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code! },
      body: { username: "Owner", password, name: "Owner" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
  }
  return {
    db,
    rt,
    keys,
    fetcher,
    owner,
    trusted: parseTrustedKeys(JSON.stringify({ keys: [keys.publicJwk] })),
    browser: (origin?: string, address?: string) =>
      new Browser(fetcher, origin, address),
    async close() {
      await rt.pool.end();
      await db.drop();
    },
  };
}

function identity(w: World, reply: Reply) {
  const token = reply.headers.get("x-life-os-identity");
  assert.ok(token, `no identity token (status ${reply.status})`);
  const claims = verifyIdentityToken(token, w.trusted);
  assert.ok(claims, "the identity token does not verify");
  return { token, claims };
}

async function signIn(b: Browser, username: string, pw = password) {
  return b.req("/api/auth/sign-in/username", {
    method: "POST",
    body: { username, password: pw },
  });
}

/** Turns on TOTP for a signed-in browser; returns the otpauth URI and codes. */
async function enableTotp(b: Browser, pw = password) {
  let r = await b.req("/api/auth/two-factor/enable", {
    method: "POST",
    body: { password: pw },
  });
  assert.equal(r.status, 200, JSON.stringify(r.data));
  const uri = r.data.totpURI as string;
  const codes = r.data.backupCodes as string[];
  r = await b.req("/api/auth/two-factor/verify-totp", {
    method: "POST",
    body: { code: totp(uri) },
  });
  assert.equal(r.status, 200, JSON.stringify(r.data));
  return { uri, codes };
}

describe("the first account", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world({ owner: false });
  });
  after(() => w?.close());

  test("sign-up without the setup code is refused", async () => {
    const r = await w.browser().req("/api/auth/sign-up/email", {
      method: "POST",
      body: { username: "owner", password },
    });
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "SETUP_CODE_INVALID");
  });

  test("an expired setup code is refused", async () => {
    const code = await createSetupCode(w.rt.pool);
    await sql(
      w.db.url,
      "UPDATE setup_codes SET expires_at = now() - interval '1 minute'",
    );
    const r = await w.browser().req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code! },
      body: { username: "owner", password },
    });
    assert.equal(r.status, 403);
  });

  test("a new setup code replaces the old one", async () => {
    const first = await createSetupCode(w.rt.pool);
    const second = await createSetupCode(w.rt.pool);
    const r = await w.browser().req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": first! },
      body: { username: "owner", password },
    });
    assert.equal(r.status, 403);
    assert.notEqual(first, second);
  });

  test("a sign-up that fails on its input leaves the setup code working", async () => {
    const code = await createSetupCode(w.rt.pool);
    const r = await w.browser().req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code! },
      body: { username: "owner", password: "short" },
    });
    assert.equal(r.status, 400, JSON.stringify(r.data));
    const { rows } = await w.rt.pool.query("SELECT used_at FROM setup_codes");
    assert.deepEqual(rows, [{ used_at: null }]);
  });

  test("the setup code creates the owner, with the fixed ids, without an email", async () => {
    const { rows } = await w.rt.pool.query(
      "SELECT 1 FROM setup_codes WHERE used_at IS NULL",
    );
    assert.equal(rows.length, 1);
    const code = await createSetupCode(w.rt.pool);
    // Lowercase and without dashes, as someone may type it.
    const typed = code!.replace(/-/g, "").toLowerCase();
    const r = await w.owner.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": typed },
      body: { username: "Owner", password, name: "" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.user.id, FIRST_USER_ID);
    assert.equal(r.data.user.role, "owner");
    assert.match(r.data.user.email, /^u-[0-9a-f]{18}@users\.invalid$/);
    assert.equal(r.data.user.username, "owner");
    const set = w.owner.jar;
    assert.ok(set.has("__Host-lifeos.session"), [...set.keys()].join());
    assert.equal((await w.owner.check()).status, 204);
    const used = await w.rt.pool.query(
      "SELECT used_by FROM setup_codes WHERE used_at IS NOT NULL",
    );
    assert.deepEqual(
      used.rows.map((row) => row.used_by),
      [FIRST_USER_ID],
    );
  });

  test("the setup code works once, and no second account can sign up", async () => {
    const code = await createSetupCode(w.rt.pool);
    assert.equal(code, null, "no setup code while an account exists");
    const r = await w.browser().req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": "AAAA-BBBB-CCCC-DDDD" },
      body: { username: "second", password, email: "x@example.com" },
    });
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "REGISTRATION_CLOSED");
  });

  test("two sign-ups racing with one setup code make one account", async () => {
    const raced = await world({ owner: false });
    try {
      const code = await createSetupCode(raced.rt.pool);
      const names = ["ann", "ben", "cid", "dot", "eve", "fay"];
      const replies = await Promise.all(
        names.map((username) =>
          raced.browser().req("/api/auth/sign-up/email", {
            method: "POST",
            headers: { "x-lifeos-setup-code": code! },
            body: { username, password },
          }),
        ),
      );
      const statuses = replies.map((r) => r.status);
      assert.equal(
        statuses.filter((status) => status === 200).length,
        1,
        statuses.join(),
      );
      for (const r of replies.filter((reply) => reply.status !== 200)) {
        assert.equal(r.status, 403, JSON.stringify(r.data));
        assert.match(r.data.code, /^(SETUP_CODE_INVALID|REGISTRATION_CLOSED)$/);
      }
      const users = await raced.rt.pool.query('SELECT id, role FROM "user"');
      assert.deepEqual(users.rows, [{ id: FIRST_USER_ID, role: "owner" }]);
      const used = await raced.rt.pool.query(
        "SELECT used_by FROM setup_codes WHERE used_at IS NOT NULL",
      );
      assert.deepEqual(
        used.rows.map((row) => row.used_by),
        [FIRST_USER_ID],
      );
    } finally {
      await raced.close();
    }
  });

  test("the session cookie is host-only, Secure, HttpOnly, and SameSite=Lax", async () => {
    const r = await new Browser(w.fetcher).req("/api/auth/sign-in/username", {
      method: "POST",
      body: { username: "owner", password },
    });
    const cookie = r.headers
      .getSetCookie()
      .find((c) => c.startsWith("__Host-lifeos.session="));
    assert.ok(cookie, r.headers.getSetCookie().join(" | "));
    assert.match(cookie, /; Path=\//i);
    assert.match(cookie, /; Secure/i);
    assert.match(cookie, /; HttpOnly/i);
    assert.match(cookie, /; SameSite=Lax/i);
    assert.doesNotMatch(cookie, /Domain=/i);
    assert.match(cookie, /Max-Age=2592000/i);
  });
});

describe("sign-in, two-factor, passkeys", { skip }, () => {
  let w: World;
  let uri: string;
  let codes: string[];
  const authn = new SoftAuthenticator();
  before(async () => {
    w = await world();
  });
  after(() => w?.close());

  test("password sign-in by username, any case", async () => {
    const b = w.browser();
    const r = await signIn(b, "OWNER");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await b.check()).status, 204);
  });

  test("a wrong password is refused, and email sign-in is not offered", async () => {
    let r = await signIn(w.browser(), "owner", "wrong password 123");
    assert.equal(r.status, 401);
    r = await w.browser().req("/api/auth/sign-in/email", {
      method: "POST",
      body: { email: "owner@users.invalid", password },
    });
    assert.equal(r.status, 404);
    r = await w.browser().req("/api/auth/request-password-reset", {
      method: "POST",
      body: { email: "owner@users.invalid" },
    });
    assert.equal(r.status, 404);
  });

  test("TOTP: enrolment gives an otpauth URI and ten recovery codes", async () => {
    ({ uri, codes } = await enableTotp(w.owner));
    assert.match(uri, /^otpauth:\/\/totp\//);
    assert.equal(codes.length, 10);
    assert.equal(new Set(codes).size, 10);
  });

  test("TOTP: sign-in asks for the code, and only the code completes it", async () => {
    const b = w.browser();
    let r = await signIn(b, "owner");
    assert.equal(r.data.twoFactorRedirect, true, JSON.stringify(r.data));
    assert.equal((await b.check()).status, 401);
    r = await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: "000000" === totp(uri) ? "111111" : "000000" },
    });
    assert.ok(r.status >= 400);
    r = await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(uri) },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await b.check()).status, 204);
  });

  test("a recovery code signs in once", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    let r = await b.req("/api/auth/two-factor/verify-backup-code", {
      method: "POST",
      body: { code: codes[0] },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const again = w.browser();
    await signIn(again, "owner");
    r = await again.req("/api/auth/two-factor/verify-backup-code", {
      method: "POST",
      body: { code: codes[0] },
    });
    assert.ok(r.status >= 400, `reused recovery code: ${r.status}`);
  });

  test("passkey: registration options for the public hostname", async () => {
    const r = await w.owner.req("/api/auth/passkey/generate-register-options");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.rp.id, new URL(PUBLIC_URL).hostname);
    const credential = authn.create(r.data, PUBLIC_URL);
    const v = await w.owner.req("/api/auth/passkey/verify-registration", {
      method: "POST",
      body: { response: credential, name: "soft key" },
    });
    assert.equal(v.status, 200, JSON.stringify(v.data));
  });

  test("passkey: sign-in without a password or a TOTP prompt", async () => {
    const b = w.browser();
    let r = await b.req("/api/auth/passkey/generate-authenticate-options");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const assertion = authn.get(r.data, PUBLIC_URL);
    r = await b.req("/api/auth/passkey/verify-authentication", {
      method: "POST",
      body: { response: assertion },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.ok(r.data.session);
    assert.equal((await b.check()).status, 204);
  });

  test("passkey: an assertion for another origin is refused", async () => {
    const b = w.browser();
    const r = await b.req("/api/auth/passkey/generate-authenticate-options");
    const assertion = authn.get(r.data, "https://evil.example");
    const v = await b.req("/api/auth/passkey/verify-authentication", {
      method: "POST",
      body: { response: assertion },
    });
    assert.ok(v.status >= 400, `foreign origin: ${v.status}`);
  });

  test("not offered until later tickets: organizations, API keys, JWT, OAuth, impersonation", async () => {
    for (const [method, path] of [
      ["POST", "/api/auth/organization/create"],
      ["POST", "/api/auth/organization/invite-member"],
      ["GET", "/api/auth/api-key/list"],
      ["POST", "/api/auth/api-key/create"],
      ["GET", "/api/auth/token"],
      ["GET", "/api/auth/jwks"],
      ["POST", "/api/auth/oauth2/register"],
      ["GET", "/api/auth/oauth2/authorize"],
      ["POST", "/api/auth/admin/impersonate-user"],
      ["POST", "/api/auth/admin/create-user"],
      ["POST", "/api/auth/delete-user"],
      ["POST", "/api/auth/change-email"],
      // The reset email's callback: a reset link is /reset/<token>.
      ["GET", "/api/auth/reset-password/some-token?callbackURL=%2F"],
    ]) {
      const r = await w.owner.req(path, {
        method,
        ...(method === "POST" ? { body: {} } : {}),
      });
      assert.equal(r.status, 404, `${method} ${path}: ${r.status}`);
    }
  });

  test("a session token as a bearer, or an API key header, is no credential", async () => {
    const token = decodeURIComponent(
      w.owner.jar.get("__Host-lifeos.session")!,
    ).split(".")[0];
    const credentials: Record<string, string>[] = [
      { authorization: `Bearer ${token}` },
      { "x-api-key": "lo_anything" },
    ];
    for (const headers of credentials) {
      const r = await new Browser(w.fetcher).check(
        "GET",
        "/api/tasks/v1/tasks",
        headers,
      );
      assert.equal(r.status, 401, JSON.stringify(headers));
    }
  });

  test("a password change signs out the other sessions", async () => {
    const other = w.browser();
    const authn2 = authn; // the owner can still use the passkey
    let r = await other.req("/api/auth/passkey/generate-authenticate-options");
    r = await other.req("/api/auth/passkey/verify-authentication", {
      method: "POST",
      body: { response: authn2.get(r.data, PUBLIC_URL) },
    });
    assert.equal((await other.check()).status, 204);
    const fresh = w.browser();
    r = await fresh.req("/api/auth/passkey/generate-authenticate-options");
    r = await fresh.req("/api/auth/passkey/verify-authentication", {
      method: "POST",
      body: { response: authn2.get(r.data, PUBLIC_URL) },
    });
    r = await fresh.req("/api/auth/change-password", {
      method: "POST",
      body: {
        currentPassword: password,
        newPassword: `${password}!`,
        revokeOtherSessions: true,
      },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await other.check()).status, 401);
    assert.equal((await fresh.check()).status, 204);
  });
});

describe("the auth check, identity tokens, and legacy access", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world({ legacy: false });
  });
  after(() => w?.close());

  test("the identity token: claims, header, and lifetime", async () => {
    const reply = await w.owner.check();
    assert.equal(reply.status, 204);
    const { token, claims } = identity(w, reply);
    const header = JSON.parse(
      Buffer.from(token.split(".")[0], "base64url").toString(),
    );
    assert.deepEqual(header, {
      alg: "EdDSA",
      typ: "lifeos-id+jwt",
      kid: w.keys.kid,
    });
    assert.equal(claims.iss, "lifeos-auth");
    assert.equal(claims.aud, "lifeos-services");
    assert.equal(claims.sub, FIRST_USER_ID);
    assert.equal(claims.via, "session");
    assert.equal(claims.role, "owner");
    assert.deepEqual(claims.sp, { [FIRST_SPACE_ID]: "o" });
    assert.equal(claims.psp, FIRST_SPACE_ID);
    const exp = claims.exp as number;
    const iat = claims.iat as number;
    assert.equal(exp - iat, 300);
    assert.ok(typeof claims.jti === "string");
    assert.ok(typeof claims.sid === "string" && claims.sid !== "legacy");
    assert.ok(token.length < 1024, `token is ${token.length} bytes`);
    for (const name of ["name", "username", "email"]) {
      assert.equal(name in claims, false, `${name} in the token`);
    }
  });

  test("the token is cached per session, and re-minted when the role changes", async () => {
    const first = identity(w, await w.owner.check()).token;
    const second = identity(w, await w.owner.check()).token;
    assert.equal(first, second);
    const member = await seedUser(w.db.url, { username: "anna", password });
    const anna = w.browser();
    await signIn(anna, "anna");
    const before = identity(w, await anna.check());
    assert.equal(before.claims.role, "member");
    await sql(w.db.url, `UPDATE "user" SET role = 'admin' WHERE id = $1`, [
      member,
    ]);
    const afterChange = identity(w, await anna.check());
    assert.equal(afterChange.claims.role, "admin");
    assert.notEqual(afterChange.token, before.token);
    const { rows } = await w.rt.pool.query(
      'SELECT "authzVersion" AS v FROM "user" WHERE id = $1',
      [member],
    );
    assert.equal(rows[0].v, 2);
    await sql(w.db.url, `DELETE FROM "user" WHERE id = $1`, [member]);
  });

  test("legacy access off: no credential is 401", async () => {
    const r = await new Browser(w.fetcher).check();
    assert.equal(r.status, 401);
    assert.equal(r.headers.get("x-life-os-identity"), null);
  });

  test("legacy access on: no credential is the first account, via legacy", async () => {
    // Only the host turns it on (./setup.sh legacy-access on).
    assert.equal(await cli.setLegacyAccess(w.rt.pool, true), "ok");
    w.rt.legacy.invalidate();
    const anonymous = new Browser(w.fetcher);
    const reply = await anonymous.check();
    assert.equal(reply.status, 204);
    const { claims } = identity(w, reply);
    assert.equal(claims.sub, FIRST_USER_ID);
    assert.equal(claims.via, "legacy");
    assert.equal(claims.sid, "legacy");
    assert.equal(claims.role, "owner");
    assert.deepEqual(claims.sp, { [FIRST_SPACE_ID]: "o" });
    // A write from a client that is not a browser, as the phone app's.
    assert.equal(
      (
        await anonymous.check("POST", "/api/voice/v1/recordings", {
          origin: "",
        })
      ).status,
      204,
    );
  });

  test("legacy access on: a credential is still that credential's", async () => {
    const { claims } = identity(w, await w.owner.check());
    assert.equal(claims.via, "session");
    const stale = new Browser(w.fetcher);
    stale.jar.set("__Host-lifeos.session", "made-up.token");
    assert.equal((await stale.check()).status, 401);
  });

  test("legacy access is not enough for the account's own pages", async () => {
    const anonymous = new Browser(w.fetcher);
    let r = await anonymous.req("/api/account");
    assert.equal(r.status, 200);
    assert.equal(r.data.via, "legacy");
    assert.equal(r.data.user, null);
    r = await anonymous.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: false },
    });
    assert.equal(r.status, 401);
    assert.equal(r.data.error.code, "sign_in_required");
    r = await anonymous.req("/api/account/sessions");
    assert.equal(r.status, 401);
  });

  test("the owner turns legacy access off; only the host turns it on, while one account exists", async () => {
    const id = await seedUser(w.db.url, { username: "bea", password });
    let r = await w.owner.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: false },
    });
    assert.equal(r.status, 200);
    assert.deepEqual(r.data, { legacyAccess: false });
    assert.equal((await new Browser(w.fetcher).check()).status, 401);
    // Not from a browser, not even the owner's, with one account or more.
    r = await w.owner.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: true },
    });
    assert.equal(r.status, 403);
    assert.equal(r.data.error.code, "host_only");
    r = await w.owner.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: "yes" },
    });
    assert.equal(r.status, 400);
    assert.equal(await cli.setLegacyAccess(w.rt.pool, true), "accounts");
    // A member cannot change it.
    const bea = w.browser();
    await signIn(bea, "bea");
    r = await bea.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: false },
    });
    assert.equal(r.status, 403);
    await sql(w.db.url, `DELETE FROM "user" WHERE id = $1`, [id]);
    r = await w.owner.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: true },
    });
    assert.equal(r.status, 403, "one account: still the host's alone");
    assert.equal(await cli.setLegacyAccess(w.rt.pool, true), "ok");
    w.rt.legacy.invalidate();
    assert.equal((await new Browser(w.fetcher).check()).status, 204);
    assert.equal(await cli.setLegacyAccess(w.rt.pool, false), "ok");
    w.rt.legacy.invalidate();
    assert.equal((await new Browser(w.fetcher).check()).status, 401);
  });

  test("the default comes from LIFE_OS_LEGACY_ACCESS, once", async () => {
    const db = await freshDatabase(admin!);
    try {
      const on = runtime(db.url, { legacy: true });
      assert.equal((await new Browser(app(on)).check()).status, 204);
      await on.pool.end();
      // Stored now: a later default does not change it.
      const off = runtime(db.url, { legacy: false });
      assert.equal((await new Browser(app(off)).check()).status, 204);
      await off.pool.end();
    } finally {
      await db.drop();
    }
  });

  test("own sessions: listed, and signed out one by one", async () => {
    const other = w.browser();
    await signIn(other, "owner");
    let r = await w.owner.req("/api/account/sessions");
    assert.equal(r.status, 200);
    const sessions = r.data.sessions as Array<{ id: string; current: boolean }>;
    assert.ok(sessions.length >= 2);
    assert.equal(sessions.filter((s) => s.current).length, 1);
    const target = sessions.find((s) => !s.current)!;
    r = await w.owner.req(`/api/account/sessions/${target.id}`, {
      method: "DELETE",
    });
    assert.equal(r.status, 204);
  });
});

describe("CSRF and Origin", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world({ legacy: true });
  });
  after(() => w?.close());

  test("a write with the cookie needs Life OS's own origin", async () => {
    const own = await w.owner.check("POST");
    assert.equal(own.status, 204);
    const forms: Record<string, string>[] = [
      { origin: "https://evil.example" },
      { origin: "https://ntfy.lifeos.test" },
      { origin: "", "sec-fetch-site": "cross-site" },
      { origin: "", "sec-fetch-site": "same-site" },
      { origin: "" },
    ];
    for (const headers of forms) {
      const r = await w.owner.check("POST", "/api/tasks/v1/tasks", headers);
      assert.equal(r.status, 403, JSON.stringify(headers));
    }
    const sameOrigin = await w.owner.check("DELETE", "/api/tasks/v1/tasks/1", {
      origin: "",
      "sec-fetch-site": "same-origin",
    });
    assert.equal(sameOrigin.status, 204);
    // The installed app's share sheet: a navigation the person started.
    const share = await w.owner.check("POST", "/share", {
      origin: "null",
      "sec-fetch-site": "none",
    });
    assert.equal(share.status, 204);
    // Reads need neither.
    assert.equal(
      (await w.owner.check("GET", "/api/tasks/v1/tasks", { origin: "" }))
        .status,
      204,
    );
  });

  test("legacy access: a browser on another site is refused, other clients are not", async () => {
    const anonymous = new Browser(w.fetcher);
    let r = await anonymous.check("POST", "/api/tasks/v1/tasks", {
      origin: "https://evil.example",
    });
    assert.equal(r.status, 403);
    r = await anonymous.check("POST", "/api/tasks/v1/tasks", {
      origin: "",
      "sec-fetch-site": "cross-site",
    });
    assert.equal(r.status, 403);
    r = await anonymous.check("POST", "/api/tasks/v1/tasks", { origin: "" });
    assert.equal(r.status, 204);
  });

  test("Better Auth refuses a sign-in from another origin", async () => {
    const evil = new Browser(w.fetcher, PUBLIC_URL);
    const r = await evil.req("/api/auth/sign-in/username", {
      method: "POST",
      headers: { origin: "https://evil.example" },
      body: { username: "owner", password },
    });
    assert.equal(r.status, 403, JSON.stringify(r.data));
  });

  test("the account API refuses a cross-site write", async () => {
    const r = await w.owner.req("/api/account/legacy-access", {
      method: "POST",
      headers: { origin: "https://evil.example" },
      body: { enabled: false },
    });
    assert.equal(r.status, 403);
  });

  test("signing in over plain HTTP is refused unless the host is allowed", async () => {
    const plain = new Browser(w.fetcher, "http://10.8.0.1:3000");
    const r = await signIn(plain, "owner");
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "INSECURE_ORIGIN");
    // localhost is a secure context.
    const local = new Browser(w.fetcher, "http://localhost:3000");
    assert.equal((await signIn(local, "owner")).status, 200);
    assert.equal((await local.check()).status, 204);
  });
});

describe(
  "an allowed plain-HTTP host (LIFE_OS_INSECURE_LOGIN_HOSTS)",
  { skip },
  () => {
    let w: World;
    before(async () => {
      w = await world({ legacy: false, insecureHosts: "10.8.0.1" });
    });
    after(() => w?.close());

    test("signs in with a non-Secure host-only cookie that only that host reads", async () => {
      const plain = new Browser(w.fetcher, "http://10.8.0.1:3000");
      const r = await signIn(plain, "owner");
      assert.equal(r.status, 200, JSON.stringify(r.data));
      const cookie = r.headers
        .getSetCookie()
        .find((c) => c.startsWith("lifeos.session="));
      assert.ok(cookie, r.headers.getSetCookie().join(" | "));
      assert.doesNotMatch(cookie, /; Secure/i);
      assert.equal((await plain.check()).status, 204);
      // The same cookie sent to the public name counts for nothing.
      const elsewhere = new Browser(w.fetcher);
      elsewhere.jar.set("lifeos.session", plain.jar.get("lifeos.session")!);
      assert.equal((await elsewhere.check()).status, 401);
      // Another plain-HTTP host is still refused.
      const other = new Browser(w.fetcher, "http://10.8.0.2:3000");
      assert.equal((await signIn(other, "owner")).status, 403);
    });
  },
);

describe("rate limits", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world();
  });
  after(() => w?.close());

  test("Better Auth limits sign-ins per address", async () => {
    const b = new Browser(w.fetcher, PUBLIC_URL, "203.0.113.7");
    const statuses: number[] = [];
    for (let i = 0; i < 5; i += 1) {
      statuses.push((await signIn(b, "owner", "wrong password 123")).status);
    }
    assert.ok(statuses.includes(429), statuses.join(","));
    // Another address is not limited by it.
    assert.equal(
      (await signIn(w.browser(PUBLIC_URL, "203.0.113.8"), "owner")).status,
      200,
    );
  });

  test("failed sign-ins for one username from one address slow down", async () => {
    // Five failures, each from its own Better Auth window, all from one address.
    const address = "203.0.113.20";
    for (let i = 0; i < 5; i += 1) {
      const r = await signIn(
        new Browser(w.fetcher, PUBLIC_URL, address),
        "owner",
        "wrong password 123",
      );
      if (r.status === 429 && r.data.code !== "TOO_MANY_FAILED_SIGN_INS") {
        // Better Auth's own limit (3 in 10 s): wait it out.
        await new Promise((resolve) => setTimeout(resolve, 10_500));
        i -= 1;
      }
    }
    const blocked = await signIn(
      new Browser(w.fetcher, PUBLIC_URL, address),
      "owner",
    );
    assert.equal(blocked.status, 429);
    assert.equal(blocked.data.code, "TOO_MANY_FAILED_SIGN_INS");
    // The owner, elsewhere, signs in at once.
    assert.equal(
      (await signIn(w.browser(PUBLIC_URL, "203.0.113.21"), "owner")).status,
      200,
    );
  });
});

describe("admin endpoints, a second account, and a ban", { skip }, () => {
  let w: World;
  let memberId: string;
  let ownerTotp: string;
  before(async () => {
    w = await world();
    memberId = await seedUser(w.db.url, { username: "alice", password });
  });
  after(() => w?.close());

  test("the owner needs a second factor first", async () => {
    const r = await w.owner.req("/api/auth/admin/list-users");
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "SECOND_FACTOR_REQUIRED");
    ownerTotp = (await enableTotp(w.owner)).uri;
  });

  test("admin: list users", async () => {
    const r = await w.owner.req("/api/auth/admin/list-users");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.users.length, 2);
  });

  test("a member is not an admin", async () => {
    const alice = w.browser();
    await signIn(alice, "alice");
    const r = await alice.req("/api/auth/admin/list-users");
    assert.equal(r.status, 403);
  });

  test("admin: impersonation is refused", async () => {
    const r = await w.owner.req("/api/auth/admin/impersonate-user", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 404);
  });

  test("admin: the owner changes roles, never to owner, never their own", async () => {
    let r = await w.owner.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: memberId, role: "admin" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.user.role, "admin");
    r = await w.owner.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: memberId, role: "owner" },
    });
    assert.equal(r.status, 400);
    r = await w.owner.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: FIRST_USER_ID, role: "member" },
    });
    assert.equal(r.status, 403);
    // An admin cannot change roles.
    const alice = w.browser();
    await signIn(alice, "alice");
    await enableTotp(alice);
    r = await alice.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: memberId, role: "member" },
    });
    assert.equal(r.status, 403);
    r = await w.owner.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: memberId, role: "member" },
    });
    assert.equal(r.status, 200);
    await sql(w.db.url, `DELETE FROM "twoFactor" WHERE "userId" = $1`, [
      memberId,
    ]);
    await sql(
      w.db.url,
      `UPDATE "user" SET "twoFactorEnabled" = false WHERE id = $1`,
      [memberId],
    );
  });

  test("the database allows one owner only", async () => {
    await assert.rejects(
      sql(w.db.url, `UPDATE "user" SET role = 'owner' WHERE id = $1`, [
        memberId,
      ]),
      /user_one_owner_idx/,
    );
  });

  test("admin: a session older than 12 hours must sign in again", async () => {
    const stale = w.browser();
    await signIn(stale, "owner");
    await stale.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(ownerTotp) },
    });
    const mine = await stale.req("/api/account/sessions");
    const current = mine.data.sessions.find(
      (session: { current: boolean }) => session.current,
    );
    await sql(
      w.db.url,
      `UPDATE session SET "createdAt" = now() - interval '13 hours' WHERE id = $1`,
      [current.id],
    );
    const r = await stale.req("/api/auth/admin/list-users");
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "SESSION_NOT_FRESH");
    // The fresh session still may.
    assert.equal((await w.owner.req("/api/auth/admin/list-users")).status, 200);
  });

  test("admin: list a user's sessions", async () => {
    const r = await w.owner.req("/api/auth/admin/list-user-sessions", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.ok(Array.isArray(r.data.sessions));
  });

  test("a banned user: sessions dead, sign-in refused, and the check says no", async () => {
    const alice = w.browser();
    await signIn(alice, "alice");
    assert.equal((await alice.check()).status, 204);
    // A session the ban did not delete (such as a credential of a later
    // ticket) still gets nothing: the check tests the ban itself.
    const kept = await w.rt.pool.query(
      `SELECT token FROM session WHERE "userId" = $1`,
      [memberId],
    );
    let r = await w.owner.req("/api/auth/admin/ban-user", {
      method: "POST",
      body: { userId: memberId, banReason: "test" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await alice.check()).status, 401);
    r = await signIn(w.browser(), "alice");
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "BANNED_USER");
    await sql(
      w.db.url,
      `INSERT INTO session (token, "userId", "expiresAt", "updatedAt")
       VALUES ($1, $2, now() + interval '1 day', now())`,
      [kept.rows[0].token, memberId],
    );
    assert.equal((await alice.check()).status, 401);
    await sql(w.db.url, `DELETE FROM session WHERE "userId" = $1`, [memberId]);
    // The owner cannot be banned or deleted.
    r = await w.owner.req("/api/auth/admin/ban-user", {
      method: "POST",
      body: { userId: FIRST_USER_ID },
    });
    assert.equal(r.status, 403);
  });

  test("admin: unban, then revoke all of a user's sessions", async () => {
    let r = await w.owner.req("/api/auth/admin/unban-user", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const alice = w.browser();
    assert.equal((await signIn(alice, "alice")).status, 200);
    r = await w.owner.req("/api/auth/admin/revoke-user-sessions", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 200);
    assert.equal((await alice.check()).status, 401);
  });

  test("admin: delete a user (never the owner)", async () => {
    let r = await w.owner.req("/api/auth/admin/remove-user", {
      method: "POST",
      body: { userId: FIRST_USER_ID },
    });
    assert.equal(r.status, 403);
    r = await w.owner.req("/api/auth/admin/remove-user", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await signIn(w.browser(), "alice")).status, 401);
  });
});

describe("the host's commands", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world({ owner: false });
  });
  after(() => w?.close());

  const run = async (...argv: string[]) => {
    const lines: string[] = [];
    const out = {
      log: (s: string) => lines.push(s),
      error: (s: string) => lines.push(s),
    };
    const code = await cli.main(
      argv,
      {
        AUTH_DATABASE_URL: w.db.url,
        LIFE_OS_PUBLIC_URL: PUBLIC_URL,
      },
      out,
    );
    return { code, text: lines.join("\n") };
  };

  test("setup-code prints a code the setup page takes; has-account follows", async () => {
    assert.equal((await run("has-account")).code, 1);
    const printed = await run("setup-code");
    assert.equal(printed.code, 0);
    const code = printed.text.match(/Setup code: (\S+)/)?.[1];
    assert.ok(code, printed.text);
    assert.match(printed.text, /https:\/\/lifeos\.test\/setup/);
    const r = await w.owner.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code! },
      body: { username: "owner", password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await run("has-account")).code, 0);
    const quiet = await run("setup-code", "--if-no-account");
    assert.equal(quiet.code, 0);
    assert.equal(quiet.text, "");
  });

  test("recover-owner prints a one-time link that sets a new password", async () => {
    const before = w.browser();
    await signIn(before, "owner");
    await enableTotp(before);
    const printed = await run("recover-owner", "--reset-2fa");
    assert.equal(printed.code, 0, printed.text);
    const link = printed.text.match(/https:\/\/lifeos\.test\/reset\/(\S+)/);
    assert.ok(link, printed.text);
    const token = link[1];
    const newPassword = `${password} again`;
    let r = await w.browser().req("/api/auth/reset-password", {
      method: "POST",
      body: { token, newPassword },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    // Sessions are gone; the link worked once; two-factor was reset.
    assert.equal((await before.check()).status, 401);
    r = await w.browser().req("/api/auth/reset-password", {
      method: "POST",
      body: { token, newPassword: `${password} third` },
    });
    assert.equal(r.status, 400);
    const after = w.browser();
    r = await signIn(after, "owner", newPassword);
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.notEqual(r.data.twoFactorRedirect, true);
    assert.equal((await after.check()).status, 204);
    await sql(
      w.db.url,
      `UPDATE account SET password = $1 WHERE "userId" = $2`,
      [await hashPassword(password), FIRST_USER_ID],
    );
  });

  test("legacy-access shows and changes the setting", async () => {
    assert.match((await run("legacy-access")).text, /off|not set/);
    assert.equal((await run("legacy-access", "on")).code, 0);
    assert.match((await run("legacy-access")).text, /is on/);
    w.rt.legacy.invalidate();
    assert.equal((await new Browser(w.fetcher).check()).status, 204);
    assert.equal((await run("legacy-access", "off")).code, 0);
    w.rt.legacy.invalidate();
    assert.equal((await new Browser(w.fetcher).check()).status, 401);
    assert.equal((await run("legacy-access", "maybe")).code, 2);
  });

  test("sign-out-all signs out every session of every account", async () => {
    const one = w.browser();
    const two = w.browser();
    await signIn(one, "owner");
    await signIn(two, "owner");
    const id = await seedUser(w.db.url, { username: "carl", password });
    const carl = w.browser();
    await signIn(carl, "carl");
    for (const b of [one, two, carl])
      assert.equal((await b.check()).status, 204);
    const before = await w.rt.pool.query(
      'SELECT "authzVersion" AS v FROM "user" ORDER BY id',
    );
    const printed = await run("sign-out-all");
    assert.equal(printed.code, 0);
    assert.match(printed.text, /Signed out \d+ session/);
    // Signing out means little while legacy access is on: it says which.
    assert.match(
      printed.text,
      /Legacy access is off: every request needs a sign-in/,
    );
    for (const b of [one, two, carl])
      assert.equal((await b.check()).status, 401);
    const afterRows = await w.rt.pool.query(
      'SELECT "authzVersion" AS v FROM "user" ORDER BY id',
    );
    afterRows.rows.forEach((row, i) =>
      assert.equal(row.v, before.rows[i].v + 1),
    );
    // Everyone signs in again as usual.
    assert.equal((await signIn(w.browser(), "owner")).status, 200);
    await sql(w.db.url, `DELETE FROM "user" WHERE id = $1`, [id]);
    assert.equal((await run("legacy-access", "on")).code, 0);
    const legacyOn = await run("sign-out-all");
    assert.match(
      legacyOn.text,
      /Legacy access is on: .*\.\/setup\.sh legacy-access off/,
    );
    assert.equal((await run("legacy-access", "off")).code, 0);
  });
});

describe("schema, passwords, and keys", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world({ owner: false });
  });
  after(() => w?.close());

  test("auth_db's migrations are all Better Auth expects", async () => {
    const { toBeCreated, toBeAdded } = await getMigrations(w.rt.auth.options);
    assert.deepEqual(
      toBeCreated.map((t) => t.table),
      [],
      "Better Auth wants tables the migrations do not create: write a migration",
    );
    assert.deepEqual(
      toBeAdded.map((t) => `${t.table}: ${Object.keys(t.fields).join(", ")}`),
      [],
      "Better Auth wants columns the migrations do not add: write a migration",
    );
  });

  test("password hashes are Better Auth's format both ways", async () => {
    const ours = await hashPassword(password);
    assert.match(ours, /^[0-9a-f]{32}:[0-9a-f]{128}$/);
    assert.equal(await betterAuthVerify({ hash: ours, password }), true);
    assert.equal(await verifyPassword({ hash: ours, password }), true);
    assert.equal(
      await verifyPassword({ hash: ours, password: "nope nope nope" }),
      false,
    );
    assert.equal(await verifyPassword({ hash: "garbage", password }), false);
  });

  test("a rotation: tokens of the old and the new key both verify while both are trusted", async () => {
    const old = testKeys("auth-old");
    const fresh = testKeys("auth-new");
    const trusted = parseTrustedKeys(
      JSON.stringify({ keys: [old.publicJwk, fresh.publicJwk] }),
    );
    const claims = {
      sub: FIRST_USER_ID,
      sid: "s",
      via: "session" as const,
      role: "owner" as const,
      sp: {},
    };
    assert.ok(
      verifyIdentityToken(createSigner(old.signingKey).sign(claims), trusted),
    );
    assert.ok(
      verifyIdentityToken(createSigner(fresh.signingKey).sign(claims), trusted),
    );
    const onlyNew = parseTrustedKeys(
      JSON.stringify({ keys: [fresh.publicJwk] }),
    );
    assert.equal(
      verifyIdentityToken(createSigner(old.signingKey).sign(claims), onlyNew),
      null,
    );
  });

  test("the runtime refuses a signing key the services would not trust", () => {
    const signing = testKeys("auth-a");
    const other = testKeys("auth-b");
    assert.throws(
      () => runtime(w.db.url, { keys: signing, trusted: [other.publicJwk] }),
      /does not list the signing key auth-a/,
    );
  });

  test("the runtime refuses a trusted key with the signing key's kid but another key", () => {
    const signing = testKeys("auth-a");
    const impostor = testKeys("auth-a");
    assert.notEqual(signing.publicJwk.x, impostor.publicJwk.x);
    assert.throws(
      () => runtime(w.db.url, { keys: signing, trusted: [impostor.publicJwk] }),
      /lists auth-a with another public key/,
    );
    // The matching pair starts.
    const ok = runtime(w.db.url, { keys: signing });
    void ok.pool.end();
  });
});
