import type { Pool, PoolClient } from "pg";

import { isPlaceholderEmail } from "./email-account";
import type { EmailService } from "./email-service";
import type { AlertKind } from "./email-kinds";
import { securityAlertMessage } from "./email-templates";

/**
 * Security alert emails (docs/install/email.md#security-alerts): what
 * changed in an account's sign-in, mailed to its verified address unless
 * the person turned that kind off (Settings → Email). An alert is queued
 * with the security log's entry (security-log.ts), in auth_db's
 * email_outbox, and sent from there by the Dashboard, at once and then
 * every minute until it went out (or a day passed): a sign-in never waits
 * for the mail server, and an alert survives a restart or a notifications
 * service that is down. The host's commands queue theirs the same way
 * (scripts/lifeos-auth.mjs).
 */

type Queryable = Pick<Pool | PoolClient, "query">;

/** The security log's entries that alert, and the switch each falls under. */
const EVENT_ALERTS: Record<string, AlertKind> = {
  password_changed: "password",
  password_reset: "password",
  email_changed: "password",
  two_factor_enabled: "two_factor",
  two_factor_disabled: "two_factor",
  recovery_codes_created: "two_factor",
  passkey_added: "two_factor",
  passkey_removed: "two_factor",
  token_created: "credentials",
  device_paired: "credentials",
  oauth_consent_given: "credentials",
  legacy_access_changed: "legacy_access",
  signed_out_everywhere: "sign_out",
  signed_out_everyone: "sign_out",
};

/** The alert a security log entry makes, if any: only what went through. */
export function alertFor(entry: {
  kind: string;
  outcome?: string;
  detail?: string | null;
}): AlertKind | null {
  if ((entry.outcome ?? "ok") !== "ok") return null;
  const kind = EVENT_ALERTS[entry.kind];
  if (!kind) return null;
  // Legacy access alerts when it goes on; off is the safe way.
  if (entry.kind === "legacy_access_changed" && entry.detail !== "on") {
    return null;
  }
  return kind;
}

type OutboxHooks = {
  /** Whether email is on, as far as the Dashboard knows (undefined: not yet). */
  enabled(): boolean | undefined;
  /** Starts sending now. */
  kick(): void;
};

const HOOKS = Symbol.for("lifeos.email.outbox-hooks");

function hooksMap(): WeakMap<object, OutboxHooks> {
  const holder = globalThis as { [HOOKS]?: WeakMap<object, OutboxHooks> };
  holder[HOOKS] ??= new WeakMap();
  return holder[HOOKS];
}

/** The runtime's outbox, for the pool it writes with (runtime.ts). */
export function registerOutbox(pool: object, hooks: OutboxHooks) {
  hooksMap().set(pool, hooks);
}

export type QueuedAlert = {
  userId: string;
  kind: AlertKind;
  /** The security log's kind and detail: what the email says happened. */
  event: string;
  detail?: string | null;
  /** Already checked: an address and a device label, as the log keeps them. */
  address?: string | null;
  device?: string | null;
  /** Another address than the account's own (the old one, when it changed). */
  toAddress?: string | null;
};

/**
 * Queues an alert. It never throws: an account action must not fail
 * because its alert could not be queued. With email known to be off,
 * nothing is queued.
 */
export async function queueAlert(db: Queryable, alert: QueuedAlert) {
  const hooks = hooksMap().get(db);
  if (hooks?.enabled() === false) return;
  try {
    await db.query(
      `INSERT INTO email_outbox (user_id, kind, event, detail, address, device, to_address)
       SELECT id, $2, $3, $4, $5, $6, $7 FROM "user" WHERE id = $1`,
      [
        alert.userId,
        alert.kind,
        alert.event,
        alert.detail ?? null,
        alert.address ?? null,
        alert.device ?? null,
        alert.toAddress ?? null,
      ],
    );
  } catch (error) {
    console.error(
      "email: an alert was not queued",
      alert.kind,
      error instanceof Error ? error.message : error,
    );
    return;
  }
  hooks?.kick();
}

/** Minutes until the next try, by the tries so far; then it is dropped. */
const BACKOFF_MINUTES = [1, 5, 15, 60, 180];

export type OutboxDeps = {
  pool: Pool;
  email: EmailService | null;
  publicUrl: string;
  timeZone?: string;
};

/**
 * Sends what is due. With email off, the queue is dropped; with the
 * notifications service down, it waits. An alert goes only to a verified
 * address (or the old one it names), and only when its kind is on.
 */
export async function processEmailOutbox(deps: OutboxDeps): Promise<number> {
  const { pool } = deps;
  if (!deps.email) {
    await pool.query("DELETE FROM email_outbox");
    return 0;
  }
  const status = await deps.email.status();
  if (status === null) return 0;
  if (!status.configured) {
    await pool.query("DELETE FROM email_outbox");
    return 0;
  }
  await pool.query(
    "DELETE FROM email_outbox WHERE at < now() - interval '1 day'",
  );
  const { rows } = await pool.query<{
    id: string;
    user_id: string;
    kind: AlertKind;
    event: string;
    detail: string | null;
    address: string | null;
    device: string | null;
    to_address: string | null;
    at: Date;
    attempts: number;
    email: string;
    verified: boolean | null;
    name: string;
    username: string | null;
    enabled: boolean | null;
  }>(
    `SELECT o.id, o.user_id, o.kind, o.event, o.detail, o.address, o.device,
            o.to_address, o.at, o.attempts, u.email, u."emailVerified" AS verified,
            u.name, coalesce(u."displayUsername", u.username) AS username,
            p.enabled
       FROM email_outbox o
       JOIN "user" u ON u.id = o.user_id
       LEFT JOIN email_preferences p ON p.user_id = o.user_id AND p.kind = o.kind
      WHERE o.next_attempt_at <= now()
      ORDER BY o.at, o.id
      LIMIT 25`,
  );
  let sent = 0;
  for (const row of rows) {
    const own =
      row.verified === true && !isPlaceholderEmail(row.email)
        ? row.email
        : null;
    const to = row.to_address ?? own;
    if (row.enabled === false || !to) {
      await pool.query("DELETE FROM email_outbox WHERE id = $1", [row.id]);
      continue;
    }
    const message = securityAlertMessage({
      publicUrl: deps.publicUrl,
      name: row.username ?? row.name,
      kind: row.kind,
      event: row.event,
      detail: row.detail,
      at: row.at,
      timeZone: deps.timeZone,
      address: row.address,
      device: row.device,
    });
    const result = await deps.email.send({
      to,
      ...message,
      kind: `alert.${row.kind}`,
    });
    if (
      result.ok ||
      !result.retry ||
      row.attempts + 1 >= BACKOFF_MINUTES.length
    ) {
      await pool.query("DELETE FROM email_outbox WHERE id = $1", [row.id]);
      if (result.ok) sent += 1;
    } else {
      await pool.query(
        `UPDATE email_outbox SET attempts = attempts + 1,
                next_attempt_at = now() + make_interval(mins => $2)
          WHERE id = $1`,
        [row.id, BACKOFF_MINUTES[row.attempts]],
      );
    }
    if (
      !result.ok &&
      (result.code === "rate_limited" ||
        result.code === "notifications_unavailable")
    ) {
      break;
    }
  }
  return sent;
}
