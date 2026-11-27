/**
 * Email (P22-16, docs/install/email.md) in the auth flow suite
 * (docs/operations/ci.md#the-auth-flow-suite): the account's address and
 * its verification link, "Forgot password?" with a reset link that signs
 * the account out as ./setup.sh recover-owner's does, sign-in codes by
 * email as a second factor, the security alerts and each person's
 * switches, and the limits on what a stranger can make Life OS send. The
 * notifications service is a stand-in that keeps what it was asked to
 * send. Against a throwaway PostgreSQL (AUTH_TEST_DATABASE_URL);
 * tools/test-auth runs it.
 */
import assert from "node:assert/strict";
import { after, before, describe, test } from "node:test";

import { Client } from "pg";

import { FIRST_USER_ID } from "../../src/lib/auth/constants";
import * as cli from "../../scripts/lifeos-auth.mjs";
import { adminUrl, Browser, PUBLIC_URL } from "./harness";
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

type Sent = {
  to: string;
  subject: string;
  text: string;
  html?: string;
  kind: string;
};

/** A stand-in notifications service: email on or off, and what it sent. */
function notifications(options: { configured: boolean }) {
  const sent: Sent[] = [];
  const state = { configured: options.configured, refuse: "" };
  const fetcher = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === "string" ? input : input.toString());
    const method = init?.method ?? "GET";
    if (url.pathname === "/v1/email/status") {
      return Response.json({
        configured: state.configured,
        state: state.configured ? "idle" : "off",
        problem: null,
        lastSuccessAt: null,
        lastFailureAt: null,
        failingSince: null,
        lastErrorCode: null,
        lastError: null,
      });
    }
    if (url.pathname === "/v1/email/messages" && method === "POST") {
      const headers = new Headers(init?.headers);
      assert.equal(headers.get("x-life-os-actor"), "system:dashboard");
      assert.equal(headers.get("x-life-os-identity"), null);
      if (!state.configured) {
        return Response.json(
          { error: { code: "email_not_configured", message: "off" } },
          { status: 409 },
        );
      }
      if (state.refuse) {
        return Response.json(
          { error: { code: state.refuse, message: "refused" } },
          { status: 502 },
        );
      }
      const message = JSON.parse(String(init?.body)) as Sent;
      // As the service checks it: nothing that loads or runs.
      assert.doesNotMatch(
        message.html ?? "",
        /<img|<script|\bsrc\s*=|\bon[a-z]+\s*=/i,
      );
      sent.push(message);
      return new Response(null, { status: 204 });
    }
    if (url.pathname.startsWith("/v1/push/device-keys/")) {
      return new Response(null, { status: 204 });
    }
    return new Response("not found", { status: 404 });
  }) as typeof fetch;
  return { fetcher, sent, state };
}

async function until(check: () => boolean | Promise<boolean>, what: string) {
  for (let i = 0; i < 100; i += 1) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assert.fail(`waited in vain: ${what}`);
}

/** Sends the alerts that are due, until none is left. */
async function flushAlerts(w: World) {
  for (let i = 0; i < 5; i += 1) {
    await w.rt.emailAlerts!();
    const { rows } = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM email_outbox WHERE next_attempt_at <= now()",
    );
    if (rows[0].n === 0) return;
  }
}

/** A browser's session token, as auth_db keeps it. */
function sessionToken(b: Browser) {
  return decodeURIComponent(b.jar.get("__Host-lifeos.session")!).split(".")[0];
}

/** The token after PUBLIC_URL + path in an email's text (a plain search, no regex built from the URL). */
function linkIn(message: Sent | undefined, path: string) {
  assert.ok(message, "no email");
  const prefix = `${PUBLIC_URL}${path}`;
  const start = message.text.indexOf(prefix);
  assert.ok(start >= 0, message.text);
  const token = /^[A-Za-z0-9_-]+/.exec(
    message.text.slice(start + prefix.length),
  );
  assert.ok(token, message.text);
  return token[0];
}

