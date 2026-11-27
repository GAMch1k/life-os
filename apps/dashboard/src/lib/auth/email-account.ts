import { createHash, randomBytes } from "node:crypto";

import type { Pool, PoolClient } from "pg";

import { PLACEHOLDER_EMAIL_DOMAIN } from "./constants";
import { ALERT_KINDS, type AlertKind } from "./email-kinds";

/**
 * An account's email (docs/install/email.md): its address (Better Auth's
 * "user".email; an account without one has a placeholder under .invalid),
 * whether it is verified ("emailVerified"), the one-time links Life OS
 * mails, sign-in codes by email as a second factor, and the person's
 * switches for the emails they receive unasked.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

/** A verification link works this long, once. */
export const VERIFY_LINK_HOURS = 24;
/** An emailed reset link works this long, once (recover-owner's: 24 h). */
export const RESET_LINK_MINUTES = 30;
/** A sign-in code works this long, for this many tries. */
export const SIGN_IN_CODE_MINUTES = 10;
export const SIGN_IN_CODE_ATTEMPTS = 5;

const VERIFY_PREFIX = "lifeos-verify-email:";

export function isPlaceholderEmail(address: string | null | undefined) {
  return (
    !address || address.toLowerCase().endsWith(`@${PLACEHOLDER_EMAIL_DOMAIN}`)
  );
}

/**
 * One bare address, lowercase, as the notifications service accepts it:
 * no name, no list, nothing that could be a header; null otherwise.
 */
export function normalizeAddress(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const address = value.trim().toLowerCase();
  if (address.length < 3 || address.length > 254) return null;
  if (!/^[^\s@<>(),;:"\\[\]]+@[^\s@<>(),;:"\\[\]]+$/.test(address)) return null;
  const [local, domain] = address.split("@");
  if (
    !local ||
    local.length > 64 ||
    !domain ||
    domain.startsWith(".") ||
    domain.endsWith(".") ||
    domain.includes("..")
  ) {
    return null;
  }
  if (domain === PLACEHOLDER_EMAIL_DOMAIN || domain.endsWith(".invalid"))
    return null;
  return address;
}

export type AccountEmail = {
  userId: string;
  username: string | null;
  name: string;
  /** null: no address (the placeholder). */
  address: string | null;
  verified: boolean;
};

export async function accountEmail(
  db: Queryable,
  userId: string,
): Promise<AccountEmail | null> {
  const { rows } = await db.query<{
    id: string;
    username: string | null;
    name: string;
    email: string;
    verified: boolean | null;
  }>(
    `SELECT id, coalesce("displayUsername", username) AS username, name, email,
            "emailVerified" AS verified
       FROM "user" WHERE id = $1`,
    [userId],
  );
  const row = rows[0];
  if (!row) return null;
  const placeholder = isPlaceholderEmail(row.email);
  return {
    userId: row.id,
    username: row.username,
    name: row.name || row.username || "",
    address: placeholder ? null : row.email,
    verified: !placeholder && row.verified === true,
  };
}

/** The account a typed username or address names, if any. */
export async function accountByLogin(
  db: Queryable,
  login: string,
): Promise<AccountEmail | null> {
  const value = login.trim().toLowerCase();
  if (!value || value.length > 254) return null;
  const { rows } = await db.query<{ id: string }>(
    value.includes("@")
      ? 'SELECT id FROM "user" WHERE lower(email) = $1'
      : 'SELECT id FROM "user" WHERE username = $1',
    [value],
  );
  return rows[0] ? accountEmail(db, rows[0].id) : null;
}

/**
 * Sets (or with null, removes) an account's address; a new one is not
 * verified. "taken" when another account has it.
 */
export async function setAccountEmail(
  db: Queryable,
  userId: string,
  address: string | null,
): Promise<"ok" | "same" | "taken"> {
  const current = await accountEmail(db, userId);
  if (current && current.address === address) return "same";
  const value =
    address ??
    `u-${randomBytes(9).toString("hex")}@${PLACEHOLDER_EMAIL_DOMAIN}`;
  try {
    await db.query(
      `UPDATE "user" SET email = $2, "emailVerified" = false, "updatedAt" = now()
        WHERE id = $1`,
      [userId, value],
    );
  } catch (error) {
    if ((error as { code?: string }).code === "23505") return "taken";
    throw error;
  }
  return "ok";
}

