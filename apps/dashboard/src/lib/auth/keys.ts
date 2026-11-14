import { createHash } from "node:crypto";

import type { Pool, PoolClient } from "pg";

import {
  DEVICE_KEY_PREFIX,
  KEY_NAME_MAX,
  KEY_RANDOM_LENGTH,
  type ServerRole,
  TOKEN_PREFIX,
} from "./constants";
import { serverRole } from "./roles";

/**
 * Device keys and personal API tokens
 * (docs/architecture/multi-user.md#devices-tokens-mcp): Better Auth API keys
 * (the api-key plugin's `apikey` table: stored as SHA-256, shown once),
 * told apart by their prefix (lo_dev_, lo_tok_) and their metadata:
 *
 *   {"kind": "phone"}                         a paired phone
 *   {"kind": "watch", "parentId": "<phone>"}  its watch's child key
 *   {"kind": "token", "scopes": [...], "devTool": true}
 *
 * The auth check verifies them here, with one query that also reads the
 * account (a ban counts at once), rather than through the plugin's verify
 * endpoint, which writes the key's row on every request.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

export type KeyKind = "phone" | "watch" | "token";

export type KeyMetadata = {
  kind: KeyKind;
  parentId?: string;
  scopes?: string[];
  devTool?: boolean;
};

/** A key as the check sees it, with its account. */
export type KeyRecord = {
  id: string;
  userId: string;
  name: string;
  /** The key's first characters (its prefix and four more), to recognise it. */
  start: string | null;
  kind: KeyKind;
  parentId: string | null;
  scopes: string[] | null;
  devTool: boolean;
  createdAt: Date;
  expiresAt: Date | null;
  lastUsedAt: Date | null;
  user: {
    role: ServerRole;
    banned: boolean;
    authzVersion: number;
  };
};

/** The api-key plugin's hash of a key (its defaultKeyHasher). */
export function hashKey(key: string) {
  return createHash("sha256").update(key).digest("base64url");
}

const KEY_SHAPE = new RegExp(
  `^(${DEVICE_KEY_PREFIX}|${TOKEN_PREFIX})[A-Za-z]{${KEY_RANDOM_LENGTH}}$`,
);

/** Whether a bearer value is shaped like one of our keys. */
export function isKeyShaped(value: string) {
  return KEY_SHAPE.test(value);
}