/** Sets and verifies the owner's address. */
async function verifiedAddress(w: World, sent: Sent[], address: string) {
  const r = await w.owner.req("/api/account/email", {
    method: "PUT",
    body: { address },
  });
  assert.equal(r.status, 200, JSON.stringify(r.data));
  assert.equal(r.data.linkSent, true);
  const token = linkIn(sent.at(-1), "/verify-email/");
  const v = await w.browser().req("/api/auth/email/verify", {
    method: "POST",
    body: { token },
  });
  assert.equal(v.status, 200, JSON.stringify(v.data));
}

describe("email off: nothing changes", { skip }, () => {
  let w: World;
  const service = notifications({ configured: false });
  before(async () => {
    w = await world(admin!, { notifications: service.fetcher });
  });
  after(() => w?.close());

  test("the email routes are hidden, and nothing is sent", async () => {
    let r = await w.owner.req("/api/account/email");
    assert.equal(r.status, 200);
    assert.equal(r.data.enabled, false);
    r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "owner@example.com" },
    });
    assert.equal(r.status, 404);
    assert.equal(r.data.error.code, "email_off");
    r = await w.browser().req("/api/auth/email/forgot-password", {
      method: "POST",
      body: { login: "owner" },
    });
    assert.equal(r.status, 404);
    // An account change queues nothing that goes out.
    r = await w.owner.req("/api/auth/change-password", {
      method: "POST",
      body: { currentPassword: password, newPassword: `${password}!` },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    await flushAlerts(w);
    const left = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM email_outbox",
    );
    assert.equal(left.rows[0].n, 0);
    assert.equal(service.sent.length, 0);
  });

  test("a sign-in offers no code by email", async () => {
    await enableTotp(w.owner, `${password}!`);
    const r = await signIn(w.browser(), "owner", `${password}!`);
    assert.equal(r.data.twoFactorRedirect, true);
    assert.deepEqual(r.data.twoFactorMethods, ["totp"]);
  });
});

