import { createHash, randomBytes } from "node:crypto";

import type { Pool, PoolClient } from "pg";

import {
  PAIRING_CODE_TTL_SECONDS,
  PAIRING_CODES_PER_ACCOUNT,
  PAIRING_LINK,
} from "./constants";
import { normalizeSetupCode, sha256Hex } from "./store";

/**
 * Pairing a phone (docs/api/devices.md#pairing): a single-use code, valid 5
 * minutes, stored as its SHA-256 in pairing_codes. The QR code in Settings
 * → Devices and the Custom Tab flow (/login?pair=1) both end in the link
 * lifeos://pair?host=<public URL>&code=<code>, and the app redeems the code
 * at POST /api/auth/devices/pair for its own device key. A code made for
 * the Custom Tab carries the app's challenge, and only its verifier redeems
 * it (as PKCE does for OAuth): an app that catches the lifeos:// link
 * instead cannot use it.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

/** A code a person can type: 4 groups of 4, without look-alike letters. */
export function newPairingCode(): string {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = randomBytes(16);
  let code = "";
  for (let i = 0; i < 16; i += 1) {
    if (i > 0 && i % 4 === 0) code += "-";
    code += alphabet[bytes[i] % alphabet.length];
  }
  return code;
}

const CHALLENGE = /^[A-Za-z0-9_-]{43}$/;
const VERIFIER = /^[A-Za-z0-9._~-]{43,128}$/;
const STATE = /^[A-Za-z0-9_-]{8,128}$/;

export function isChallenge(value: unknown): value is string {
  return typeof value === "string" && CHALLENGE.test(value);
}

export function isVerifier(value: unknown): value is string {
  return typeof value === "string" && VERIFIER.test(value);
}

export function isPairingState(value: unknown): value is string {
  return typeof value === "string" && STATE.test(value);
}

/** BASE64URL(SHA-256(verifier)), as the app computes its challenge. */
export function challengeOf(verifier: string) {
  return createHash("sha256").update(verifier).digest("base64url");
}

/**
 * The deep link the app opens: always lifeos://pair, with the server's
 * public URL, the code or an error, and the app's state. Nothing of it
 * comes from the request but the state, which is checked.
 */
export function pairingLink(
  publicUrl: string,
  result: { code: string } | { error: "access_denied" },
  state?: string,
): string {
  const params = new URLSearchParams({ host: publicUrl });
  if ("code" in result) params.set("code", result.code);
  else params.set("error", result.error);
  if (state && isPairingState(state)) params.set("state", state);
  return `${PAIRING_LINK}?${params.toString()}`;
}

/**
 * A new pairing code for an account: at most PAIRING_CODES_PER_ACCOUNT
 * unused ones at a time (a new one replaces the oldest); used and expired
 * ones are dropped.
 */
export async function createPairingCode(
  db: Queryable,
  userId: string,
  options: { challenge?: string } = {},
): Promise<{ code: string; expiresAt: Date }> {
  await db.query(
    `DELETE FROM pairing_codes
      WHERE user_id = $1 AND (used_at IS NOT NULL OR expires_at <= now())`,
    [userId],
  );
  await db.query(
    `DELETE FROM pairing_codes WHERE code_hash IN (
       SELECT code_hash FROM pairing_codes WHERE user_id = $1
        ORDER BY created_at DESC OFFSET $2)`,
    [userId, PAIRING_CODES_PER_ACCOUNT - 1],
  );
  const code = newPairingCode();
  const { rows } = await db.query<{ expires_at: Date }>(
    `INSERT INTO pairing_codes (code_hash, user_id, challenge, expires_at)
     VALUES ($1, $2, $3, now() + make_interval(secs => $4))
     RETURNING expires_at`,
    [
      sha256Hex(normalizeSetupCode(code)),
      userId,
      options.challenge ?? null,
      PAIRING_CODE_TTL_SECONDS,
    ],
  );
  return { code, expiresAt: rows[0].expires_at };
}

/**
 * Uses up a pairing code, in one UPDATE: unused, unexpired, of an account
 * that is not disabled, and, when it was made with a challenge, presented
 * with its verifier. Of two redemptions racing with one code, one gets the
 * account. null for anything else, without saying which.
 */
export async function claimPairingCode(
  db: Queryable,
  code: string,
  verifier?: string,
): Promise<{ userId: string; codeHash: string } | null> {
  const normalized = normalizeSetupCode(code);
  if (normalized.length !== 16) return null;
  const codeHash = sha256Hex(normalized);
  const challenge =
    verifier && isVerifier(verifier) ? challengeOf(verifier) : null;
  const { rows } = await db.query<{ user_id: string }>(
    `UPDATE pairing_codes p SET used_at = now()
      WHERE p.code_hash = $1 AND p.used_at IS NULL AND p.expires_at > now()
        AND (p.challenge IS NULL OR p.challenge = $2)
        AND NOT EXISTS (
          SELECT 1 FROM "user" u WHERE u.id = p.user_id AND u.banned IS TRUE
             AND (u."banExpires" IS NULL OR u."banExpires" > now()))
      RETURNING p.user_id`,
    [codeHash, challenge],
  );
  return rows[0] ? { userId: rows[0].user_id, codeHash } : null;
}

/** Notes which device key a redeemed code made. */
export async function notePairedKey(
  db: Queryable,
  codeHash: string,
  keyId: string,
) {
  await db.query(`UPDATE pairing_codes SET key_id = $2 WHERE code_hash = $1`, [
    codeHash,
    keyId,
  ]);
}

/**
 * Limits redeeming codes per address (in memory): 5 tries a minute, and
 * after 10 wrong codes within 15 minutes, none for 15 minutes. A code is
 * 80 random bits and lives 5 minutes, so this only keeps guessing (and the
 * security log) quiet.
 */
export class PairingLimiter {
  private readonly tries = new Map<string, number[]>();
  private readonly failures = new Map<string, number[]>();
  private readonly blockedUntil = new Map<string, number>();

  constructor(
    private readonly perMinute = 5,
    private readonly maxFailures = 10,
    private readonly windowMs = 15 * 60_000,
    private readonly maxAddresses = 10_000,
  ) {}

  /** Milliseconds to wait before this address may try again; 0 to go on. */
  attempt(address: string, now = Date.now()): number {
    const blocked = this.blockedUntil.get(address);
    if (blocked !== undefined) {
      if (blocked > now) return blocked - now;
      this.blockedUntil.delete(address);
    }
    const recent = (this.tries.get(address) ?? []).filter(
      (at) => now - at < 60_000,
    );
    if (recent.length >= this.perMinute) {
      return 60_000 - (now - recent[0]);
    }
    recent.push(now);
    this.remember(this.tries, address, recent);
    return 0;
  }

  /** A wrong code from this address. */
  failed(address: string, now = Date.now()) {
    const recent = (this.failures.get(address) ?? []).filter(
      (at) => now - at < this.windowMs,
    );
    recent.push(now);
    this.remember(this.failures, address, recent);
    if (recent.length >= this.maxFailures) {
      this.blockedUntil.set(address, now + this.windowMs);
      this.failures.delete(address);
    }
  }

  private remember(
    map: Map<string, number[]>,
    address: string,
    list: number[],
  ) {
    if (map.size >= this.maxAddresses && !map.has(address)) map.clear();
    map.set(address, list);
  }
}