function parseMetadata(text: unknown): KeyMetadata | null {
  let value: unknown = text;
  // The plugin stores metadata as JSON text (and once stored it twice
  // encoded); either way an object comes out.
  for (let i = 0; i < 2 && typeof value === "string"; i += 1) {
    try {
      value = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== "object") return null;
  const meta = value as Record<string, unknown>;
  if (meta.kind !== "phone" && meta.kind !== "watch" && meta.kind !== "token") {
    return null;
  }
  return {
    kind: meta.kind,
    parentId: typeof meta.parentId === "string" ? meta.parentId : undefined,
    scopes: Array.isArray(meta.scopes)
      ? meta.scopes.filter((s): s is string => typeof s === "string")
      : undefined,
    devTool: meta.devTool === true,
  };
}

type KeyRow = {
  id: string;
  user_id: string;
  name: string | null;
  start: string | null;
  prefix: string | null;
  metadata: string | null;
  enabled: boolean | null;
  created_at: Date;
  expires_at: Date | null;
  last_request: Date | null;
  role: string | null;
  banned: boolean | null;
  ban_expires: Date | null;
  authz_version: number | null;
};

const KEY_COLUMNS = `k.id, k."referenceId" AS user_id, k.name, k.start, k.prefix,
  k.metadata, k.enabled, k."createdAt" AS created_at,
  k."expiresAt" AS expires_at, k."lastRequest" AS last_request,
  u.role, u.banned, u."banExpires" AS ban_expires,
  u."authzVersion" AS authz_version`;

function toRecord(row: KeyRow): KeyRecord | null {
  const meta = parseMetadata(row.metadata);
  if (!meta) return null;
  const expectedPrefix =
    meta.kind === "token" ? TOKEN_PREFIX : DEVICE_KEY_PREFIX;
  if (row.prefix !== expectedPrefix) return null;
  const banned =
    row.banned === true &&
    (!row.ban_expires || new Date(row.ban_expires).getTime() > Date.now());
  return {
    id: row.id,
    userId: row.user_id,
    name: row.name ?? "",
    start: row.start,
    kind: meta.kind,
    parentId: meta.parentId ?? null,
    scopes: meta.kind === "token" ? (meta.scopes ?? []) : null,
    devTool: meta.kind === "token" && meta.devTool === true,
    createdAt: row.created_at,
    expiresAt: row.expires_at,
    lastUsedAt: row.last_request,
    user: {
      role: serverRole(row.role),
      banned,
      authzVersion: row.authz_version ?? 0,
    },
  };
}

export type KeyCheck =
  | { ok: true; key: KeyRecord }
  | { ok: false; reason: "unknown" | "disabled" | "expired" | "banned" };

/**
 * Looks a presented key up: its hash, its kind, its account. Unknown,
 * turned off, expired, or of a disabled account is refused, each the same
 * way to the caller (401).
 */
export async function checkKey(
  db: Queryable,
  presented: string,
  now = Date.now(),
): Promise<KeyCheck> {
  if (!isKeyShaped(presented)) return { ok: false, reason: "unknown" };
  const { rows } = await db.query<KeyRow>(
    `SELECT ${KEY_COLUMNS}
       FROM apikey k JOIN "user" u ON u.id::text = k."referenceId"
      WHERE k.key = $1`,
    [hashKey(presented)],
  );
  const key = rows[0] ? toRecord(rows[0]) : null;
  if (
    !key ||
    !presented.startsWith(
      key.kind === "token" ? TOKEN_PREFIX : DEVICE_KEY_PREFIX,
    )
  ) {
    return { ok: false, reason: "unknown" };
  }
  if (rows[0].enabled === false) return { ok: false, reason: "disabled" };
  if (key.expiresAt && key.expiresAt.getTime() <= now) {
    return { ok: false, reason: "expired" };
  }
  if (key.user.banned) return { ok: false, reason: "banned" };
  return { ok: true, key };
}

/** An account's keys of some kinds, newest first. */
export async function listKeys(
  db: Queryable,
  userId: string,
  kinds: KeyKind[],
): Promise<KeyRecord[]> {
  const { rows } = await db.query<KeyRow>(
    `SELECT ${KEY_COLUMNS}
       FROM apikey k JOIN "user" u ON u.id::text = k."referenceId"
      WHERE k."referenceId" = $1
      ORDER BY k."createdAt" DESC, k.id`,
    [userId],
  );
  return rows
    .map(toRecord)
    .filter(
      (key): key is KeyRecord => key !== null && kinds.includes(key.kind),
    );
}

/** Control characters and line or paragraph separators. */
const CONTROL = new RegExp("[\\u0000-\\u001f\\u007f-\\u009f\\u2028\\u2029]");

/** A device's or token's name: trimmed, 1 to 64 characters, printable. */
export function keyName(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const name = value.trim().replace(/\s+/g, " ");
  if (!name || [...name].length > KEY_NAME_MAX) return null;
  if (CONTROL.test(name)) return null;
  return name;
}

/**
 * Revokes keys of one account, in one transaction: the keys, a phone's
 * watch key with it, and a note for each device key that its push
 * subscriptions are to be deleted (worked off by processKeyCleanup).
 * Returns the ids revoked.
 */
export async function revokeKeys(
  pool: Pool,
  userId: string,
  ids: string[],
  options: { kinds?: KeyKind[] } = {},
): Promise<{ id: string; kind: KeyKind }[]> {
  if (ids.length === 0) return [];
  const client = await pool.connect();
  try {
    await client.query("BEGIN");
    const { rows } = await client.query<{
      id: string;
      prefix: string;
      metadata: string | null;
    }>(
      `SELECT id, prefix, metadata FROM apikey
        WHERE "referenceId" = $1
          AND (id::text = ANY($2::text[])
               OR (prefix = $3 AND metadata IS NOT NULL
                   AND (metadata::jsonb ->> 'parentId') = ANY($2::text[])))
        FOR UPDATE`,
      [userId, ids, DEVICE_KEY_PREFIX],
    );
    const doomed = rows
      .map((row) => ({ id: row.id, meta: parseMetadata(row.metadata) }))
      .filter(
        (row): row is { id: string; meta: KeyMetadata } =>
          row.meta !== null &&
          (!options.kinds ||
            options.kinds.includes(row.meta.kind) ||
            !ids.includes(row.id)),
      );
    if (doomed.length) {
      const devices = doomed
        .filter((row) => row.meta.kind !== "token")
        .map((row) => row.id);
      if (devices.length) {
        await client.query(
          `INSERT INTO device_key_cleanup (key_id)
           SELECT unnest($1::uuid[]) ON CONFLICT (key_id) DO NOTHING`,
          [devices],
        );
      }
      await client.query("DELETE FROM apikey WHERE id = ANY($1::uuid[])", [
        doomed.map((row) => row.id),
      ]);
    }
    await client.query("COMMIT");
    return doomed.map((row) => ({ id: row.id, kind: row.meta.kind }));
  } catch (error) {
    await client.query("ROLLBACK").catch(() => undefined);
    throw error;
  } finally {
    client.release();
  }
}

export type RevokedCredentials = {
  sessions: number;
  devices: number;
  tokens: number;
  grants: number;
};

/**
 * "Sign out everywhere" (docs/architecture/multi-user.md#sign-everyone-out):
 * in one transaction, an account's (or, with userId null, every account's)
 * sessions, device keys (unless keepDevices), personal tokens, OAuth refresh
 * and access tokens and consents, and unused pairing codes go, and every
 * authz_version moves on. The device keys' push subscriptions follow within
 * a minute (device_key_cleanup). ./setup.sh sign-out-all runs the same
 * statements (scripts/lifeos-auth.mjs).
 */
export async function revokeCredentials(
  pool: Pool,
  userId: string | null,
  options: { keepDevices?: boolean } = {},
): Promise<RevokedCredentials> {
  const client = await pool.connect();
  const who = userId === null ? "true" : `"referenceId" = $1`;
  const whoUser = userId === null ? "true" : `"userId" = $1::uuid`;
  const params = userId === null ? [] : [userId];
  try {
    await client.query("BEGIN");
    let devices = 0;
    if (!options.keepDevices) {
      await client.query(
        `INSERT INTO device_key_cleanup (key_id)
         SELECT id FROM apikey WHERE ${who} AND prefix = '${DEVICE_KEY_PREFIX}'
         ON CONFLICT (key_id) DO NOTHING`,
        params,
      );
      devices =
        (
          await client.query(
            `DELETE FROM apikey WHERE ${who} AND prefix = '${DEVICE_KEY_PREFIX}'`,
            params,
          )
        ).rowCount ?? 0;
    }
    const tokens =
      (
        await client.query(
          `DELETE FROM apikey WHERE ${who} AND prefix IS DISTINCT FROM '${DEVICE_KEY_PREFIX}'`,
          params,
        )
      ).rowCount ?? 0;
    await client.query(
      `DELETE FROM "oauthAccessToken" WHERE ${whoUser}`,
      params,
    );
    await client.query(
      `DELETE FROM "oauthRefreshToken" WHERE ${whoUser}`,
      params,
    );
    const grants =
      (
        await client.query(
          `DELETE FROM "oauthConsent" WHERE ${whoUser}`,
          params,
        )
      ).rowCount ?? 0;
    await client.query(
      `DELETE FROM pairing_codes WHERE ${userId === null ? "true" : "user_id = $1::uuid"}`,
      params,
    );
    const sessions =
      (await client.query(`DELETE FROM session WHERE ${whoUser}`, params))
        .rowCount ?? 0;
    await client.query(
      `UPDATE "user" SET "authzVersion" = "authzVersion" + 1, "updatedAt" = now()
        WHERE ${userId === null ? "true" : "id = $1::uuid"}`,
      params,
    );
    await client.query("COMMIT");
    return { sessions, devices, tokens, grants };
  } catch (error) {
    await client.query("ROLLBACK").catch(() => undefined);
    throw error;
  } finally {
    client.release();
  }
}

/**
 * Notes when keys were last used, at most once a minute per key, so that
 * Settings shows a last use without a write on every request.
 */
export class KeyUseLog {
  private readonly written = new Map<string, number>();

  constructor(
    private readonly db: Queryable,
    private readonly intervalMs = 60_000,
    private readonly maxEntries = 10_000,
  ) {}

  touch(id: string, now = Date.now()) {
    const last = this.written.get(id);
    if (last !== undefined && now - last < this.intervalMs) return;
    if (this.written.size >= this.maxEntries) this.written.clear();
    this.written.set(id, now);
    void this.db
      .query(`UPDATE apikey SET "lastRequest" = $2 WHERE id = $1`, [
        id,
        new Date(now),
      ])
      .catch((error) =>
        console.error(
          "keys: the last use was not noted",
          error instanceof Error ? error.message : error,
        ),
      );
  }
}

/** Expired tokens are listed (as expired) for 30 days, then deleted. */
export async function pruneExpiredKeys(db: Queryable): Promise<number> {
  const { rowCount } = await db.query(
    `DELETE FROM apikey
      WHERE "expiresAt" IS NOT NULL AND "expiresAt" < now() - interval '30 days'`,
  );
  return rowCount ?? 0;
}

/** How long a device key's cleanup is retried before it is given up. */
const CLEANUP_GIVE_UP_DAYS = 30;

/**
 * Works off device_key_cleanup: asks Notifications to delete each revoked
 * device key's push subscriptions (DELETE /v1/push/device-keys/{id}, a
 * route for internal callers only), and forgets the note once it answered
 * 204. Retried every minute; given up after 30 days.
 */
export async function processKeyCleanup(
  db: Queryable,
  deleteSubscriptions: ((keyId: string) => Promise<boolean>) | null,
): Promise<number> {
  await db.query(
    `DELETE FROM device_key_cleanup
      WHERE revoked_at < now() - make_interval(days => $1)`,
    [CLEANUP_GIVE_UP_DAYS],
  );
  if (!deleteSubscriptions) return 0;
  const { rows } = await db.query<{ key_id: string }>(
    `SELECT key_id FROM device_key_cleanup
      WHERE last_attempt_at IS NULL OR last_attempt_at < now() - interval '30 seconds'
      ORDER BY revoked_at LIMIT 50`,
  );
  let done = 0;
  for (const { key_id: keyId } of rows) {
    let ok = false;
    try {
      ok = await deleteSubscriptions(keyId);
    } catch {
      ok = false;
    }
    if (ok) {
      await db.query("DELETE FROM device_key_cleanup WHERE key_id = $1", [
        keyId,
      ]);
      done += 1;
    } else {
      await db.query(
        `UPDATE device_key_cleanup
            SET attempts = attempts + 1, last_attempt_at = now()
          WHERE key_id = $1`,
        [keyId],
      );
    }
  }
  return done;
}

/** Notifications' internal route for a revoked device key's subscriptions. */
export function notificationsCleanup(
  baseUrl: string | undefined,
  fetcher: typeof fetch = fetch,
): ((keyId: string) => Promise<boolean>) | null {
  if (!baseUrl) return null;
  const base = baseUrl.replace(/\/+$/, "");
  return async (keyId) => {
    const response = await fetcher(
      `${base}/v1/push/device-keys/${encodeURIComponent(keyId)}`,
      {
        method: "DELETE",
        headers: { "x-life-os-actor": "system:dashboard" },
        signal: AbortSignal.timeout(5000),
      },
    );
    // The route answers 204 with or without subscriptions; anything else
    // (a Notifications from before P21-06 answers 404) is tried again.
    return response.status === 204;
  };
}
