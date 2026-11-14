import { createHash, randomBytes } from "node:crypto";

import type { Pool, PoolClient } from "pg";

import { FIRST_USER_ID, SETUP_CODE_TTL_HOURS } from "./constants";

/**
 * Life OS's own tables in auth_db, next to Better Auth's
 * (apps/dashboard/db/migrations): server settings and setup codes. Plain SQL
 * through the same pool; the CLI (scripts/lifeos-auth.mjs) runs the same
 * statements from the host's ./setup.sh.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

export function sha256Hex(value: string) {
  return createHash("sha256").update(value).digest("hex");
}

/** A setup code a person types: 4 groups of 4, without look-alike letters. */
export function newSetupCode(): string {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = randomBytes(16);
  let code = "";
  for (let i = 0; i < 16; i += 1) {
    if (i > 0 && i % 4 === 0) code += "-";
    code += alphabet[bytes[i] % alphabet.length];
  }
  return code;
}

/** How a typed code is compared: case and dashes do not matter. */
export function normalizeSetupCode(code: string) {
  return code.toUpperCase().replace(/[^A-Z0-9]/g, "");
}

export async function accountCount(db: Queryable): Promise<number> {
  const { rows } = await db.query<{ n: number }>(
    'SELECT count(*)::int AS n FROM "user"',
  );
  return rows[0]?.n ?? 0;
}

export async function firstAccountExists(db: Queryable): Promise<boolean> {
  const { rowCount } = await db.query('SELECT 1 FROM "user" WHERE id = $1', [
    FIRST_USER_ID,
  ]);
  return (rowCount ?? 0) > 0;
}

/**
 * Stores a new setup code (its SHA-256, valid 24 hours) and returns it; any
 * older unused code stops working. Only while no account exists.
 */
export async function createSetupCode(db: Queryable): Promise<string | null> {
  if ((await accountCount(db)) > 0) return null;
  const code = newSetupCode();
  await db.query("DELETE FROM setup_codes WHERE used_at IS NULL");
  await db.query(
    `INSERT INTO setup_codes (code_hash, expires_at)
     VALUES ($1, now() + make_interval(hours => $2))`,
    [sha256Hex(normalizeSetupCode(code)), SETUP_CODE_TTL_HOURS],
  );
  return code;
}

/** Whether a code is one that is unused and unexpired. */
export async function setupCodeValid(
  db: Queryable,
  code: string,
): Promise<boolean> {
  const normalized = normalizeSetupCode(code);
  if (normalized.length < 8) return false;
  const { rowCount } = await db.query(
    `SELECT 1 FROM setup_codes
     WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()`,
    [sha256Hex(normalized)],
  );
  return (rowCount ?? 0) > 0;
}

/** Marks the code used, once; false when it was not valid any more. */
export async function consumeSetupCode(
  db: Queryable,
  code: string,
  userId: string,
): Promise<boolean> {
  const { rowCount } = await db.query(
    `UPDATE setup_codes SET used_at = now(), used_by = $2
     WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()`,
    [sha256Hex(normalizeSetupCode(code)), userId],
  );
  return (rowCount ?? 0) > 0;
}

export const LEGACY_ACCESS = "legacy_access";

/**
 * Legacy access: on or off. The first read stores the default
 * (LIFE_OS_LEGACY_ACCESS: on after upgrading an install, off on a new one);
 * from then on the stored value counts.
 */
export async function legacyAccess(
  db: Queryable,
  fallback: boolean,
): Promise<boolean> {
  const read = async () =>
    (
      await db.query<{ value: boolean }>(
        "SELECT (value)::boolean AS value FROM server_settings WHERE key = $1",
        [LEGACY_ACCESS],
      )
    ).rows;
  let rows = await read();
  if (rows.length === 0) {
    await db.query(
      `INSERT INTO server_settings (key, value) VALUES ($1, to_jsonb($2::boolean))
       ON CONFLICT (key) DO NOTHING`,
      [LEGACY_ACCESS, fallback],
    );
    rows = await read();
  }
  return rows[0]?.value === true;
}

/**
 * Turns legacy access on or off. Turning it on is refused while more than
 * one account exists (docs/architecture/multi-user.md#legacy-access).
 */
export async function setLegacyAccess(
  db: Queryable,
  on: boolean,
  by: string | null,
): Promise<"ok" | "accounts"> {
  if (on && (await accountCount(db)) > 1) return "accounts";
  await db.query(
    `INSERT INTO server_settings (key, value, updated_at, updated_by)
     VALUES ($1, to_jsonb($2::boolean), now(), $3)
     ON CONFLICT (key) DO UPDATE
       SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by`,
    [LEGACY_ACCESS, on, by],
  );
  return "ok";
}
