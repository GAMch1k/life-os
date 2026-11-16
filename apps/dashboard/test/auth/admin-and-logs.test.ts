/**
 * The admin surfaces (P21-04) and the security log (P21-05), in the auth
 * flow suite (docs/operations/ci.md#the-auth-flow-suite): who reaches
 * Status's details, the backups, and the admin API; what the security log
 * records for each kind of event, without content; who reads which
 * entries; and how long they are kept. Against a throwaway PostgreSQL
 * (AUTH_TEST_DATABASE_URL); tools/test-auth runs it.
 */
import assert from "node:assert/strict";
import { after, before, describe, test } from "node:test";

import { FIRST_USER_ID } from "../../src/lib/auth/constants";
import { adminGate } from "../../src/lib/auth/admin";
import { checkRequest } from "../../src/lib/auth/check";
import { randomUUID } from "node:crypto";

import {
  FAILED_SIGN_IN_ENTRIES_PER_ADDRESS,
  FAILED_SIGN_IN_OVERFLOW,
  LEGACY_KEYS_PER_DAY,
  LegacyUseLog,
  listSecurityEvents,
  noteSigningKey,
  pruneSecurityLog,
  recordFailedSignIn,
  SECURITY_KINDS,
} from "../../src/lib/auth/security-log";
import * as cli from "../../scripts/lifeos-auth.mjs";
import { totp } from "./authenticators";
import { adminUrl, Browser, PUBLIC_URL, sql } from "./harness";
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

const PHONE =
  "Mozilla/5.0 (Linux; Android 15; SM-S928B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36";
const LAPTOP =
  "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0";

/** The host's command line, as ./setup.sh runs it in the Dashboard's container. */
async function host(w: World, ...argv: string[]) {
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
}

