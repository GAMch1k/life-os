import type { Pool, PoolClient } from "pg";

import { FIRST_USER_ID } from "./constants";
import { describeUserAgent } from "./devices";

/**
 * The security log (docs/architecture/multi-user.md#logs): sign-ins, failed
 * sign-ins, changes to an account's sign-in, admin actions, legacy access,
 * and the host's commands, in auth_db's security_events. Each entry says
 * what happened, who did it, whom it concerned, from which address and kind
 * of device, and how it ended; never any content. Admins read all of it;
 * everyone reads the entries that concern them. Kept for one year.
 *
 * The host's commands (scripts/lifeos-auth.mjs) write the same rows.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

/** What can happen, with its label. The database checks only the form. */
export const SECURITY_EVENT_KINDS = {
  sign_in: "Signed in",
  sign_in_failed: "Sign-in failed",
  sign_out: "Signed out",
  sessions_revoked: "Signed out elsewhere",
  password_changed: "Password changed",
  password_reset: "Password set with a reset link",
  two_factor_enabled: "Two-factor sign-in turned on",
  two_factor_disabled: "Two-factor sign-in turned off",
  recovery_codes_created: "New recovery codes made",
  passkey_added: "Passkey added",
  passkey_removed: "Passkey removed",
  account_created: "Account created with a setup code",
  setup_code_created: "Setup code made on the server",
  setup_code_refused: "Setup code refused",
  legacy_access_used: "Used without signing in (legacy access)",
  legacy_access_changed: "Legacy access changed",
  owner_recovered: "Reset link for the owner made on the server",
  signed_out_everyone: "Everyone signed out on the server",
  keys_rotated: "Signing key rotated",
  role_changed: "Role changed",
  user_banned: "Account disabled",
  user_unbanned: "Account enabled again",
  user_deleted: "Account deleted",
  user_sessions_revoked: "Account signed out by an admin",
  backup_started: "Backup started",
  admin_refused: "Admin action refused",
} as const;

export type SecurityEventKind = keyof typeof SECURITY_EVENT_KINDS;

export const SECURITY_KINDS = Object.keys(
  SECURITY_EVENT_KINDS,
) as SecurityEventKind[];

export function isSecurityKind(value: unknown): value is SecurityEventKind {
  return typeof value === "string" && value in SECURITY_EVENT_KINDS;
}

export type SecurityOutcome = "ok" | "failed" | "refused";
export type SecurityVia =
  "session" | "legacy" | "host" | "device" | "token" | "mcp" | "delegation";