describe("the address, its verification, and forgot password", { skip }, () => {
  let w: World;
  const service = notifications({ configured: true });
  before(async () => {
    w = await world(admin!, { notifications: service.fetcher });
  });
  after(() => w?.close());

  test("an address gets a verification link, which works once", async () => {
    let r = await w.owner.req("/api/account/email");
    assert.equal(r.data.enabled, true);
    assert.equal(r.data.address, null);
    r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "not an address" },
    });
    assert.equal(r.status, 400);
    r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "Owner@Example.com" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.address, "owner@example.com");
    assert.equal(r.data.verified, false);
    const mail = service.sent.at(-1)!;
    assert.equal(mail.to, "owner@example.com");
    assert.equal(mail.kind, "verify_email");
    assert.match(mail.text, /Life OS at lifeos\.test/);
    const token = linkIn(mail, "/verify-email/");
    // A second link right away waits.
    r = await w.owner.req("/api/account/email/verification", {
      method: "POST",
    });
    assert.equal(r.status, 429);

    r = await w.browser().req("/api/auth/email/verify", {
      method: "POST",
      body: { token },
      headers: { origin: "https://evil.example" },
    });
    assert.equal(r.status, 403, "only from Life OS's own origin");
    r = await w.browser().req("/api/auth/email/verify", {
      method: "POST",
      body: { token },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await w.browser().req("/api/auth/email/verify", {
      method: "POST",
      body: { token },
    });
    assert.equal(r.status, 400, "used up");
    r = await w.owner.req("/api/account/email");
    assert.equal(r.data.verified, true);
    const kinds = (await securityRows(w)).map((e) => e.kind);
    assert.ok(
      kinds.includes("email_changed") && kinds.includes("email_verified"),
      kinds.join(),
    );
  });

  test("a link for an old address does not verify a new one", async () => {
    w.rt.emailLimiter!.clear();
    let r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "second@example.com" },
    });
    assert.equal(r.status, 200);
    const stale = linkIn(service.sent.at(-1), "/verify-email/");
    // The old, verified address hears of the change.
    await flushAlerts(w);
    const notice = service.sent.find(
      (m) => m.to === "owner@example.com" && m.kind === "alert.password",
    );
    assert.ok(notice, "no alert to the old address");
    assert.match(notice.subject, /email address was changed/);
    w.rt.emailLimiter!.clear();
    r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "owner@example.com" },
    });
    assert.equal(r.status, 200);
    r = await w.browser().req("/api/auth/email/verify", {
      method: "POST",
      body: { token: stale },
    });
    assert.equal(r.status, 400);
    const fresh = linkIn(service.sent.at(-1), "/verify-email/");
    r = await w.browser().req("/api/auth/email/verify", {
      method: "POST",
      body: { token: fresh },
    });
    assert.equal(r.status, 200);
  });

  test("changing the address needs a recent sign-in", async () => {
    await w.rt.pool.query(
      `UPDATE session SET "createdAt" = now() - interval '13 hours'`,
    );
    const r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "late@example.com" },
    });
    assert.equal(r.status, 403);
    assert.equal(r.data.error.code, "reauth_required");
    const c = await w.owner.req("/api/account/confirm-password", {
      method: "POST",
      body: { password },
    });
    assert.equal(c.status, 204);
  });

  test("forgot password: the same answer for anyone, a link only to a verified address", async () => {
    const before = service.sent.length;
    for (const login of ["nobody", "nobody@example.com", ""]) {
      const r = await w.browser().req("/api/auth/email/forgot-password", {
        method: "POST",
        body: { login },
      });
      assert.equal(r.status, login ? 200 : 400, login);
    }
    // An account without a verified address gets nothing either.
    await seedUser(w.db.url, { username: "member", password });
    const r = await w.browser().req("/api/auth/email/forgot-password", {
      method: "POST",
      body: { login: "member" },
    });
    assert.equal(r.status, 200);
    await new Promise((resolve) => setTimeout(resolve, 100));
    assert.equal(service.sent.length, before);
  });

  test("the reset link signs out the account's sessions, devices, tokens, and apps", async () => {
    const b = w.browser();
    assert.equal((await signIn(b, "owner")).status, 200);
    const created = await b.req("/api/account/tokens", {
      method: "POST",
      body: { name: "script", scopes: ["tasks:read"] },
    });
    assert.equal(created.status, 201, JSON.stringify(created.data));
    const apiToken = created.data.token as string;
    const before = service.sent.length;
    const r = await w.browser().req("/api/auth/email/forgot-password", {
      method: "POST",
      body: { login: "OWNER@example.com" },
    });
    assert.equal(r.status, 200);
    assert.equal(r.data.minutes, 30);
    await until(() => service.sent.length > before, "the reset email");
    const mail = service.sent.at(-1)!;
    assert.equal(mail.kind, "password_reset");
    assert.equal(mail.to, "owner@example.com");
    const token = linkIn(mail, "/reset/");
    const reset = await w.browser().req("/api/auth/reset-password", {
      method: "POST",
      body: { token, newPassword: `${password}?` },
    });
    assert.equal(reset.status, 200, JSON.stringify(reset.data));
    assert.equal((await b.check()).status, 401, "sessions signed out");
    const bearer = await new Browser(w.fetcher).check(
      "GET",
      "/api/tasks/v1/tasks",
      {
        authorization: `Bearer ${apiToken}`,
      },
    );
    assert.equal(bearer.status, 401, "the token is revoked");
    const again = await w.browser().req("/api/auth/reset-password", {
      method: "POST",
      body: { token, newPassword: `${password}!?` },
    });
    assert.notEqual(again.status, 200, "the link works once");
    assert.equal(
      (await signIn(w.browser(), "owner", `${password}?`)).status,
      200,
    );
    const rows = await securityRows(w);
    assert.ok(
      rows.some(
        (e) =>
          e.kind === "password_reset_requested" &&
          e.subject_user_id === FIRST_USER_ID,
      ),
    );
    assert.ok(
      rows.some(
        (e) =>
          e.kind === "credentials_revoked" && e.detail === "password_reset",
      ),
    );
    // The reset's alert.
    await flushAlerts(w);
    assert.ok(
      service.sent.some(
        (m) => m.kind === "alert.password" && /reset link/.test(m.subject),
      ),
    );
    // The owner's browser signs in again for the next tests.
    assert.equal((await signIn(w.owner, "owner", `${password}?`)).status, 200);
  });

  test("forgot password is limited per address and per network address", async () => {
    const net = new Browser(w.fetcher, PUBLIC_URL, "198.51.100.77");
    const before = service.sent.length;
    for (let i = 0; i < 4; i += 1) {
      const r = await net.req("/api/auth/email/forgot-password", {
        method: "POST",
        body: { login: "owner" },
      });
      assert.equal(r.status, 200);
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
    // One link went out in the test before: two more this hour, then none.
    assert.equal(
      service.sent.slice(before).filter((m) => m.kind === "password_reset")
        .length,
      2,
    );
    for (let i = 0; i < 6; i += 1) {
      await net.req("/api/auth/email/forgot-password", {
        method: "POST",
        body: { login: "nobody" },
      });
    }
    const r = await net.req("/api/auth/email/forgot-password", {
      method: "POST",
      body: { login: "nobody" },
    });
    assert.equal(r.status, 429);
  });
});