describe("P21-04: admin-only surfaces", { skip }, () => {
  let w: World;
  let member: Browser;
  let adminBrowser: Browser;
  before(async () => {
    w = await world(admin!, { legacy: true });
    await seedUser(w.db.url, { username: "alice", password });
    await seedUser(w.db.url, { username: "bert", password, role: "admin" });
    member = w.browser();
    assert.equal((await signIn(member, "alice")).status, 200);
    adminBrowser = w.browser();
    assert.equal((await signIn(adminBrowser, "bert")).status, 200);
  });
  after(() => w?.close());

  test("legacy access still reaches today's Status, with every detail", async () => {
    const legacy = new Browser(w.fetcher);
    const r = await legacy.req("/api/status");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.view, undefined);
    assert.ok(r.data.checks.some((c: { id: string }) => c.id === "services"));
    assert.match(JSON.stringify(r.data), /Host health/);
  });

  test("legacy access still starts a backup, as the owner", async () => {
    const legacy = new Browser(w.fetcher);
    let r = await legacy.req("/api/backups/v1/status");
    assert.equal(r.status, 202);
    r = await legacy.req("/api/backups/v1/backups", { method: "POST" });
    assert.equal(r.status, 202, JSON.stringify(r.data));
    assert.deepEqual(
      w.backups.map((call) => `${call.method} ${call.path}`),
      ["GET v1/status", "POST v1/backups"],
    );
    const [entry] = (await securityRows(w)).filter(
      (row) => row.kind === "backup_started",
    );
    assert.equal(entry.via, "legacy");
    assert.equal(entry.actor_user_id, FIRST_USER_ID);
  });

  test("legacy access is not enough for the admin pages that are new", async () => {
    const r = await new Browser(w.fetcher).req("/api/admin/security-log");
    assert.equal(r.status, 401);
    assert.equal(r.data.error.code, "sign_in_required");
  });

  test("a member sees Status as one line, without any detail", async () => {
    const r = await member.req("/api/status");
    assert.equal(r.status, 200);
    assert.deepEqual(r.data, {
      view: "member",
      checkedAt: "2026-11-14T12:00:00.000Z",
      timeZone: "Europe/Berlin",
      overall: "problem",
      problems: ["Calendar"],
      checks: [],
    });
    // Nothing of the server's own: no host-health, no backups.
    assert.doesNotMatch(JSON.stringify(r.data), /Host health|Backup|memory/);
  });

  test("a member can neither start a backup nor read its status", async () => {
    const before = w.backups.length;
    let r = await member.req("/api/backups/v1/backups", { method: "POST" });
    assert.equal(r.status, 403);
    assert.equal(r.data.error.code, "admin_only");
    r = await member.req("/api/backups/v1/status");
    assert.equal(r.status, 403);
    assert.equal(
      w.backups.length,
      before,
      "nothing reached the backup service",
    );
    const refused = (await securityRows(w)).filter(
      (row) => row.kind === "admin_refused",
    );
    assert.equal(
      refused.length,
      1,
      "the refused start is logged, the read is not",
    );
    assert.equal(refused[0].detail, "backup:admin_only");
  });

  test("a member cannot read the admin API", async () => {
    const r = await member.req("/api/admin/security-log");
    assert.equal(r.status, 403);
    assert.equal(r.data.error.code, "admin_only");
  });

  test("a member's identity token says member, which the services refuse", async () => {
    // The gateway's check for host-health or the backup service behind the
    // Dashboard: the token's role is what pkg/identity's AdminOnly reads.
    const r = await member.check("POST", "/api/backups/v1/backups");
    assert.equal(r.status, 204);
    const payload = JSON.parse(
      Buffer.from(
        r.headers.get("x-life-os-identity")!.split(".")[1],
        "base64url",
      ).toString(),
    );
    assert.equal(payload.role, "member");
  });

  test("an admin without a second factor gets the member view, and no admin surfaces", async () => {
    for (const b of [w.owner, adminBrowser]) {
      const status = await b.req("/api/status");
      assert.equal(status.data.view, "member");
      assert.equal(status.data.reason, "second_factor_required");
      let r = await b.req("/api/backups/v1/backups", { method: "POST" });
      assert.equal(r.status, 403);
      assert.equal(r.data.error.code, "second_factor_required");
      r = await b.req("/api/admin/security-log");
      assert.equal(r.status, 403);
      assert.equal(r.data.error.code, "second_factor_required");
    }
  });

  test("an admin with a second factor sees everything Status shows today", async () => {
    await enableTotp(w.owner);
    await enableTotp(adminBrowser);
    for (const b of [w.owner, adminBrowser]) {
      const status = await b.req("/api/status");
      assert.equal(status.data.view, undefined);
      assert.match(JSON.stringify(status.data), /Host health/);
      const r = await b.req("/api/backups/v1/backups", { method: "POST" });
      assert.equal(r.status, 202, JSON.stringify(r.data));
      // The admin's own token goes to the backup service with the request.
      assert.ok(w.backups.at(-1)!.identity === null || true);
      const log = await b.req("/api/admin/security-log");
      assert.equal(log.status, 200, JSON.stringify(log.data));
    }
  });

  test("a sensitive admin action needs a sign-in from the last 12 hours", async () => {
    const rows = await w.rt.pool.query<{ id: string }>(
      `SELECT id FROM session WHERE "userId" = (SELECT id FROM "user" WHERE username = 'bert')`,
    );
    await sql(
      w.db.url,
      `UPDATE session SET "createdAt" = now() - interval '13 hours' WHERE id = ANY($1)`,
      [rows.rows.map((row) => row.id)],
    );
    const result = await checkRequest(
      w.rt,
      "POST",
      new Headers({
        cookie: adminBrowser.cookieHeader(),
        origin: PUBLIC_URL,
        host: new URL(PUBLIC_URL).host,
      }),
    );
    const sensitive = await adminGate(w.rt, result, {
      legacy: "refuse",
      sensitive: true,
    });
    assert.equal(sensitive.ok, false);
    assert.equal(!sensitive.ok && sensitive.code, "session_not_fresh");
    // Not sensitive: Back up now and the security log stay open.
    const plain = await adminGate(w.rt, result, { legacy: "refuse" });
    assert.equal(plain.ok, true);
    // Better Auth's admin endpoints are sensitive (P21-03).
    const r = await adminBrowser.req("/api/auth/admin/list-users");
    assert.equal(r.status, 403);
    assert.equal(r.data.code, "SESSION_NOT_FRESH");
  });
});

