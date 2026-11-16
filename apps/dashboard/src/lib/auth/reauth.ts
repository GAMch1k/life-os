import type { Pool, PoolClient } from "pg";

import { SESSION_FRESH_SECONDS } from "./constants";
import { verifyPassword } from "./password";
import { hasSecondFactor } from "./roles";

/**
 * Proving it is still you (P21-06's review): a stolen session must not
 * outlive itself in a credential, nor become two-factor.
 *
 *  - **Recent authentication** (a sign-in, or the password re-entered,
 *    within 12 hours, as adding a passkey and the sensitive admin actions
 *    need) is what making an API token, a pairing code, or an OAuth consent
 *    needs. Otherwise the page asks for the password (ReauthPrompt).
 *  - **Adding a second factor** (a passkey, the authenticator app) needs
 *    the account's current second factor when it has one (a session that
 *    signed in with it), or else the password re-entered within the last 5
 *    minutes. Only then does the session count as signed in with the new
 *    factor (the mfa claim).
 */

type Queryable = Pick<Pool | PoolClient, "query">;

/** Recent authentication: a sign-in or the password, within 12 hours. */
export const RECENT_AUTH_SECONDS = SESSION_FRESH_SECONDS;
/** Adding a first second factor needs the password from the last 5 minutes. */
export const FACTOR_PASSWORD_SECONDS = 5 * 60;

/** When the session last re-entered its password, if ever. */
export async function passwordConfirmedAt(
  db: Queryable,
  sessionId: string,
): Promise<Date | null> {
  const { rows } = await db.query<{ password_at: Date }>(
    "SELECT password_at FROM session_reauth WHERE session_id = $1",
    [sessionId],
  );
  return rows[0]?.password_at ?? null;
}

/**
 * Whether a session authenticated recently: it signed in, or re-entered
 * its password, within RECENT_AUTH_SECONDS.
 */
export async function recentlyAuthenticated(
  db: Queryable,
  session: { id?: string; createdAt?: Date | string | number },
  now = Date.now(),
): Promise<boolean> {
  if (!session.id) return false;
  const limit = now - RECENT_AUTH_SECONDS * 1000;
  if (
    session.createdAt !== undefined &&
    new Date(session.createdAt).getTime() > limit
  ) {
    return true;
  }
  const at = await passwordConfirmedAt(db, session.id);
  return at !== null && at.getTime() > limit;
}

/** Wrong passwords per session: 5 within 15 minutes, then none for 15. */
export class ReauthLimiter {
  private readonly failures = new Map<string, number[]>();

  constructor(
    private readonly max = 5,
    private readonly windowMs = 15 * 60_000,
    private readonly maxEntries = 10_000,
  ) {}

  blocked(key: string, now = Date.now()) {
    const recent = (this.failures.get(key) ?? []).filter(
      (at) => now - at < this.windowMs,
    );
    return recent.length >= this.max;
  }

  failed(key: string, now = Date.now()) {
    if (this.failures.size >= this.maxEntries) this.failures.clear();
    const recent = (this.failures.get(key) ?? []).filter(
      (at) => now - at < this.windowMs,
    );
    recent.push(now);
    this.failures.set(key, recent);
  }
}

/**
 * Checks the password a signed-in person re-entered against the account's
 * and notes it for the session. "limited" after too many wrong ones.
 */
export async function confirmPassword(
  db: Queryable,
  limiter: ReauthLimiter,
  session: { id: string; userId: string },
  password: unknown,
): Promise<"ok" | "invalid" | "limited"> {
  if (limiter.blocked(session.id)) return "limited";
  if (typeof password !== "string" || !password || password.length > 256) {
    limiter.failed(session.id);
    return "invalid";
  }
  const { rows } = await db.query<{ password: string | null }>(
    `SELECT password FROM account
      WHERE "userId" = $1 AND "providerId" = 'credential' AND password IS NOT NULL
      LIMIT 1`,
    [session.userId],
  );
  const hash = rows[0]?.password;
  if (!hash || !(await verifyPassword({ hash, password }))) {
    limiter.failed(session.id);
    return "invalid";
  }
  await db.query(
    `INSERT INTO session_reauth (session_id, password_at) VALUES ($1, now())
     ON CONFLICT (session_id) DO UPDATE SET password_at = EXCLUDED.password_at`,
    [session.id],
  );
  return "ok";
}

/**
 * Whether a session may add a second factor: with one already on the
 * account, only a session that signed in with it; without, after the
 * password re-entered within FACTOR_PASSWORD_SECONDS.
 */
export async function mayAddSecondFactor(
  db: Queryable,
  session: { id: string; userId: string; secondFactor?: boolean | null },
  now = Date.now(),
): Promise<"ok" | "second_factor_sign_in_required" | "password_required"> {
  if (session.secondFactor === true) return "ok";
  if (await hasSecondFactor(db, session.userId)) {
    return "second_factor_sign_in_required";
  }
  const at = await passwordConfirmedAt(db, session.id);
  return at !== null && now - at.getTime() < FACTOR_PASSWORD_SECONDS * 1000
    ? "ok"
    : "password_required";
}