describe("sign-in codes by email and security alerts", { skip }, () => {
  let w: World;
  const service = notifications({ configured: true });
  before(async () => {
    w = await world(admin!, { notifications: service.fetcher });
    await verifiedAddress(w, service.sent, "owner@example.com");
  });
  after(() => w?.close());

  const codeIn = (message: Sent | undefined) => {
    assert.ok(message);
    assert.equal(message.kind, "sign_in_code");
    const match = message.text.match(/^\s{4}(\d{6})$/m);
    assert.ok(match, message.text);
    return match[1];
  };

  test("Better Auth's own switch for codes is refused", async () => {
    const r = await w.owner.req("/api/auth/two-factor/enable", {
      method: "POST",
      body: { password, method: "otp" },
    });
    assert.equal(r.status, 400);
    assert.equal(r.data.code, "USE_SETTINGS");
  });

  test("turning codes on needs a verified address and the password", async () => {
    let r = await w.owner.req("/api/account/email/sign-in-codes", {
      method: "POST",
      body: { enabled: true, password: "wrong password!" },
    });
    assert.equal(r.status, 400);
    r = await w.owner.req("/api/account/email/sign-in-codes", {
      method: "POST",
      body: { enabled: true, password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.signInCodes, true);
    const { rows } = await w.rt.pool.query(
      'SELECT "twoFactorEnabled" AS on FROM "user" WHERE id = $1',
      [FIRST_USER_ID],
    );
    assert.equal(rows[0].on, true);
    await flushAlerts(w);
    const alert = service.sent.find((m) => m.kind === "alert.two_factor");
    assert.ok(alert);
    assert.match(alert.subject, /Sign-in codes by email turned on/);
    // The address stays while codes go there.
    r = await w.owner.req("/api/account/email", {
      method: "PUT",
      body: { address: "other@example.com" },
    });
    assert.equal(r.status, 409);
  });

  test("a sign-in asks for a code, which works once; a new device is told", async () => {
    const b = w.browser();
    const before = service.sent.length;
    let r = await signIn(b, "owner");
    assert.equal(r.status, 200);
    assert.equal(r.data.twoFactorRedirect, true);
    assert.deepEqual(r.data.twoFactorMethods, ["otp"]);
    await flushAlerts(w);
    const newDevice = service.sent
      .slice(before)
      .filter((m) => m.kind === "alert.new_device");
    assert.equal(newDevice.length, 1, "a new browser: one alert");
    assert.match(newDevice[0].text, /Network address: 198\.51\.100\./);

    r = await b.req("/api/auth/two-factor/send-otp", {
      method: "POST",
      body: {},
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const code = codeIn(service.sent.at(-1));
    // Another code at once waits.
    r = await b.req("/api/auth/two-factor/send-otp", {
      method: "POST",
      body: {},
    });
    assert.equal(r.status, 429);
    r = await b.req("/api/auth/two-factor/verify-otp", {
      method: "POST",
      body: { code: code === "000000" ? "111111" : "000000" },
    });
    assert.equal(r.status, 401);
    r = await b.req("/api/auth/two-factor/verify-otp", {
      method: "POST",
      body: { code },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await b.check()).status, 204);
    // Not the second factor the admin surfaces need.
    const session = await w.rt.pool.query(
      `SELECT "secondFactor" FROM session WHERE token = $1`,
      [sessionToken(b)],
    );
    assert.equal(session.rows[0].secondFactor, false);
    const rows = await securityRows(w);
    assert.ok(
      rows.some((e) => e.kind === "sign_in" && e.detail === "email_code"),
    );
    assert.ok(
      rows.some(
        (e) =>
          e.kind === "sign_in_failed" && e.detail === "email_code:invalid_code",
      ),
    );

    // The same browser again: known, no alert.
    const count = service.sent.length;
    await signIn(b, "owner");
    await flushAlerts(w);
    assert.equal(
      service.sent.slice(count).filter((m) => m.kind === "alert.new_device")
        .length,
      0,
    );
  });

  test("a code is only for a sign-in, never for a signed-in session", async () => {
    const r = await w.owner.req("/api/auth/two-factor/send-otp", {
      method: "POST",
      body: {},
    });
    assert.equal(r.status, 403);
  });

  test("with the authenticator app too, both are offered; turning it off keeps codes", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    w.rt.emailLimiter!.clear();
    let sent = await b.req("/api/auth/two-factor/send-otp", {
      method: "POST",
      body: {},
    });
    assert.equal(sent.status, 200, JSON.stringify(sent.data));
    sent = await b.req("/api/auth/two-factor/verify-otp", {
      method: "POST",
      body: { code: codeIn(service.sent.at(-1)) },
    });
    assert.equal(sent.status, 200, JSON.stringify(sent.data));
    // A session signed in with a code may add the authenticator with the
    // password from the last 5 minutes (it has no stronger factor yet).
    const confirm = await b.req("/api/account/confirm-password", {
      method: "POST",
      body: { password },
    });
    assert.equal(confirm.status, 204);
    const before = service.sent.length;
    await enableTotp(b);
    await flushAlerts(w);
    assert.ok(
      service.sent
        .slice(before)
        .some(
          (m) =>
            m.kind === "alert.two_factor" &&
            /authenticator app was turned on/.test(m.text),
        ),
    );
    const session = await w.rt.pool.query(
      `SELECT "secondFactor" FROM session WHERE token = $1`,
      [sessionToken(b)],
    );
    assert.equal(
      session.rows[0].secondFactor,
      true,
      "the first code proves the app",
    );
    let r = await signIn(w.browser(), "owner");
    assert.deepEqual(r.data.twoFactorMethods, ["totp", "otp"]);
    r = await b.req("/api/auth/two-factor/disable", {
      method: "POST",
      body: { password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await signIn(w.browser(), "owner");
    assert.equal(r.data.twoFactorRedirect, true, "codes still asked for");
    assert.deepEqual(r.data.twoFactorMethods, ["otp"]);
  });

  test("each person's switches: an alert kind turned off is not sent", async () => {
    let r = await w.owner.req("/api/account/email/preferences", {
      method: "PUT",
      body: { alerts: { credentials: false, nonsense: true } },
    });
    assert.equal(r.status, 400);
    const other = await seedUser(w.db.url, { username: "member2", password });
    r = await w.owner.req("/api/account/email/preferences", {
      method: "PUT",
      body: { userId: other, alerts: { credentials: false } },
    });
    assert.equal(r.status, 200);
    assert.equal(r.data.alerts.credentials, false);
    const others = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM email_preferences WHERE user_id = $1",
      [other],
    );
    assert.equal(others.rows[0].n, 0, "only the person's own");
    const before = service.sent.length;
    r = await w.owner.req("/api/account/tokens", {
      method: "POST",
      body: { name: "quiet", scopes: ["tasks:read"] },
    });
    assert.equal(r.status, 201);
    await flushAlerts(w);
    assert.equal(
      service.sent.slice(before).filter((m) => m.kind === "alert.credentials")
        .length,
      0,
    );
    r = await w.owner.req("/api/account/email/preferences", {
      method: "PUT",
      body: { alerts: { credentials: true } },
    });
    r = await w.owner.req("/api/account/tokens", {
      method: "POST",
      body: { name: "loud", scopes: ["tasks:read"] },
    });
    await flushAlerts(w);
    const alert = service.sent
      .slice(before)
      .find((m) => m.kind === "alert.credentials");
    assert.ok(alert);
    assert.match(alert.subject, /New API token/);
    // Legacy access stays off while two accounts exist: the next test
    // turns it on.
    await w.rt.pool.query('DELETE FROM "user" WHERE id = $1', [other]);
    assert.match(
      alert.text,
      /Settings → Email \(https:\/\/lifeos\.test\/settings\/email\)/,
    );
  });

  test("the host's commands alert too: legacy access on, everyone signed out", async () => {
    const client = new Client({ connectionString: w.db.url });
    await client.connect();
    try {
      assert.equal(await cli.setLegacyAccess(client, true), "ok");
      await cli.setLegacyAccess(client, false);
      await cli.signOutAll(client);
    } finally {
      await client.end();
    }
    await flushAlerts(w);
    assert.ok(service.sent.some((m) => m.kind === "alert.legacy_access"));
    assert.ok(
      service.sent.some(
        (m) =>
          m.kind === "alert.sign_out" &&
          /Everyone was signed out/.test(m.subject),
      ),
    );
  });

  test("a failing provider is retried, a refused address is dropped", async () => {
    service.state.refuse = "email_connect_failed";
    const b = w.browser();
    await signIn(b, "owner");
    await w.rt.pool.query("DELETE FROM email_outbox");
    await w.rt.pool.query(
      `INSERT INTO email_outbox (user_id, kind, event) VALUES ($1, 'sign_out', 'signed_out_everywhere')`,
      [FIRST_USER_ID],
    );
    await w.rt.emailAlerts!();
    let { rows } = await w.rt.pool.query("SELECT attempts FROM email_outbox");
    assert.deepEqual(
      rows.map((row) => row.attempts),
      [1],
    );
    service.state.refuse = "email_recipient_refused";
    await w.rt.pool.query("UPDATE email_outbox SET next_attempt_at = now()");
    await w.rt.emailAlerts!();
    ({ rows } = await w.rt.pool.query("SELECT attempts FROM email_outbox"));
    assert.equal(rows.length, 0);
    service.state.refuse = "";
  });

  test("recover-owner --reset-2fa turns codes off too", async () => {
    const client = new Client({ connectionString: w.db.url });
    await client.connect();
    try {
      await cli.recoverOwner(client, PUBLIC_URL, { resetTwoFactor: true });
    } finally {
      await client.end();
    }
    const r = await signIn(w.browser(), "owner");
    assert.equal(r.status, 200);
    assert.notEqual(r.data.twoFactorRedirect, true);
  });
});
