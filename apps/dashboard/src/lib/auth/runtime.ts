import { Pool } from "pg";

import { type CheckRuntime, LegacyCache } from "./check";
import { readAuthEnv } from "./env";
import { createSigner, parseTrustedKeys, TokenCache } from "./identity";
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
  if (!parseTrustedKeys(env.trustedKeys).has(signer.kid)) {
    throw new Error(
      `LIFE_OS_TRUSTED_KEYS does not list the signing key ${signer.kid}: run ./setup.sh rotate-keys auth, or add its public key`,
    );
  }
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
  };
}

export function authRuntime(): CheckRuntime {
  const holder = globalThis as Holder;
  holder[KEY] ??= createRuntime();
  return holder[KEY];
}
