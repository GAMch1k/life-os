import { Pool } from "pg";

import { resolveTimeZone } from "../time-zone";
import { type CheckRuntime, LegacyCache } from "./check";
import { EmailLimiter } from "./email-account";
import { processEmailOutbox, registerOutbox } from "./email-alerts";
import { emailService } from "./email-service";
import { readAuthEnv } from "./env";
import {
  assertSignerTrusted,
  createSigner,
  parseTrustedKeys,
  TokenCache,
} from "./identity";
import {
  KeyUseLog,
  notificationsCleanup,
  processKeyCleanup,
  pruneExpiredKeys,
} from "./keys";
import { McpTokenVerifier, pruneOAuthClients } from "./oauth";
import { processRevocations } from "./security-hooks";
import { LegacyUseLog, noteSigningKey, pruneSecurityLog } from "./security-log";
import { createAuth, mcpResource } from "./server";
import { LoginThrottle } from "./throttle";

/**
 * The Dashboard's one auth runtime: the pool to auth_db, Better Auth, the
 * signer, and the caches. Created on first use (never during `next build`)
 * and kept on globalThis, because Next.js bundles the proxy (src/proxy.ts)
 * and the routes separately and both must share it.
 */
const KEY = Symbol.for("lifeos.auth.runtime");

type Holder = { [KEY]?: CheckRuntime };

export function createRuntime(
  source: Record<string, string | undefined> = process.env,
  deps: { fetch?: typeof fetch } = {},
): CheckRuntime {
  const env = readAuthEnv(source);
  const signer = createSigner(env.signingKey);
  // The signer's own key must be one the services trust, or every token
  // it mints is refused: say so at once rather than per request.
  assertSignerTrusted(signer, parseTrustedKeys(env.trustedKeys));
  const pool = new Pool({
    connectionString: env.databaseUrl,
    max: 8,
    idleTimeoutMillis: 30_000,
    connectionTimeoutMillis: 5_000,
  });
  pool.on("error", (error) => {
    console.error("auth_db pool error", error.message);
  });
  const throttle = new LoginThrottle();
  const cleanup = notificationsCleanup(
    source.NOTIFICATIONS_SERVICE_URL,
    deps.fetch,
  );
  let cleaning: Promise<void> | null = null;
  // Email (docs/install/email.md): through the notifications service, on
  // when its SMTP settings are; the security alerts' outbox is sent at
  // once and every minute, one run at a time.
  const email = emailService(source.NOTIFICATIONS_SERVICE_URL, deps.fetch);
  const emailLimiter = new EmailLimiter();
  let alerting: Promise<void> | null = null;
  let again = false;
  const emailAlerts = (): Promise<void> => {
    if (alerting) {
      again = true;
      return alerting;
    }
    alerting = processEmailOutbox({
      pool,
      email,
      publicUrl: env.publicUrl,
      timeZone: resolveTimeZone(source.LIFE_OS_TIMEZONE),
    })
      .then(() => undefined)
      .catch((error) =>
        console.error(
          "email: security alerts will be retried",
          (error as Error).message,
        ),
      )
      .finally(() => {
        alerting = null;
        if (again) {
          again = false;
          void emailAlerts();
        }
      });
    return alerting;
  };
  registerOutbox(pool, {
    enabled: () => (email ? email.known() : false),
    kick: () => void emailAlerts(),
  });
  const authDeps = { throttle, email, emailLimiter };
  return {
    env,
    pool,
    auth: createAuth(env, pool, { kind: "secure", ...authDeps }),
    loopbackAuth: createAuth(env, pool, { kind: "loopback", ...authDeps }),
    insecureAuth:
      env.insecureLoginHosts.length > 0
        ? createAuth(env, pool, { kind: "insecure", ...authDeps })
        : undefined,
    email,
    emailLimiter,
    emailAlerts,
    signer,
    tokens: new TokenCache(),
    legacy: new LegacyCache(pool, env.legacyAccessDefault),
    legacyUses: new LegacyUseLog(pool),
    keyUses: new KeyUseLog(pool),
    mcpTokens: new McpTokenVerifier(pool, env.publicUrl, mcpResource(env)),
    keyCleanup: () => {
      // One run at a time; the minute's timer catches up with the rest.
      cleaning ??= processKeyCleanup(pool, cleanup)
        .then(() => undefined)
        .catch((error) =>
          console.error(
            "keys: push subscriptions of revoked devices were not deleted",
            (error as Error).message,
          ),
        )
        .finally(() => {
          cleaning = null;
        });
      return cleaning;
    },
  };
}

const TIMERS = Symbol.for("lifeos.auth.timers");

/**
 * The security log's upkeep while the Dashboard runs (src/instrumentation.ts
 * starts it, the tests do not): the legacy access counts are written every
 * minute, entries older than a year are deleted at start and once a day,
 * and a signing key that changed since the last start is logged as
 * rotated. With P21-06, also the push subscriptions of revoked device keys
 * (every minute) and MCP clients nobody connected (once a day).
 */
export function startSecurityLog(rt: CheckRuntime) {
  const holder = globalThis as { [TIMERS]?: boolean };
  if (holder[TIMERS]) return;
  holder[TIMERS] = true;
  const prune = () => {
    pruneSecurityLog(rt.pool).catch((error) =>
      console.error("security log: pruning failed", (error as Error).message),
    );
    pruneExpiredKeys(rt.pool).catch((error) =>
      console.error(
        "keys: pruning expired tokens failed",
        (error as Error).message,
      ),
    );
    pruneOAuthClients(rt.pool).catch((error) =>
      console.error(
        "oauth: pruning unused clients failed",
        (error as Error).message,
      ),
    );
  };
  prune();
  void noteSigningKey(rt.pool, rt.signer.kid).catch((error) =>
    console.error(
      "security log: the signing key was not noted",
      (error as Error).message,
    ),
  );
  rt.keyCleanup?.();
  // Credential revocations that an admin action, a password reset, or a
  // password change could not finish at once (security-hooks.ts).
  const revocations = () =>
    processRevocations(rt.pool).catch((error) =>
      console.error(
        "keys: credential revocations will be retried",
        (error as Error).message,
      ),
    );
  void revocations();
  setInterval(() => void rt.legacyUses?.flush(), 60_000).unref();
  void rt.emailAlerts?.();
  setInterval(() => {
    void rt.keyCleanup?.();
    void revocations();
    void rt.emailAlerts?.();
  }, 60_000).unref();
  setInterval(prune, 24 * 60 * 60 * 1000).unref();
}

export function authRuntime(): CheckRuntime {
  const holder = globalThis as Holder;
  holder[KEY] ??= createRuntime();
  return holder[KEY];
}