/** How Better Auth keeps a verification identifier (storeIdentifier "hashed"). */
export function hashedIdentifier(identifier: string) {
  return createHash("sha256").update(identifier).digest("base64url");
}

function newToken() {
  return randomBytes(24).toString("base64url");
}

/** A one-time link that verifies this address for this account. */
export async function createVerificationLink(
  db: Queryable,
  account: { userId: string; address: string },
  publicUrl: string,
): Promise<string> {
  const token = newToken();
  await db.query(
    `INSERT INTO verification (identifier, value, "expiresAt")
     VALUES ($1, $2, now() + make_interval(hours => $3))`,
    [
      hashedIdentifier(`${VERIFY_PREFIX}${token}`),
      JSON.stringify({ u: account.userId, e: account.address }),
      VERIFY_LINK_HOURS,
    ],
  );
  return `${publicUrl.replace(/\/+$/, "")}/verify-email/${token}`;
}

/**
 * Uses a verification link: the account's address is verified when it is
 * still the one the link was made for. Once, within its time.
 */
export async function consumeVerificationLink(
  db: Queryable,
  token: unknown,
): Promise<{ userId: string } | null> {
  if (typeof token !== "string" || !/^[A-Za-z0-9_-]{20,64}$/.test(token)) {
    return null;
  }
  const { rows } = await db.query<{ value: string }>(
    `DELETE FROM verification WHERE identifier = $1 AND "expiresAt" > now()
     RETURNING value`,
    [hashedIdentifier(`${VERIFY_PREFIX}${token}`)],
  );
  if (!rows[0]) return null;
  let link: { u?: unknown; e?: unknown };
  try {
    link = JSON.parse(rows[0].value) as typeof link;
  } catch {
    return null;
  }
  if (typeof link.u !== "string" || typeof link.e !== "string") return null;
  const { rows: updated } = await db.query<{ id: string }>(
    `UPDATE "user" SET "emailVerified" = true, "updatedAt" = now()
      WHERE id = $1 AND email = $2 RETURNING id`,
    [link.u, link.e],
  );
  return updated[0] ? { userId: updated[0].id } : null;
}

/**
 * A one-time link to set the account's password: Better Auth's reset token,
 * stored as it stores it (as ./setup.sh recover-owner makes one,
 * scripts/lifeos-auth.mjs recoverOwner), for RESET_LINK_MINUTES. The reset
 * page (/reset/<token>) and Better Auth's /reset-password use it, and
 * setting the password signs out every session, device key, token, and
 * connected app of the account (security-hooks.ts).
 */
export async function createResetLink(
  db: Queryable,
  userId: string,
  publicUrl: string,
): Promise<string> {
  const token = newToken();
  await db.query(
    `INSERT INTO verification (identifier, value, "expiresAt")
     VALUES ($1, $2, now() + make_interval(mins => $3))`,
    [hashedIdentifier(`reset-password:${token}`), userId, RESET_LINK_MINUTES],
  );
  return `${publicUrl.replace(/\/+$/, "")}/reset/${token}`;
}

// ---- Sign-in codes by email -------------------------------------------------

/** Whether the account turned sign-in codes by email on. */
export async function signInCodesOn(db: Queryable, userId: string) {
  const { rows } = await db.query(
    "SELECT 1 FROM email_sign_in_codes WHERE user_id = $1",
    [userId],
  );
  return rows.length > 0;
}

/** A verified authenticator app (Better Auth's TOTP). */
export async function hasAuthenticator(db: Queryable, userId: string) {
  const { rows } = await db.query<{ ok: boolean }>(
    `SELECT coalesce((SELECT "twoFactorEnabled" FROM "user" WHERE id = $1), false)
        AND EXISTS (SELECT 1 FROM "twoFactor" WHERE "userId" = $1 AND verified IS NOT FALSE) AS ok`,
    [userId],
  );
  return rows[0]?.ok === true;
}

/**
 * Better Auth asks for a second factor after the password while
 * "twoFactorEnabled" is on: an authenticator app, or sign-in codes by
 * email. After either changes, it follows: on while one of them is.
 */
export async function syncTwoFactorEnabled(db: Queryable, userId: string) {
  await db.query(
    `UPDATE "user" SET "twoFactorEnabled" =
        EXISTS (SELECT 1 FROM "twoFactor" WHERE "userId" = $1 AND verified IS NOT FALSE)
        OR EXISTS (SELECT 1 FROM email_sign_in_codes WHERE user_id = $1),
       "updatedAt" = now()
      WHERE id = $1`,
    [userId],
  );
}

