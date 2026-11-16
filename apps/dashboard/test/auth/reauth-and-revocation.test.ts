/**
 * Proving it is still you, and credentials that do not outlive what ends
 * them (P21-06's review), in the auth flow suite
 * (docs/operations/ci.md#the-auth-flow-suite): a token, a pairing code, or
 * an OAuth consent needs a sign-in or the password from the last 12 hours;
 * adding a second factor needs the current one (or, with none, the
 * password); a password reset and (by default) a password change sign the
 * account's devices, tokens, and connected apps out, as an admin's sign-out
 * does, through an outbox that retries; MCP's prompt=login and max_age are
 * honoured when signed in; and the check judges the route the gateway
 * names, refusing paths nginx would resolve elsewhere. Against a throwaway
 * PostgreSQL (AUTH_TEST_DATABASE_URL); tools/test-auth runs it.
 */
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { after, before, describe, test } from "node:test";

import { FIRST_USER_ID } from "../../src/lib/auth/constants";
import { queueCredentialRevocation } from "../../src/lib/auth/keys";
import {
  consentRequest,
  oauthLoginDecision,
} from "../../src/lib/auth/oauth-pages";
import { processRevocations } from "../../src/lib/auth/security-hooks";
import * as cli from "../../scripts/lifeos-auth.mjs";
import { SoftAuthenticator } from "./authenticators";
import { adminUrl, type Browser, PUBLIC_URL, type Reply, sql } from "./harness";
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
const NAVIGATE = { accept: "text/html", "sec-fetch-mode": "navigate" };