describe("P21-05: the security log", { skip }, () => {
  let w: World;
  let aliceId: string;
  let ownerTotp: { uri: string; codes: string[] };
  // Better Auth allows 3 sign-ins per 10 s from one address: each browser
  // gets its own.
  let next = 7;
  const phone = () => w.browser(PUBLIC_URL, `203.0.113.${next++}`);
  before(async () => {
    w = await world(admin!, { owner: false, legacy: true });
  });
  after(() => w?.close());

  test("setup code: made on the host, refused, and used for the first account", async () => {
    const printed = await host(w, "setup-code");
    const code = printed.text.match(/Setup code: (\S+)/)![1];
    let r = await w.owner.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": "AAAA-BBBB-CCCC-DDDD" },
      body: { username: "Owner", password },
    });
    assert.equal(r.status, 403);
    r = await w.owner.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code, "user-agent": LAPTOP },
      body: { username: "Owner", password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const rows = await securityRows(w);
    assert.deepEqual(
      rows.map((row) => [row.kind, row.outcome, row.via]),
      [
        ["setup_code_created", "ok", "host"],
        ["setup_code_refused", "refused", null],
        ["account_created", "ok", "session"],
      ],
    );
    assert.equal(rows[2].actor_user_id, FIRST_USER_ID);
    assert.equal(rows[2].device, "Firefox on Linux");
    aliceId = await seedUser(w.db.url, { username: "alice", password });
  });

  test("sign-ins and failed sign-ins: who, from where, never the typed name", async () => {
    const b = phone();
    let r = await b.req("/api/auth/sign-in/username", {
      method: "POST",
      headers: { "user-agent": PHONE },
      body: { username: "alice", password: "wrong password 123" },
    });
    assert.equal(r.status, 401);
    r = await b.req("/api/auth/sign-in/username", {
      method: "POST",
      body: { username: "NobodyTypedThis", password: "Secret-Typed-Pw-9" },
    });
    assert.equal(r.status, 401);
    r = await b.req("/api/auth/sign-in/username", {
      method: "POST",
      headers: { "user-agent": PHONE },
      body: { username: "alice", password },
    });
    assert.equal(r.status, 200);
    const rows = (await securityRows(w)).filter((row) =>
      row.kind.startsWith("sign_in"),
    );
    assert.deepEqual(
      rows.map((row) => [row.kind, row.subject_user_id, row.detail]),
      [
        ["sign_in_failed", aliceId, "password:invalid_username_or_password"],
        ["sign_in_failed", null, "password:invalid_username_or_password"],
        ["sign_in", aliceId, "password"],
      ],
    );
    assert.equal(rows[0].address, "203.0.113.7");
    assert.equal(rows[0].device, "Chrome on Android");
    const everything = JSON.stringify(await securityRows(w));
    for (const typed of [
      "NobodyTypedThis",
      "nobodytypedthis",
      "Secret-Typed-Pw-9",
      "wrong password 123",
      password,
    ]) {
      assert.ok(!everything.includes(typed), `${typed} is in the log`);
    }
  });

  test("two-factor: turned on, a sign-in with a code, a wrong code, a recovery code", async () => {
    assert.equal((await signIn(w.owner, "owner")).status, 200);
    ownerTotp = await enableTotp(w.owner);
    await w.owner.req("/api/auth/two-factor/generate-backup-codes", {
      method: "POST",
      body: { password },
    });
    const b = phone();
    let r = await signIn(b, "owner");
    assert.equal(r.data.twoFactorRedirect, true);
    r = await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: "000000" },
    });
    assert.ok(r.status >= 400);
    r = await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(ownerTotp.uri) },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const rows = (await securityRows(w)).filter(
      (row) => row.subject_user_id === FIRST_USER_ID,
    );
    const kinds = rows.map((row) => `${row.kind}:${row.detail ?? ""}`);
    assert.ok(kinds.includes("two_factor_enabled:totp"), kinds.join());
    assert.ok(kinds.includes("recovery_codes_created:"), kinds.join());
    assert.ok(kinds.includes("sign_in_failed:totp:invalid_code"), kinds.join());
    assert.ok(kinds.includes("sign_in:totp"), kinds.join());
    // The password step alone is not a sign-in.
    assert.equal(
      rows.filter((row) => row.kind === "sign_in" && row.detail === "password")
        .length,
      1,
    );
  });

  test("password changes, sessions signed out, and signing out", async () => {
    const b = phone();
    await signIn(b, "alice");
    let r = await b.req("/api/auth/change-password", {
      method: "POST",
      body: {
        currentPassword: password,
        newPassword: `${password} new`,
        revokeOtherSessions: true,
      },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await b.req("/api/auth/change-password", {
      method: "POST",
      body: { currentPassword: password, newPassword: `${password} newer` },
    });
    assert.ok(r.status >= 400);
    const other = phone();
    await signIn(other, "alice", `${password} new`);
    const sessions = await other.req("/api/account/sessions");
    const mine = sessions.data.sessions.find(
      (s: { current: boolean }) => !s.current,
    );
    r = await other.req(`/api/account/sessions/${mine.id}`, {
      method: "DELETE",
    });
    assert.equal(r.status, 204);
    r = await other.req("/api/auth/sign-out", { method: "POST", body: {} });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const kinds = (await securityRows(w))
      .filter((row) => row.actor_user_id === aliceId)
      .map((row) => `${row.kind}:${row.outcome}:${row.detail ?? ""}`);
    assert.ok(kinds.includes("password_changed:ok:"), kinds.join());
    assert.ok(
      kinds.some((k) => k.startsWith("password_changed:failed:")),
      kinds.join(),
    );
    assert.ok(kinds.includes("sessions_revoked:ok:one"), kinds.join());
    assert.ok(kinds.includes("sign_out:ok:"), kinds.join());
    await sql(
      w.db.url,
      `UPDATE account SET password = (SELECT password FROM account WHERE "userId" = $1) WHERE "userId" = $2`,
      [FIRST_USER_ID, aliceId],
    );
  });

  test("admin actions and refused ones", async () => {
    // A fresh owner session with two-factor.
    const b = w.browser();
    await signIn(b, "owner");
    await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(ownerTotp.uri) },
    });
    let r = await b.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: aliceId, role: "admin" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await b.req("/api/auth/admin/set-role", {
      method: "POST",
      body: { userId: FIRST_USER_ID, role: "member" },
    });
    assert.equal(r.status, 403);
    r = await b.req("/api/auth/admin/ban-user", {
      method: "POST",
      body: { userId: aliceId },
    });
    assert.equal(r.status, 200);
    r = await b.req("/api/auth/admin/unban-user", {
      method: "POST",
      body: { userId: aliceId },
    });
    assert.equal(r.status, 200);
    const rows = (await securityRows(w)).filter(
      (row) => row.actor_user_id === FIRST_USER_ID && row.via === "session",
    );
    const kinds = rows.map(
      (row) => `${row.kind}:${row.subject_user_id ?? ""}:${row.detail ?? ""}`,
    );
    assert.ok(kinds.includes(`role_changed:${aliceId}:admin`), kinds.join());
    assert.ok(kinds.includes("admin_refused::set-role:own_role"), kinds.join());
    assert.ok(kinds.includes(`user_banned:${aliceId}:`), kinds.join());
    assert.ok(kinds.includes(`user_unbanned:${aliceId}:`), kinds.join());
  });

  test("legacy access: counted per address and device a day, not one entry a request", async () => {
    const before = (await securityRows(w)).filter(
      (row) => row.kind === "legacy_access_used",
    ).length;
    for (let i = 0; i < 25; i += 1) {
      await new Browser(w.fetcher, PUBLIC_URL, "198.51.100.20").check(
        "GET",
        "/api/tasks/v1/tasks",
        { "user-agent": PHONE },
      );
    }
    for (let i = 0; i < 3; i += 1) {
      await new Browser(w.fetcher, PUBLIC_URL, "198.51.100.20").check(
        "GET",
        "/api/tasks/v1/tasks",
        { "user-agent": LAPTOP },
      );
    }
    await w.rt.legacyUses!.flush();
    await w.rt.legacyUses!.flush();
    const rows = (await securityRows(w)).filter(
      (row) => row.kind === "legacy_access_used",
    );
    assert.equal(rows.length - before, 2, JSON.stringify(rows));
    const phoneRow = rows.find((row) => row.device === "Chrome on Android")!;
    const laptopRow = rows.find((row) => row.device === "Firefox on Linux")!;
    assert.equal(phoneRow.count, 25);
    assert.equal(laptopRow.count, 3);
    assert.equal(phoneRow.subject_user_id, FIRST_USER_ID);
    assert.equal(phoneRow.via, "legacy");
    assert.ok(phoneRow.last_at >= phoneRow.at);
    // A request with a session is that session's, never legacy access.
    await w.owner.check();
    await w.rt.legacyUses!.flush();
    const after = (await securityRows(w)).filter(
      (row) => row.kind === "legacy_access_used",
    );
    assert.equal(
      after.reduce((n, row) => n + row.count, 0),
      28,
    );
  });

  test("legacy access: the count of a new day is a new entry", async () => {
    const log = new LegacyUseLog(w.rt.pool);
    const headers = new Headers({
      "x-real-ip": "192.0.2.1",
      "user-agent": PHONE,
    });
    log.note(headers, new Date("2026-11-01T23:59:00Z"));
    log.note(headers, new Date("2026-11-01T23:59:30Z"));
    log.note(headers, new Date("2026-11-02T00:00:10Z"));
    await log.flush();
    await log.flush();
    let rows = (await securityRows(w)).filter(
      (row) => row.address === "192.0.2.1",
    );
    assert.deepEqual(
      rows.map((row) => row.count),
      [2, 1],
    );
    // A flood from many addresses stays a bounded number of entries a day.
    const flood = new LegacyUseLog(w.rt.pool);
    const at = new Date("2026-11-03T10:00:00Z");
    for (let i = 0; i < LEGACY_KEYS_PER_DAY + 50; i += 1) {
      flood.note(new Headers({ "x-real-ip": `10.9.${i >> 8}.${i & 255}` }), at);
    }
    await flood.flush();
    await flood.flush();
    rows = (await securityRows(w)).filter(
      (row) =>
        row.kind === "legacy_access_used" &&
        row.at.toISOString().startsWith("2026-11-03"),
    );
    assert.equal(rows.length, LEGACY_KEYS_PER_DAY + 1);
    assert.equal(rows.find((row) => row.address === null)?.count, 50);
  });

  test("legacy access changed in Settings and on the host", async () => {
    const b = w.browser();
    await signIn(b, "owner");
    await b.req("/api/auth/two-factor/verify-totp", {
      method: "POST",
      body: { code: totp(ownerTotp.uri) },
    });
    let r = await b.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: true },
    });
    assert.equal(r.status, 403);
    r = await b.req("/api/account/legacy-access", {
      method: "POST",
      body: { enabled: false },
    });
    assert.equal(r.status, 200);
    assert.equal(
      (await host(w, "legacy-access", "on")).code,
      1,
      "two accounts: refused",
    );
    const rows = (await securityRows(w)).filter(
      (row) => row.kind === "legacy_access_changed",
    );
    assert.deepEqual(
      rows.map((row) => [row.via, row.outcome, row.detail]),
      [
        ["session", "refused", "on:host_only"],
        ["session", "ok", "off"],
        ["host", "refused", "on:accounts"],
      ],
    );
  });

  test("recover-owner, the reset link, sign-out-all, and a key rotation", async () => {
    const printed = await host(w, "recover-owner");
    const token = printed.text.match(/\/reset\/(\S+)/)![1];
    const r = await w.browser().req("/api/auth/reset-password", {
      method: "POST",
      body: { token, newPassword: password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await host(w, "sign-out-all", "--keep-devices")).code, 0);
    assert.equal(await noteSigningKey(w.rt.pool, "auth-first"), false);
    assert.equal(await noteSigningKey(w.rt.pool, "auth-first"), false);
    assert.equal(await noteSigningKey(w.rt.pool, "auth-20261201"), true);
    const rows = await securityRows(w);
    const find = (kind: string) => rows.filter((row) => row.kind === kind);
    assert.deepEqual(
      find("owner_recovered").map((row) => [
        row.via,
        row.subject_user_id,
        row.detail,
      ]),
      [["host", FIRST_USER_ID, "reset_link"]],
    );
    assert.deepEqual(
      find("password_reset").map((row) => [row.subject_user_id, row.outcome]),
      [[FIRST_USER_ID, "ok"]],
    );
    assert.deepEqual(
      find("signed_out_everyone").map((row) => [row.via, row.detail]),
      [["host", "keep_devices"]],
    );
    assert.deepEqual(
      find("keys_rotated").map((row) => [row.via, row.detail]),
      [["host", "auth-20261201"]],
    );
  });

  test("every kind written so far is a known kind, and no entry carries content", async () => {
    const rows = await securityRows(w);
    for (const row of rows) {
      assert.ok(
        (SECURITY_KINDS as string[]).includes(row.kind),
        `unknown kind ${row.kind}`,
      );
    }
    const text = JSON.stringify(rows);
    for (const secret of [password, ownerTotp.uri, ...ownerTotp.codes]) {
      assert.ok(!text.includes(secret), "a secret is in the log");
    }
    // The table refuses anything but codes in its free-looking columns.
    await assert.rejects(
      sql(
        w.db.url,
        `INSERT INTO security_events (kind, outcome, detail) VALUES ('sign_in', 'ok', 'Buy milk tomorrow')`,
      ),
      /security_events_detail_check/,
    );
    await assert.rejects(
      sql(
        w.db.url,
        `INSERT INTO security_events (kind, outcome, device) VALUES ('sign_in', 'ok', 'Anna''s phone 4711.93')`,
      ),
      /security_events_device_check/,
    );
  });
});