export async function setSignInCodes(
  db: Pool,
  userId: string,
  on: boolean,
): Promise<boolean> {
  const client = await db.connect();
  try {
    await client.query("BEGIN");
    const { rowCount } = on
      ? await client.query(
          `INSERT INTO email_sign_in_codes (user_id) VALUES ($1)
           ON CONFLICT (user_id) DO NOTHING`,
          [userId],
        )
      : await client.query(
          "DELETE FROM email_sign_in_codes WHERE user_id = $1",
          [userId],
        );
    await syncTwoFactorEnabled(client, userId);
    await client.query("COMMIT");
    return (rowCount ?? 0) > 0;
  } catch (error) {
    await client.query("ROLLBACK").catch(() => undefined);
    throw error;
  } finally {
    client.release();
  }
}

// ---- The person's switches ---------------------------------------------------

export type EmailPreferences = Record<AlertKind, boolean>;

export async function emailPreferences(
  db: Queryable,
  userId: string,
): Promise<EmailPreferences> {
  const { rows } = await db.query<{ kind: string; enabled: boolean }>(
    "SELECT kind, enabled FROM email_preferences WHERE user_id = $1",
    [userId],
  );
  const result = Object.fromEntries(
    ALERT_KINDS.map((kind) => [kind, true]),
  ) as EmailPreferences;
  for (const row of rows) {
    if ((ALERT_KINDS as readonly string[]).includes(row.kind)) {
      result[row.kind as AlertKind] = row.enabled;
    }
  }
  return result;
}

export async function setEmailPreferences(
  db: Queryable,
  userId: string,
  changes: Partial<EmailPreferences>,
) {
  for (const kind of ALERT_KINDS) {
    const value = changes[kind];
    if (typeof value !== "boolean") continue;
    await db.query(
      `INSERT INTO email_preferences (user_id, kind, enabled, updated_at)
       VALUES ($1, $2, $3, now())
       ON CONFLICT (user_id, kind) DO UPDATE
         SET enabled = EXCLUDED.enabled, updated_at = now()`,
      [userId, kind, value],
    );
  }
}

// ---- Limits ----------------------------------------------------------------

export type Window = { max: number; ms: number };

/**
 * Every email a stranger could make Life OS send is limited, per address,
 * per network address, and per account, in memory (a restart forgets):
 * take(key, windows) is 0 when it may go, else the milliseconds to wait.
 */
export class EmailLimiter {
  private readonly sent = new Map<string, number[]>();

  constructor(private readonly maxKeys = 20_000) {}

  take(key: string, windows: Window[], now = Date.now()): number {
    const longest = Math.max(...windows.map((w) => w.ms));
    const times = (this.sent.get(key) ?? []).filter((at) => now - at < longest);
    let wait = 0;
    for (const window of windows) {
      const inside = times.filter((at) => now - at < window.ms);
      if (inside.length >= window.max) {
        wait = Math.max(
          wait,
          inside[inside.length - window.max] + window.ms - now,
        );
      }
    }
    if (wait > 0) {
      this.sent.set(key, times);
      return wait;
    }
    if (this.sent.size >= this.maxKeys && !this.sent.has(key)) {
      for (const [old, list] of this.sent) {
        if (!list.some((at) => now - at < longest)) this.sent.delete(old);
      }
      if (this.sent.size >= this.maxKeys) this.sent.clear();
    }
    times.push(now);
    this.sent.set(key, times);
    return 0;
  }

  clear() {
    this.sent.clear();
  }
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

/** The limits (docs/install/email.md#limits). */
export const LIMITS = {
  /** A reset link: per address it goes to, and per network address. */
  resetPerAddress: [{ max: 3, ms: HOUR }],
  resetPerNetwork: [{ max: 10, ms: HOUR }],
  /** A sign-in code: per account. */
  signInCode: [
    { max: 1, ms: 30_000 },
    { max: 5, ms: 15 * MINUTE },
  ],
  /** A verification link: per account. */
  verifyLink: [
    { max: 1, ms: 30_000 },
    { max: 5, ms: HOUR },
  ],
  /** A new-device alert: per account. */
  newDevice: [{ max: 10, ms: HOUR }],
} satisfies Record<string, Window[]>;