/** A request as a native client makes it: no cookie, no Origin. */
async function native(
  w: World,
  method: string,
  path: string,
  options: {
    key?: string;
    body?: unknown;
    headers?: Record<string, string>;
  } = {},
): Promise<Reply> {
  const headers = new Headers({
    host: new URL(PUBLIC_URL).host,
    "x-real-ip": `203.0.113.${1 + Math.floor(Math.random() * 250)}`,
    "user-agent": "okhttp/4.12.0",
    ...options.headers,
  });
  if (options.key) headers.set("authorization", `Bearer ${options.key}`);
  let body: string | undefined;
  if (options.body !== undefined) {
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
  key: string,
  extra: Record<string, string> = {},
) {
  return native(w, "GET", "/internal/auth/check", {
    key,
    headers: { "x-original-method": method, "x-original-uri": uri, ...extra },
  });
}

const works = async (w: World, key: string) =>
  (await nativeCheck(w, "GET", "/api/tasks/v1/tasks", key)).status === 204;

/** Makes a browser's session look as if it signed in hours ago. */
async function age(w: World, b: Browser, hours = 13) {
  const token = decodeURIComponent(b.jar.get("__Host-lifeos.session")!).split(
    ".",
  )[0];
  await sql(
    w.db.url,
    `UPDATE session SET "createdAt" = now() - make_interval(hours => $2)
      WHERE token = $1`,
    [token, hours],
  );
}

function confirm(b: Browser, pw: string = password) {
  return b.req("/api/account/confirm-password", {
    method: "POST",
    body: { password: pw },
  });
}

function createToken(b: Browser, name = "script") {
  return b.req("/api/account/tokens", {
    method: "POST",
    body: { name, scopes: ["tasks:read"] },
  });
}

function createPairingCode(b: Browser) {
  return b.req("/api/account/devices/pairing-codes", {
    method: "POST",
    body: {},
  });
}

async function pairPhone(w: World, b: Browser) {
  const code = await createPairingCode(b);
  assert.equal(code.status, 201, JSON.stringify(code.data));
  const r = await native(w, "POST", "/api/auth/devices/pair", {
    body: { code: code.data.code, name: "Phone" },
  });
  assert.equal(r.status, 200, JSON.stringify(r.data));
  return r.data.key as string;
}

async function makeToken(b: Browser) {
  const r = await createToken(b);
  assert.equal(r.status, 201, JSON.stringify(r.data));
  return r.data.token as string;
}

/** A registered MCP client, and its authorization requests. */
async function oauthClient(w: World, port: number) {
  const redirect = `http://127.0.0.1:${port}/cb`;
  const r = await native(w, "POST", "/api/auth/oauth2/register", {
    body: {
      client_name: "Review client",
      redirect_uris: [redirect],
      token_endpoint_auth_method: "none",
      grant_types: ["authorization_code", "refresh_token"],
      response_types: ["code"],
      scope: "mcp:read mcp:write offline_access",
    },
  });
  assert.ok([200, 201].includes(r.status), JSON.stringify(r.data));
  const query = (extra: Record<string, string> = {}) =>
    new URLSearchParams({
      response_type: "code",
      client_id: r.data.client_id,
      redirect_uri: redirect,
      scope: "mcp:read mcp:write offline_access",
      code_challenge: createHash("sha256")
        .update(randomBytes(32).toString("base64url"))
        .digest("base64url"),
      code_challenge_method: "S256",
      resource: MCP,
      ...extra,
    });
  return { clientId: r.data.client_id as string, redirect, query };
}

/** Where the provider sends a browser, and the page's signed parameters. */
async function authorize(b: Browser, query: URLSearchParams) {
  const r = await b.req(`/api/auth/oauth2/authorize?${query}`, {
    headers: NAVIGATE,
  });
  assert.equal(r.status, 302, JSON.stringify(r.data));
  const url = new URL(r.headers.get("location")!, PUBLIC_URL);
  // As Next.js gives a page its search parameters: a repeated one (the
  // signature's ba_param) as a list.
  const params: Record<string, string | string[]> = {};
  for (const key of new Set(url.searchParams.keys())) {
    const values = url.searchParams.getAll(key);
    params[key] = values.length > 1 ? values : values[0];
  }
  return { url, params };
}

/** A signed-in browser's consent request for a new client. */
async function consentQuery(w: World, b: Browser, port: number) {
  const client = await oauthClient(w, port);
  const page = await authorize(b, client.query());
  assert.equal(page.url.pathname, "/oauth/consent", page.url.toString());
  const request = await consentRequest(w.rt, page.params);
  assert.ok(request.ok);
  return { ...client, oauthQuery: request.oauthQuery };
}

function consent(b: Browser, oauthQuery: string, accept = true) {
  return b.req("/api/auth/oauth2/consent", {
    method: "POST",
    body: {
      accept,
      scope: "mcp:read mcp:write offline_access",
      oauth_query: oauthQuery,
    },
  });
}

describe("proving it is still you", { skip }, () => {
  let w: World;
  before(async () => {
    w = await world(admin!);
  });
  after(() => w?.close());

  test("a token, a pairing code, and a consent need a recent sign-in or the password", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    assert.equal((await b.req("/api/account")).data.recentAuth, true);
    const app = await consentQuery(w, b, 41000);
    await age(w, b);
    assert.equal((await b.req("/api/account")).data.recentAuth, false);
    let r = await createToken(b, "late");
    assert.equal(r.status, 403, JSON.stringify(r.data));
    assert.equal(r.data.error.code, "reauth_required");
    r = await createPairingCode(b);
    assert.equal(r.status, 403, JSON.stringify(r.data));
    assert.equal(r.data.error.code, "reauth_required");
    r = await consent(b, app.oauthQuery);
    assert.equal(r.status, 403, JSON.stringify(r.data));
    assert.equal(r.data.code, "REAUTH_REQUIRED");
    const keys = await w.rt.pool.query("SELECT count(*)::int AS n FROM apikey");
    assert.equal(keys.rows[0].n, 0);
    // Saying no needs nothing.
    r = await consent(b, app.oauthQuery, false);
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(
      new URL(r.data.url).searchParams.get("error"),
      "access_denied",
    );

    // A wrong password is refused and logged; the right one goes on.
    r = await confirm(b, "not the password at all");
    assert.equal(r.status, 400);
    assert.equal(r.data.error.code, "password_invalid");
    assert.equal((await b.req("/api/account")).data.recentAuth, false);
    r = await confirm(b);
    assert.equal(r.status, 204);
    assert.equal((await b.req("/api/account")).data.recentAuth, true);
    await makeToken(b);
    await pairPhone(w, b);
    const again = await consentQuery(w, b, 41001);
    r = await consent(b, again.oauthQuery);
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.ok(new URL(r.data.url).searchParams.get("code"));
    const rows = await securityRows(w);
    assert.equal(rows.filter((e) => e.kind === "password_confirmed").length, 1);
    assert.ok(
      rows.some(
        (e) =>
          e.kind === "sign_in_failed" &&
          e.detail === "reauth:password_invalid" &&
          e.subject_user_id === FIRST_USER_ID,
      ),
    );
    assert.ok(!JSON.stringify(rows).includes(password));

    // A confirmation counts for 12 hours, as a sign-in does.
    await sql(
      w.db.url,
      `UPDATE session_reauth SET password_at = now() - interval '13 hours'`,
    );
    r = await createToken(b, "later");
    assert.equal(r.status, 403);
    assert.equal(r.data.error.code, "reauth_required");
  });

  test("wrong passwords are limited per session", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    const answers = [];
    for (let i = 0; i < 6; i += 1) {
      answers.push((await confirm(b, `wrong password ${i}`)).status);
    }
    assert.deepEqual(answers, [400, 400, 400, 400, 400, 429]);
    assert.equal((await confirm(b)).status, 429, "even the right one");
    // Another session is not held up.
    const other = w.browser();
    await signIn(other, "owner");
    assert.equal((await confirm(other)).status, 204);
  });

  test("a first passkey needs the password; with a second factor, a sign-in with it", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    let r = await b.req("/api/auth/passkey/generate-register-options");
    assert.equal(r.status, 403, JSON.stringify(r.data));
    assert.equal(r.data.code, "PASSWORD_REQUIRED");
    // A password confirmed more than 5 minutes ago is not enough.
    assert.equal((await confirm(b)).status, 204);
    await sql(
      w.db.url,
      `UPDATE session_reauth SET password_at = now() - interval '6 minutes'`,
    );
    r = await b.req("/api/auth/passkey/generate-register-options");
    assert.equal(r.data.code, "PASSWORD_REQUIRED");
    assert.equal((await confirm(b)).status, 204);
    const authn = new SoftAuthenticator();
    r = await b.req("/api/auth/passkey/generate-register-options");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await b.req("/api/auth/passkey/verify-registration", {
      method: "POST",
      body: { response: authn.create(r.data, PUBLIC_URL), name: "key" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    // The check passed, so adding it counts as signing in with it.
    assert.equal((await b.req("/api/account")).data.mfa, true);

    // A password-only session, now that the account has a passkey: no
    // second factor without signing in with the passkey, password or not.
    const other = w.browser();
    await signIn(other, "owner");
    assert.equal((await confirm(other)).status, 204);
    r = await other.req("/api/auth/passkey/generate-register-options");
    assert.equal(r.status, 403, JSON.stringify(r.data));
    assert.equal(r.data.code, "SECOND_FACTOR_SIGN_IN_REQUIRED");
    r = await other.req("/api/auth/passkey/verify-registration", {
      method: "POST",
      body: {
        response: new SoftAuthenticator().create(
          { challenge: "x" },
          PUBLIC_URL,
        ),
      },
    });
    assert.equal(r.status, 403, JSON.stringify(r.data));
    r = await other.req("/api/auth/two-factor/enable", {
      method: "POST",
      body: { password },
    });
    assert.equal(r.status, 403, JSON.stringify(r.data));
    assert.equal(r.data.code, "SECOND_FACTOR_SIGN_IN_REQUIRED");
    assert.equal((await other.req("/api/account")).data.mfa, false);
    const passkeys = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM passkey",
    );
    assert.equal(passkeys.rows[0].n, 1);

    // Signed in with the passkey, it may.
    const withKey = w.browser();
    r = await withKey.req("/api/auth/passkey/generate-authenticate-options");
    r = await withKey.req("/api/auth/passkey/verify-authentication", {
      method: "POST",
      body: { response: authn.get(r.data, PUBLIC_URL) },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await withKey.req("/api/auth/passkey/generate-register-options");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    await enableTotp(withKey);
  });
});

describe("credentials that do not outlive what ends them", { skip }, () => {
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

  const revokedEntries = async () =>
    (await securityRows(w))
      .filter((e) => e.kind === "credentials_revoked")
      .map((e) => [e.subject_user_id, e.detail]);

  const queued = async () =>
    (
      await w.rt.pool.query(
        "SELECT count(*)::int AS n FROM credential_revocations",
      )
    ).rows[0].n;

  test("a password reset signs out the account's sessions, devices, tokens, and apps", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    const key = await pairPhone(w, b);
    const token = await makeToken(b);
    const app = await consentQuery(w, b, 42000);
    assert.equal((await consent(b, app.oauthQuery)).status, 200);
    const printed = await host("recover-owner");
    assert.equal(printed.code, 0, printed.text);
    const link = printed.text.match(/\/reset\/(\S+)/);
    assert.ok(link, printed.text);
    const r = await w.browser().req("/api/auth/reset-password", {
      method: "POST",
      body: { token: link[1], newPassword: password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(await works(w, key), false);
    assert.equal(await works(w, token), false);
    assert.equal((await b.check()).status, 401);
    const grants = await w.rt.pool.query(
      `SELECT count(*)::int AS n FROM "oauthConsent" WHERE "userId" = $1`,
      [FIRST_USER_ID],
    );
    assert.equal(grants.rows[0].n, 0);
    assert.equal(await queued(), 0, "done at once");
    assert.deepEqual(await revokedEntries(), [
      [FIRST_USER_ID, "password_reset"],
    ]);
  });

  test("a password change signs them out too, unless the box is cleared; this session stays", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    let key = await pairPhone(w, b);
    const token = await makeToken(b);
    let r = await b.req("/api/auth/change-password", {
      method: "POST",
      body: {
        currentPassword: password,
        newPassword: `${password}!`,
        revokeOtherSessions: true,
      },
      headers: { "x-lifeos-revoke-credentials": "1" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(await works(w, key), false);
    assert.equal(await works(w, token), false);
    assert.equal((await b.check()).status, 204, "this session stays");
    assert.deepEqual((await revokedEntries()).at(-1), [
      FIRST_USER_ID,
      "password_change",
    ]);

    // The box cleared (no header): devices and tokens are kept.
    key = await pairPhone(w, b);
    const count = (await revokedEntries()).length;
    r = await b.req("/api/auth/change-password", {
      method: "POST",
      body: {
        currentPassword: `${password}!`,
        newPassword: password,
        revokeOtherSessions: true,
      },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(await works(w, key), true, "kept without the box");
    assert.equal((await revokedEntries()).length, count);
    // A wrong current password changes nothing, whatever the box says.
    r = await b.req("/api/auth/change-password", {
      method: "POST",
      body: { currentPassword: "not it at all", newPassword: `${password}?` },
      headers: { "x-lifeos-revoke-credentials": "1" },
    });
    assert.ok(r.status >= 400);
    assert.equal(await works(w, key), true);
  });

  test("the outbox retries a revocation until it is done, and does it once", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    const key = await pairPhone(w, b);
    const count = (await revokedEntries()).length;
    // As if the revocation after an action could not run at once.
    await queueCredentialRevocation(w.rt.pool, FIRST_USER_ID, "admin_sign_out");
    await w.rt.pool.query(
      "UPDATE credential_revocations SET last_attempt_at = now(), attempts = 1",
    );
    assert.equal(await processRevocations(w.rt.pool), 0, "waits 30 s");
    assert.equal(await works(w, key), true);
    await w.rt.pool.query(
      "UPDATE credential_revocations SET last_attempt_at = now() - interval '1 minute'",
    );
    assert.equal(await processRevocations(w.rt.pool), 1);
    assert.equal(await works(w, key), false);
    assert.equal((await b.check()).status, 401, "sessions too");
    assert.equal(await queued(), 0);
    assert.equal(await processRevocations(w.rt.pool), 0, "once");
    const entries = await revokedEntries();
    assert.equal(entries.length, count + 1);
    assert.deepEqual(entries.at(-1), [FIRST_USER_ID, "admin_sign_out"]);
    // A reason the database does not know is refused.
    await assert.rejects(
      w.rt.pool.query(
        "INSERT INTO credential_revocations (user_id, reason) VALUES ($1, 'Bad reason!')",
        [FIRST_USER_ID],
      ),
    );
  });

  test("an admin's sign-out revokes the member's keys through the outbox", async () => {
    const memberId = await seedUser(w.db.url, { username: "erin", password });
    const erin = w.browser();
    assert.equal((await signIn(erin, "erin")).status, 200);
    const key = await pairPhone(w, erin);
    const owner = w.browser();
    await signIn(owner, "owner");
    await enableTotp(owner);
    const r = await owner.req("/api/auth/admin/revoke-user-sessions", {
      method: "POST",
      body: { userId: memberId },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await erin.check()).status, 401);
    assert.equal(await works(w, key), false);
    assert.equal(await queued(), 0);
    assert.deepEqual((await revokedEntries()).at(-1), [
      memberId,
      "admin_sign_out",
    ]);
  });
});

describe(
  "MCP's prompt=login and max_age, and the route the gateway names",
  { skip },
  () => {
    let w: World;
    before(async () => {
      w = await world(admin!);
    });
    after(() => w?.close());

    test("the login page's decision: prompt=login and max_age ask for a new sign-in", async () => {
      const client = await oauthClient(w, 43000);
      const stranger = w.browser();
      const signed = async (extra: Record<string, string>) => {
        const page = await authorize(stranger, client.query(extra));
        assert.equal(page.url.pathname, "/login");
        return page.params;
      };
      const now = Date.now();
      const minuteAgo = { signedIn: true, signedInAt: now - 60_000 };
      const hoursAgo = { signedIn: true, signedInAt: now - 2 * 3600_000 };
      const decide = async (
        extra: Record<string, string>,
        session: { signedIn: boolean; signedInAt: number | null },
      ) => oauthLoginDecision(w.rt, await signed(extra), session, now);

      // Nothing asked: on at once.
      let d = await decide({}, hoursAgo);
      assert.equal(d?.action, "continue");
      // prompt=login: a new sign-in, after which the prompt is answered.
      d = await decide({ prompt: "login" }, minuteAgo);
      assert.equal(d?.action, "sign-in");
      assert.ok(d?.action === "sign-in" && !d.next.includes("prompt="));
      d = await decide({ prompt: "login consent" }, minuteAgo);
      assert.ok(d?.action === "sign-in" && d.next.includes("prompt=consent"));
      // max_age: on when the sign-in is young enough (the provider checks it
      // again), a new sign-in when not.
      d = await decide({ max_age: "3600" }, minuteAgo);
      assert.ok(d?.action === "continue" && d.url.includes("max_age=3600"));
      d = await decide({ max_age: "3600" }, hoursAgo);
      assert.ok(d?.action === "sign-in" && !d.next.includes("max_age"));
      d = await decide({ max_age: "0" }, { signedIn: true, signedInAt: now });
      assert.equal(d?.action, "sign-in");
      d = await decide(
        { max_age: "3600" },
        { signedIn: true, signedInAt: null },
      );
      assert.equal(d?.action, "sign-in");
    });

    test("signed in long ago, max_age sends the browser to sign in again, then on to consent", async () => {
      const b = w.browser();
      await signIn(b, "owner");
      await age(w, b, 2);
      const client = await oauthClient(w, 43001);
      const page = await authorize(b, client.query({ max_age: "600" }));
      assert.equal(
        page.url.pathname,
        "/login",
        "the provider asks for a sign-in",
      );
      const d = await oauthLoginDecision(w.rt, page.params, {
        signedIn: true,
        signedInAt: Date.now() - 2 * 3600_000,
      });
      assert.equal(d?.action, "sign-in");
      assert.equal((await signIn(b, "owner")).status, 200);
      const next = d?.action === "sign-in" ? d.next : "";
      const r = await b.req(next, { headers: NAVIGATE });
      assert.equal(r.status, 302, JSON.stringify(r.data));
      assert.equal(
        new URL(r.headers.get("location")!, PUBLIC_URL).pathname,
        "/oauth/consent",
      );
    });

    test("the check judges the gateway's route, and refuses paths nginx would resolve elsewhere", async () => {
      const b = w.browser();
      await signIn(b, "owner");
      const made = await b.req("/api/account/tokens", {
        method: "POST",
        body: { name: "finance", scopes: ["finance:write"] },
      });
      assert.equal(made.status, 201, JSON.stringify(made.data));
      const token = made.data.token as string;
      const check = (uri: string, route?: string) =>
        nativeCheck(
          w,
          "POST",
          uri,
          token,
          route ? { "x-life-os-route": route } : {},
        );
      // The gateway's location is MCP's: judged as MCP, which no token
      // reaches, whatever the URI says.
      let r = await check("/api/finance/v1/accounts", "mcp");
      assert.equal(r.status, 403);
      assert.equal(
        r.headers.get("x-life-os-auth-error"),
        "credential_not_allowed",
      );
      r = await check("/api/finance/v1/accounts", "tasks");
      assert.equal(r.status, 403);
      assert.equal(r.headers.get("x-life-os-auth-error"), "insufficient_scope");
      r = await check("/api/tasks/v1/tasks", "finance");
      assert.equal(r.status, 204);
      r = await check("/api/finance/v1/accounts", "finance");
      assert.equal(r.status, 204);
      // Without the header (a gateway from before it), the URI decides.
      r = await check("/api/finance/v1/accounts");
      assert.equal(r.status, 204);
      r = await check("/api/tasks/v1/tasks");
      assert.equal(r.status, 403);
      for (const uri of [
        "/api/finance/..%2Fmcp/mcp",
        "/api/finance/%2e%2e/mcp/mcp",
        "/api/finance/../mcp/mcp",
        "/api/finance/.%2E/mcp",
        "/api/finance/v1%5Caccounts",
        "/api/finance/./v1/accounts",
      ]) {
        r = await check(uri, "finance");
        assert.equal(r.status, 403, uri);
        assert.equal(r.headers.get("x-life-os-auth-error"), "forbidden_path");
      }
      // In a query string, an encoded slash is fine.
      r = await check("/api/finance/v1/transactions?q=a%2Fb", "finance");
      assert.equal(r.status, 204);
      // A browser's session gets no further with such a path.
      r = await b.check("GET", "/api/tasks/..%2Ffinance/v1/x", {
        "x-life-os-route": "finance",
      });
      assert.equal(r.status, 403);
      assert.equal(r.headers.get("x-life-os-auth-error"), "forbidden_path");
      r = await b.check("GET", "/api/finance/v1/x", {
        "x-life-os-route": "finance",
      });
      assert.equal(r.status, 204);
    });
  },
);
