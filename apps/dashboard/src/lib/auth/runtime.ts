import { Pool } from "pg";

import { type CheckRuntime, LegacyCache } from "./check";
import { readAuthEnv } from "./env";
import {
  assertSignerTrusted,
  createSigner,
  parseTrustedKeys,
  TokenCache,
} from "./identity";
import { LegacyUseLog, noteSigningKey, pruneSecurityLog } from "./security-log";
import { createAuth } from "./server";
import { LoginThrottle } from "./throttle";

/**
 * The Dashboard's one auth runtime: the pool to auth_db, Better Auth, the
 * signer, and the caches. Created on first use (never during `next build`)
 * and kept on globalThis, because Next.js bundles the proxy (src/proxy.ts)
 * and the routes separately and both must share it.
 */
const KEY = Symbol.for("lifeos.auth.runtime");

type Holder = { [KEY]?: CheckRuntime };

export function createRuntime(source = process.env): CheckRuntime {
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
  return {
    env,
    pool,
    auth: createAuth(env, pool, { kind: "secure", throttle }),
    insecureAuth:
      env.insecureLoginHosts.length > 0
        ? createAuth(env, pool, { kind: "insecure", throttle })
        : undefined,
    signer,
    tokens: new TokenCache(),
    legacy: new LegacyCache(pool, env.legacyAccessDefault),
    legacyUses: new LegacyUseLog(pool),
  };
}

const TIMERS = Symbol.for("lifeos.auth.timers");

/**
 * The security log's upkeep while the Dashboard runs (src/instrumentation.ts
 * starts it, the tests do not): the legacy access counts are written every
 * minute, entries older than a year are deleted at start and once a day,
 * and a signing key that changed since the last start is logged as
 * rotated.
 */
export function startSecurityLog(rt: CheckRuntime) {
  const holder = globalThis as { [TIMERS]?: boolean };
  if (holder[TIMERS]) return;
  holder[TIMERS] = true;
  const prune = () =>
    pruneSecurityLog(rt.pool).catch((error) =>
      console.error("security log: pruning failed", (error as Error).message),
    );
  void prune();
  void noteSigningKey(rt.pool, rt.signer.kid).catch((error) =>
    console.error(
      "security log: the signing key was not noted",
      (error as Error).message,
    ),
  );
  setInterval(() => void rt.legacyUses?.flush(), 60_000).unref();
  setInterval(() => void prune(), 24 * 60 * 60 * 1000).unref();
}

export function authRuntime(): CheckRuntime {
  const holder = globalThis as Holder;
  holder[KEY] ??= createRuntime();
  return holder[KEY];
}