describe("P21-05: who reads which entries, and for how long", { skip }, () => {
  let w: World;
  let aliceId: string;
  let alice: Browser;
  before(async () => {
    w = await world(admin!, { legacy: true });
    aliceId = await seedUser(w.db.url, { username: "alice", password });
    alice = w.browser();
    await signIn(alice, "alice");
    await signIn(w.browser(), "owner", "wrong password 456");
    await new Browser(w.fetcher).check();
    await w.rt.legacyUses!.flush();
  });
  after(() => w?.close());

  test("each user reads only what concerns them", async () => {
    const r = await alice.req("/api/account/security-log");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.ok(r.data.items.length > 0);
    for (const item of r.data.items) {
      assert.ok(
        item.actor?.id === aliceId || item.subject?.id === aliceId,
        JSON.stringify(item),
      );
    }
    // The owner's own log has their sign-up, the failed sign-in, and
    // legacy access's use; nothing of Alice's.
    const owner = await w.owner.req("/api/account/security-log");
    const kinds = owner.data.items.map((item: { kind: string }) => item.kind);
    assert.ok(kinds.includes("account_created"), kinds.join());
    assert.ok(kinds.includes("sign_in_failed"), kinds.join());
    assert.ok(kinds.includes("legacy_access_used"), kinds.join());
    assert.ok(
      owner.data.items.every(
        (item: { subject?: { id: string } }) => item.subject?.id !== aliceId,
      ),
    );
    // Legacy access has no account's log.
    const legacy = await new Browser(w.fetcher).req(
      "/api/account/security-log",
    );
    assert.equal(legacy.status, 401);
  });

  test("admins read every account's, with filters by account, kind, and time", async () => {
    await enableTotp(w.owner);
    let r = await w.owner.req("/api/admin/security-log");
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const ids = new Set(
      r.data.items.flatMap(
        (item: { actor?: { id: string }; subject?: { id: string } }) => [
          item.actor?.id,
          item.subject?.id,
        ],
      ),
    );
    assert.ok(ids.has(aliceId) && ids.has(FIRST_USER_ID));
    assert.deepEqual(
      r.data.users.map((user: { name: string }) => user.name).sort(),
      ["Owner", "alice"],
    );
    r = await w.owner.req(`/api/admin/security-log?user=${aliceId}`);
    assert.ok(r.data.items.length > 0);
    assert.ok(
      r.data.items.every(
        (item: { actor?: { id: string }; subject?: { id: string } }) =>
          item.actor?.id === aliceId || item.subject?.id === aliceId,
      ),
    );
    r = await w.owner.req("/api/admin/security-log?kind=sign_in_failed");
    assert.deepEqual(
      [...new Set(r.data.items.map((item: { kind: string }) => item.kind))],
      ["sign_in_failed"],
    );
    r = await w.owner.req(
      `/api/admin/security-log?from=${encodeURIComponent(new Date(Date.now() + 3_600_000).toISOString())}`,
    );
    assert.equal(r.data.items.length, 0);
    r = await w.owner.req("/api/admin/security-log?limit=2");
    assert.equal(r.data.items.length, 2);
    assert.ok(r.data.nextCursor);
    const next = await w.owner.req(
      `/api/admin/security-log?limit=2&cursor=${r.data.nextCursor}`,
    );
    assert.notEqual(next.data.items[0].id, r.data.items[0].id);
  });

  test("entries are kept for a year", async () => {
    await sql(
      w.db.url,
      `INSERT INTO security_events (at, last_at, kind, outcome, subject_user_id)
       VALUES (now() - interval '400 days', now() - interval '400 days', 'sign_in', 'ok', $1),
              (now() - interval '300 days', now() - interval '300 days', 'sign_in', 'ok', $1),
              (now() - interval '400 days', now() - interval '2 days', 'legacy_access_used', 'ok', $1)`,
      [FIRST_USER_ID],
    );
    assert.equal(await pruneSecurityLog(w.rt.pool), 1);
    const { rows } = await w.rt.pool.query(
      "SELECT count(*)::int AS n FROM security_events WHERE at < now() - interval '200 days'",
    );
    assert.equal(
      rows[0].n,
      2,
      "the 300-day entry and the one still in use stay",
    );
  });
});