export type SecurityEventInput = {
  kind: SecurityEventKind;
  outcome?: SecurityOutcome;
  actorUserId?: string | null;
  subjectUserId?: string | null;
  via?: SecurityVia | null;
  /** The request: its address (X-Real-IP) and device (User-Agent). */
  headers?: Headers;
  /** A code: the way in, the new role, a reason. Lowercased and checked. */
  detail?: string | null;
  count?: number;
  at?: Date;
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const ADDRESS = /^[0-9A-Fa-f:.]{1,64}$/;
const DEVICE = /^[A-Za-z][A-Za-z ]{0,47}$/;
const DETAIL = /^[a-z0-9][a-z0-9_.:-]{0,63}$/;

function uuidOrNull(value: string | null | undefined) {
  return value && UUID.test(value) ? value.toLowerCase() : null;
}

/** The client's address as the gateway passed it on, if it is one. */
export function clientAddress(headers: Headers | undefined): string | null {
  const value = (
    headers?.get("x-real-ip") ??
    headers?.get("x-forwarded-for") ??
    ""
  )
    .split(",")[0]
    .trim();
  return ADDRESS.test(value) ? value : null;
}

/** "Chrome on Android": a label from a fixed set, never the User-Agent. */
export function deviceLabel(headers: Headers | undefined): string | null {
  const ua = headers?.get("user-agent");
  if (!ua) return null;
  const label = describeUserAgent(ua);
  return DEVICE.test(label) ? label : null;
}

/** A detail as the log keeps it: a lowercase code, or nothing. */
export function detailCode(value: string | null | undefined): string | null {
  if (!value) return null;
  const code = value
    .toLowerCase()
    .replace(/[^a-z0-9_.:-]+/g, "_")
    .slice(0, 64);
  return DETAIL.test(code) ? code : null;
}

/**
 * Records one entry. It never throws: a sign-in must not fail because its
 * entry could not be written (the error is logged, without content).
 */
export async function recordSecurityEvent(
  db: Queryable,
  event: SecurityEventInput,
): Promise<void> {
  const at = event.at ?? new Date();
  try {
    await db.query(
      `INSERT INTO security_events
         (at, last_at, kind, outcome, actor_user_id, subject_user_id, via, address, device, detail, count)
       VALUES ($1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
      [
        at,
        event.kind,
        event.outcome ?? "ok",
        uuidOrNull(event.actorUserId),
        uuidOrNull(event.subjectUserId),
        event.via ?? null,
        clientAddress(event.headers),
        deviceLabel(event.headers),
        detailCode(event.detail),
        Math.max(1, Math.floor(event.count ?? 1)),
      ],
    );
  } catch (error) {
    console.error(
      "security log: an entry was not written",
      event.kind,
      error instanceof Error ? error.message : error,
    );
  }
}

/** Entries are kept for one year. */
export const SECURITY_LOG_RETENTION_DAYS = 365;

/** Deletes entries older than a year; returns how many. */
export async function pruneSecurityLog(db: Queryable): Promise<number> {
  const { rowCount } = await db.query(
    `DELETE FROM security_events
     WHERE last_at < now() - make_interval(days => $1)`,
    [SECURITY_LOG_RETENTION_DAYS],
  );
  return rowCount ?? 0;
}

/**
 * Legacy access is recorded without flooding the log: the requests of one
 * address and kind of device are counted in memory and kept as one entry a
 * day (UTC), whose count and last time grow. The first request of a new
 * address, device, or day is written at once; the counts follow with
 * flush(), which the Dashboard runs every minute.
 */
/** At most this many legacy access entries a day (addresses and devices). */
export const LEGACY_KEYS_PER_DAY = 200;

export class LegacyUseLog {
  private pending = new Map<
    string,
    {
      address: string | null;
      device: string | null;
      count: number;
      first: Date;
      last: Date;
    }
  >();
  private known = new Set<string>();
  private day = "";
  private chain: Promise<void> = Promise.resolve();

  constructor(private readonly db: Queryable) {}

  /** Counts one request made under legacy access. */
  note(headers: Headers, now = new Date()) {
    const day = now.toISOString().slice(0, 10);
    if (day !== this.day) {
      // A new day: forget the other days' keys, so the set stays small.
      this.day = day;
      for (const old of this.known) {
        if (!old.startsWith(`${day}|`)) this.known.delete(old);
      }
    }
    let address = clientAddress(headers);
    let device = deviceLabel(headers);
    // A flood from many addresses stays bounded: past the cap, a day's new
    // addresses and devices count together, without either.
    if (this.known.size >= LEGACY_KEYS_PER_DAY) {
      const fresh = `${day}|${address ?? ""}|${device ?? ""}`;
      if (!this.known.has(fresh)) address = device = null;
    }
    const key = `${day}|${address ?? ""}|${device ?? ""}`;
    const entry = this.pending.get(key);
    if (entry) {
      entry.count += 1;
      entry.last = now;
    } else {
      this.pending.set(key, {
        address,
        device,
        count: 1,
        first: now,
        last: now,
      });
    }
    if (!this.known.has(key)) {
      this.known.add(key);
      void this.flush();
    }
  }

  /** Writes what was counted. Flushes run one after another. */
  flush(): Promise<void> {
    this.chain = this.chain.then(() => this.write()).catch(() => undefined);
    return this.chain;
  }

  private async write() {
    const entries = [...this.pending.entries()];
    this.pending.clear();
    for (const [key, entry] of entries) {
      const day = key.slice(0, 10);
      try {
        const { rowCount } = await this.db.query(
          `UPDATE security_events
              SET count = count + $4, last_at = greatest(last_at, $5)
            WHERE id = (
              SELECT id FROM security_events
               WHERE kind = 'legacy_access_used'
                 AND at >= ($1::date)::timestamp AT TIME ZONE 'UTC'
                 AND at < ($1::date + 1)::timestamp AT TIME ZONE 'UTC'
                 AND address IS NOT DISTINCT FROM $2
                 AND device IS NOT DISTINCT FROM $3
               ORDER BY at DESC LIMIT 1)`,
          [day, entry.address, entry.device, entry.count, entry.last],
        );
        if (!rowCount) {
          await this.db.query(
            `INSERT INTO security_events
               (at, last_at, kind, outcome, subject_user_id, via, address, device, count)
             VALUES ($1, $2, 'legacy_access_used', 'ok', $3, 'legacy', $4, $5, $6)`,
            [
              entry.first,
              entry.last,
              FIRST_USER_ID,
              entry.address,
              entry.device,
              entry.count,
            ],
          );
        }
      } catch (error) {
        console.error(
          "security log: legacy access use was not written",
          error instanceof Error ? error.message : error,
        );
      }
    }
  }
}

/**
 * At most this many failed sign-in entries an address gets a day, one per
 * account and code; past it, its further failures count in one more entry
 * per account (or none) without the code, so a failure against an account
 * always names it. An address so gets at most this many entries, plus one
 * per account on the server, plus one, a day.
 */
export const FAILED_SIGN_IN_ENTRIES_PER_ADDRESS = 20;

/** The detail of an account's entry past the cap. */
export const FAILED_SIGN_IN_OVERFLOW = "other:too_many_entries";

const failedSignInChains = new WeakMap<object, Promise<void>>();

/**
 * Records a failed sign-in without flooding the log: failures are grouped
 * per day (UTC), address, account (or none, when the name typed is none),
 * and code (the way in and the reason), as one entry whose count and last
 * time grow; past FAILED_SIGN_IN_ENTRIES_PER_ADDRESS entries an address's
 * failures are grouped per account alone. An entry keeps the device of the
 * first failure it counts. Writes run one after another, so two failures
 * at once never make two entries. It never throws.
 */
export function recordFailedSignIn(
  db: Queryable,
  entry: {
    subjectUserId?: string | null;
    headers?: Headers;
    detail?: string | null;
    at?: Date;
  },
): Promise<void> {
  const previous = failedSignInChains.get(db) ?? Promise.resolve();
  const next = previous
    .then(() => writeFailedSignIn(db, entry))
    .catch((error) => {
      console.error(
        "security log: a failed sign-in was not written",
        error instanceof Error ? error.message : error,
      );
    });
  failedSignInChains.set(db, next);
  return next;
}

async function writeFailedSignIn(
  db: Queryable,
  entry: {
    subjectUserId?: string | null;
    headers?: Headers;
    detail?: string | null;
    at?: Date;
  },
) {
  const at = entry.at ?? new Date();
  const day = at.toISOString().slice(0, 10);
  const address = clientAddress(entry.headers);
  const device = deviceLabel(entry.headers);
  const sameDay = `kind = 'sign_in_failed'
    AND at >= ($1::date)::timestamp AT TIME ZONE 'UTC'
    AND at < ($1::date + 1)::timestamp AT TIME ZONE 'UTC'
    AND address IS NOT DISTINCT FROM $2`;
  const bump = async (subject: string | null, detail: string | null) => {
    const { rowCount } = await db.query(
      `UPDATE security_events SET count = count + 1, last_at = greatest(last_at, $5)
        WHERE id = (
          SELECT id FROM security_events
           WHERE ${sameDay}
             AND subject_user_id IS NOT DISTINCT FROM $3
             AND detail IS NOT DISTINCT FROM $4
           ORDER BY at DESC LIMIT 1)`,
      [day, address, subject, detail, at],
    );
    return (rowCount ?? 0) > 0;
  };
  const subject = uuidOrNull(entry.subjectUserId);
  let detail = detailCode(entry.detail);
  if (await bump(subject, detail)) return;
  // The entries per account and code; an account's entry past the cap
  // does not count against it.
  const { rows } = await db.query<{ n: number }>(
    `SELECT count(*)::int AS n FROM security_events
      WHERE ${sameDay} AND detail IS DISTINCT FROM $3`,
    [day, address, FAILED_SIGN_IN_OVERFLOW],
  );
  if ((rows[0]?.n ?? 0) >= FAILED_SIGN_IN_ENTRIES_PER_ADDRESS) {
    // Past the cap: one entry per account, without the code, so a
    // failure against an account still names it.
    detail = FAILED_SIGN_IN_OVERFLOW;
    if (await bump(subject, detail)) return;
  }
  await db.query(
    `INSERT INTO security_events
       (at, last_at, kind, outcome, subject_user_id, address, device, detail, count)
     VALUES ($1, $1, 'sign_in_failed', 'failed', $2, $3, $4, $5, 1)`,
    [at, subject, address, device, detail],
  );
}

export type SecurityEvent = {
  id: string;
  at: string;
  lastAt: string;
  kind: SecurityEventKind | string;
  outcome: SecurityOutcome;
  actor: { id: string; name: string | null } | null;
  subject: { id: string; name: string | null } | null;
  via: SecurityVia | null;
  address: string | null;
  device: string | null;
  detail: string | null;
  count: number;
};

export type SecurityLogQuery = {
  /** Only the entries that concern this user (as actor or subject). */
  userId?: string;
  /**
   * Who reads, in their own log (Settings → Security log): an entry
   * someone else did to their account (an admin's action) shows no address
   * or device; those stay in the admins' view. Entries they did, and those
   * of someone unknown (failed sign-ins, legacy access), show them.
   */
  viewer?: string;
  kind?: SecurityEventKind;
  /** At or after (inclusive). */
  from?: Date;
  /** Before (exclusive). */
  to?: Date;
  cursor?: string;
  limit?: number;
};

export type SecurityLogPage = {
  items: SecurityEvent[];
  nextCursor: string | null;
};

/**
 * A page's position: the last entry's time in microseconds since 1970 (as
 * PostgreSQL keeps it; a JavaScript Date has milliseconds only) and its id.
 */
function encodeCursor(atMicros: string, id: string) {
  return Buffer.from(`${atMicros}|${id}`).toString("base64url");
}

function decodeCursor(cursor: string | undefined) {
  if (!cursor) return null;
  const [at, id] = Buffer.from(cursor, "base64url").toString("utf8").split("|");
  return /^\d{1,17}$/.test(at ?? "") && UUID.test(id ?? "")
    ? { atMicros: at, id }
    : null;
}

/** A page of entries, newest first, with the accounts' names. */
export async function listSecurityEvents(
  db: Queryable,
  query: SecurityLogQuery = {},
): Promise<SecurityLogPage> {
  const limit = Math.min(Math.max(query.limit ?? 50, 1), 200);
  const cursor = decodeCursor(query.cursor);
  const viewer = uuidOrNull(query.viewer);
  const { rows } = await db.query<{
    id: string;
    at: Date;
    at_micros: string;
    last_at: Date;
    kind: string;
    outcome: SecurityOutcome;
    actor_user_id: string | null;
    actor_name: string | null;
    subject_user_id: string | null;
    subject_name: string | null;
    via: SecurityVia | null;
    address: string | null;
    device: string | null;
    detail: string | null;
    count: number;
  }>(
    `SELECT e.id, e.at, (extract(epoch FROM e.at) * 1000000)::bigint::text AS at_micros,
            e.last_at, e.kind, e.outcome, e.via, e.address, e.device, e.detail, e.count,
            e.actor_user_id, coalesce(a."displayUsername", a.username) AS actor_name,
            e.subject_user_id, coalesce(s."displayUsername", s.username) AS subject_name
       FROM security_events e
       LEFT JOIN "user" a ON a.id = e.actor_user_id
       LEFT JOIN "user" s ON s.id = e.subject_user_id
      WHERE ($1::uuid IS NULL OR e.actor_user_id = $1 OR e.subject_user_id = $1)
        AND ($2::text IS NULL OR e.kind = $2)
        AND ($3::timestamptz IS NULL OR e.at >= $3)
        AND ($4::timestamptz IS NULL OR e.at < $4)
        AND ($5::bigint IS NULL
             OR (e.at, e.id) < (to_timestamp(0) + $5::bigint * interval '1 microsecond', $6::uuid))
      ORDER BY e.at DESC, e.id DESC
      LIMIT $7`,
    [
      uuidOrNull(query.userId),
      query.kind ?? null,
      query.from ?? null,
      query.to ?? null,
      cursor?.atMicros ?? null,
      cursor?.id ?? null,
      limit + 1,
    ],
  );
  const items = rows.slice(0, limit).map((row) => {
    // Someone else acted on the reader's account: where from is theirs.
    const hidden =
      viewer !== null &&
      row.actor_user_id !== null &&
      row.actor_user_id !== viewer;
    return {
      id: row.id,
      at: row.at.toISOString(),
      lastAt: row.last_at.toISOString(),
      kind: row.kind,
      outcome: row.outcome,
      actor: row.actor_user_id
        ? { id: row.actor_user_id, name: row.actor_name }
        : null,
      subject: row.subject_user_id
        ? { id: row.subject_user_id, name: row.subject_name }
        : null,
      via: row.via,
      address: hidden ? null : row.address,
      device: hidden ? null : row.device,
      detail: row.detail,
      count: row.count,
    };
  });
  const last = rows.length > limit ? rows[limit - 1] : null;
  return {
    items,
    nextCursor: last ? encodeCursor(last.at_micros, last.id) : null,
  };
}

/** The accounts, for the admin view's filter. */
export async function securityLogUsers(
  db: Queryable,
): Promise<{ id: string; name: string }[]> {
  const { rows } = await db.query<{ id: string; name: string }>(
    `SELECT id, coalesce("displayUsername", username, name) AS name
       FROM "user" ORDER BY lower(coalesce("displayUsername", username, name))`,
  );
  return rows;
}

/** The signing key's id, as the Dashboard last started with it. */
export const SIGNING_KID_SETTING = "signing_kid";

/**
 * At start: when the signing key is not the one the Dashboard last started
 * with, the key was rotated on the host (./setup.sh rotate-keys): the log
 * says so. The first start only notes the key.
 */
export async function noteSigningKey(db: Queryable, kid: string) {
  const { rows } = await db.query<{ kid: string | null }>(
    "SELECT value #>> '{}' AS kid FROM server_settings WHERE key = $1",
    [SIGNING_KID_SETTING],
  );
  const previous = rows[0]?.kid ?? null;
  if (previous === kid) return false;
  await db.query(
    `INSERT INTO server_settings (key, value, updated_at)
     VALUES ($1, to_jsonb($2::text), now())
     ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
    [SIGNING_KID_SETTING, kid],
  );
  if (previous === null) return false;
  await recordSecurityEvent(db, {
    kind: "keys_rotated",
    via: "host",
    detail: kid,
  });
  return true;
}
