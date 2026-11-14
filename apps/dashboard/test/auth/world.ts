import assert from "node:assert/strict";

import { toRuntimeStatus } from "../../src/lib/runtime-status";
import { createSetupCode } from "../../src/lib/auth/store";
import { totp } from "./authenticators";
import {
  type AppOptions,
  app,
  Browser,
  freshDatabase,
  runtime,
  type RuntimeOptions,
  testKeys,
} from "./harness";

/**
 * A world for the admin surfaces and the security log
 * (admin-and-logs.test.ts): a database, the auth runtime, the Dashboard's
 * routes with a made-up Status and backup service, and the owner's
 * account, signed in.
 */

export const password = "correct horse battery staple 42";

/** A Status with a problem in Calendar and the server's own checks. */
export function fakeStatus() {
  return toRuntimeStatus(
    {
      inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
      services: [
        { name: "Calendar", ready: false },
        { name: "Tasks", ready: true },
        { name: "Host health", ready: false },
        { name: "Backup", ready: true },
      ],
    },
    new Date("2026-11-14T12:00:00Z"),
    "Europe/Berlin",
  );
}

export type BackupCall = {
  method: string;
  path: string;
  identity: string | null;
};

export async function world(
  admin: string,
  options: RuntimeOptions & { owner?: boolean } = {},
) {
  const db = await freshDatabase(admin);
  const keys = options.keys ?? testKeys();
  const rt = runtime(db.url, { ...options, keys });
  const backups: BackupCall[] = [];
  const routes: AppOptions = {
    status: async () => fakeStatus(),
    backup: async (request, path) => {
      backups.push({
        method: request.method,
        path,
        identity: request.headers.get("x-life-os-identity"),
      });
      return Response.json({ id: "20261114T120000Z" }, { status: 202 });
    },
  };
  const fetcher = app(rt, routes);
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
    backups,
    browser: (origin?: string, address?: string) =>
      new Browser(fetcher, origin, address),
    async close() {
      await rt.pool.end();
      await db.drop();
    },
  };
}

export type World = Awaited<ReturnType<typeof world>>;

export async function signIn(b: Browser, username: string, pw = password) {
  return b.req("/api/auth/sign-in/username", {
    method: "POST",
    body: { username, password: pw },
  });
}

/** Turns on TOTP for a signed-in browser; returns the otpauth URI and codes. */
export async function enableTotp(b: Browser, pw = password) {
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

/** Every entry of the security log, oldest first, as rows. */
export async function securityRows(w: World) {
  const { rows } = await w.rt.pool.query(
    "SELECT * FROM security_events ORDER BY at, id",
  );
  return rows as {
    id: string;
    at: Date;
    last_at: Date;
    kind: string;
    outcome: string;
    actor_user_id: string | null;
    subject_user_id: string | null;
    via: string | null;
    address: string | null;
    device: string | null;
    detail: string | null;
    count: number;
  }[];
}