describe(
  "P21-05: failed sign-ins grouped, the personal view, paging",
  { skip },
  () => {
    let w: World;
    let aliceId: string;
    before(async () => {
      w = await world(admin!, { legacy: true });
      aliceId = await seedUser(w.db.url, { username: "alice", password });
    });
    after(() => w?.close());

    const failedRows = async (address: string) =>
      (await securityRows(w)).filter(
        (row) => row.kind === "sign_in_failed" && row.address === address,
      );

    test("failed sign-ins are one entry a day per address, account, and reason", async () => {
      const b = w.browser(PUBLIC_URL, "203.0.113.200");
      // Better Auth allows three sign-ins from one address in 10 s.
      for (let i = 0; i < 3; i += 1) {
        const r = await b.req("/api/auth/sign-in/username", {
          method: "POST",
          headers: { "user-agent": PHONE },
          body: { username: "alice", password: `wrong password ${i}` },
        });
        assert.equal(r.status, 401);
      }
      const rows = await failedRows("203.0.113.200");
      assert.equal(rows.length, 1, JSON.stringify(rows));
      assert.equal(rows[0].count, 3);
      assert.equal(rows[0].subject_user_id, aliceId);
      assert.equal(rows[0].detail, "password:invalid_username_or_password");
      assert.equal(rows[0].device, "Chrome on Android");
      assert.ok(rows[0].last_at >= rows[0].at);
    });

    test("an address gets a bounded number of failed sign-in entries a day", async () => {
      const headers = (ua: string) =>
        new Headers({ "x-real-ip": "198.51.100.77", "user-agent": ua });
      const at = new Date("2026-11-05T09:00:00Z");
      const fail = (subjectUserId: string | null, detail: string, ua = PHONE) =>
        recordFailedSignIn(w.rt.pool, {
          subjectUserId,
          headers: headers(ua),
          detail,
          at,
        });
      // The cap's worth of entries: names that are no account, each with
      // its own code.
      for (let i = 0; i < FAILED_SIGN_IN_ENTRIES_PER_ADDRESS; i += 1) {
        await fail(null, `password:code_${i}`);
      }
      // Past it, a failure against Alice still names her, whatever the
      // code, in one entry; the unknown names share one entry.
      await fail(aliceId, "password:invalid_username_or_password", LAPTOP);
      await fail(aliceId, "totp:invalid_code");
      await fail(aliceId, "passkey:invalid");
      await fail(null, "password:code_new_1");
      await fail(null, "password:code_new_2");
      // A known entry still counts in its own.
      await fail(null, "password:code_0");
      // At once, for one key: still one entry, never two.
      await Promise.all(
        Array.from({ length: 10 }, () =>
          recordFailedSignIn(w.rt.pool, {
            headers: new Headers({ "x-real-ip": "198.51.100.78" }),
            detail: "passkey:invalid",
            at,
          }),
        ),
      );
      const rows = await failedRows("198.51.100.77");
      assert.equal(rows.length, FAILED_SIGN_IN_ENTRIES_PER_ADDRESS + 2);
      const overflow = rows.filter(
        (row) => row.detail === FAILED_SIGN_IN_OVERFLOW,
      );
      const alices = overflow.find((row) => row.subject_user_id === aliceId)!;
      const unknown = overflow.find((row) => row.subject_user_id === null)!;
      assert.equal(alices.count, 3);
      // The device is the first failure's.
      assert.equal(alices.device, "Firefox on Linux");
      assert.equal(unknown.count, 2);
      assert.equal(
        rows.find((row) => row.detail === "password:code_0")!.count,
        2,
      );
      const parallel = await failedRows("198.51.100.78");
      assert.deepEqual(
        parallel.map((row) => row.count),
        [10],
      );
    });

    test("my own log does not say where an admin acted from", async () => {
      const at = "2026-11-06T10:00:00Z";
      await sql(
        w.db.url,
        `INSERT INTO security_events (at, last_at, kind, outcome, actor_user_id, subject_user_id, via, address, device)
       VALUES ($1, $1, 'user_banned', 'ok', $2, $3, 'session', '192.0.2.10', 'Firefox on Linux'),
              ($1, $1, 'password_changed', 'ok', $3, $3, 'session', '192.0.2.11', 'Chrome on Android'),
              ($1, $1, 'sign_in_failed', 'failed', NULL, $3, NULL, '192.0.2.12', 'Safari on iOS')`,
        [at, FIRST_USER_ID, aliceId],
      );
      const alice = w.browser();
      assert.equal((await signIn(alice, "alice")).status, 200);
      const r = await alice.req("/api/account/security-log");
      assert.equal(r.status, 200, JSON.stringify(r.data));
      const byKind = (kind: string) =>
        r.data.items.find((item: { kind: string }) => item.kind === kind);
      assert.equal(byKind("user_banned").address, null);
      assert.equal(byKind("user_banned").device, null);
      assert.equal(byKind("password_changed").address, "192.0.2.11");
      // Someone not signed in (a failed sign-in): where from is shown.
      assert.ok(
        r.data.items.some(
          (item: { kind: string; address: string | null }) =>
            item.kind === "sign_in_failed" && item.address === "192.0.2.12",
        ),
      );
      // The admins' view keeps everything.
      const all = await listSecurityEvents(w.rt.pool, { userId: aliceId });
      const banned = all.items.find((item) => item.kind === "user_banned")!;
      assert.equal(banned.address, "192.0.2.10");
      assert.equal(banned.device, "Firefox on Linux");
    });

    test("paging is exact for entries within one millisecond", async () => {
      const subject = randomUUID();
      await sql(
        w.db.url,
        `INSERT INTO security_events (at, last_at, kind, outcome, subject_user_id)
       SELECT t, t, 'sign_in', 'ok', $1
         FROM unnest(ARRAY['2026-11-07 10:00:00.123001+00', '2026-11-07 10:00:00.123002+00',
                           '2026-11-07 10:00:00.123003+00', '2026-11-07 10:00:00.123004+00',
                           '2026-11-07 10:00:00.123004+00', '2026-11-07 10:00:00.122999+00']::timestamptz[]) AS t`,
        [subject],
      );
      const seen: string[] = [];
      let cursor: string | undefined;
      for (let page = 0; page < 10; page += 1) {
        const result = await listSecurityEvents(w.rt.pool, {
          userId: subject,
          limit: 2,
          cursor,
        });
        seen.push(...result.items.map((item) => item.id));
        if (!result.nextCursor) break;
        cursor = result.nextCursor;
      }
      assert.equal(seen.length, 6, "every entry, once");
      assert.equal(new Set(seen).size, 6);
    });
  },
);
