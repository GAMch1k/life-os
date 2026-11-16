import { createPublicKey, type KeyObject, verify } from "node:crypto";

import type { Pool, PoolClient } from "pg";

import { MCP_RESOURCE_PATH, OAUTH_SCOPES, type ServerRole } from "./constants";
import { serverRole } from "./roles";

/**
 * MCP over OAuth (docs/services/mcp.md#signing-in-with-oauth): Better
 * Auth's OAuth provider issues 15-minute JWT access tokens, signed with the
 * jwt plugin's key (auth_db's jwks), bound to the resource <public
 * URL>/api/mcp. The auth check verifies them here, against that key read
 * from the database (no fetch), and then looks the grant up, the person's
 * consent for that client: revoking it in Settings → Connected apps counts
 * on the next request, not when the token runs out.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

const ACCESS_TOKEN_TYPES = new Set(["at+jwt", "application/at+jwt"]);
/** Clock leeway, as pkg/identity allows. */
const LEEWAY_SECONDS = 30;

export type McpAccessToken = {
  userId: string;
  clientId: string;
  scopes: string[];
  issuedAt: number;
  expiresAt: number;
};

/** The WWW-Authenticate challenge of MCP's 401 (RFC 9728, RFC 6750). */
export function mcpChallenge(publicUrl: string, error?: "invalid_token") {
  const parts = [
    `resource_metadata="${publicUrl}/.well-known/oauth-protected-resource${MCP_RESOURCE_PATH}"`,
    `scope="${OAUTH_SCOPES.join(" ")}"`,
  ];
  if (error) parts.push(`error="${error}"`);
  return `Bearer ${parts.join(", ")}`;
}

function decode(segment: string): Record<string, unknown> | null {
  try {
    const value = JSON.parse(Buffer.from(segment, "base64url").toString());
    return value && typeof value === "object" ? value : null;
  } catch {
    return null;
  }
}

/** Whether a bearer value looks like a JWT at all. */
export function isJwtShaped(value: string) {
  return /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(value);
}

/**
 * Verifies MCP access tokens: EdDSA (the jwt plugin's default, Ed25519)
 * with a key of auth_db's jwks, the issuer (the public URL), the audience
 * (the MCP resource), the lifetime, and the type at+jwt. The keys are
 * cached; an unknown key id reloads them, at most every 10 seconds.
 */
export class McpTokenVerifier {
  private keys = new Map<string, KeyObject>();
  private loadedAt = 0;

  constructor(
    private readonly db: Queryable,
    private readonly issuer: string,
    private readonly resource: string,
  ) {}

  private async load(now: number) {
    const { rows } = await this.db.query<{
      id: string;
      publicKey: string;
      alg: string | null;
    }>('SELECT id, "publicKey", alg FROM jwks');
    const keys = new Map<string, KeyObject>();
    for (const row of rows) {
      if (row.alg && row.alg !== "EdDSA") continue;
      try {
        const jwk = JSON.parse(row.publicKey);
        if (jwk.kty !== "OKP" || jwk.crv !== "Ed25519") continue;
        keys.set(
          row.id,
          createPublicKey({
            key: { kty: "OKP", crv: "Ed25519", x: jwk.x },
            format: "jwk",
          }),
        );
      } catch {
        // A key that does not parse verifies nothing.
      }
    }
    this.keys = keys;
    this.loadedAt = now;
  }

  async verify(
    token: string,
    nowSeconds = Math.floor(Date.now() / 1000),
  ): Promise<McpAccessToken | null> {
    if (token.length > 8192 || !isJwtShaped(token)) return null;
    const [h, p, s] = token.split(".");
    const header = decode(h);
    if (!header || header.alg !== "EdDSA") return null;
    if (typeof header.typ !== "string" || !ACCESS_TOKEN_TYPES.has(header.typ)) {
      return null;
    }
    const kid = typeof header.kid === "string" ? header.kid : "";
    let key = this.keys.get(kid);
    if (!key && Date.now() - this.loadedAt > 10_000) {
      await this.load(Date.now());
      key = this.keys.get(kid);
    }
    if (!key) return null;
    let valid = false;
    try {
      valid = verify(
        null,
        Buffer.from(`${h}.${p}`),
        key,
        Buffer.from(s, "base64url"),
      );
    } catch {
      valid = false;
    }
    if (!valid) return null;
    const claims = decode(p);
    if (!claims) return null;
    if (claims.iss !== this.issuer) return null;
    const audiences = Array.isArray(claims.aud) ? claims.aud : [claims.aud];
    if (!audiences.includes(this.resource)) return null;
    const exp = Number(claims.exp);
    const iat = Number(claims.iat);
    if (!Number.isFinite(exp) || exp + LEEWAY_SECONDS <= nowSeconds) {
      return null;
    }
    if (!Number.isFinite(iat) || iat - LEEWAY_SECONDS > nowSeconds) return null;
    if (
      claims.nbf !== undefined &&
      Number(claims.nbf) - LEEWAY_SECONDS > nowSeconds
    ) {
      return null;
    }
    const clientId =
      typeof claims.client_id === "string" ? claims.client_id : claims.azp;
    if (typeof claims.sub !== "string" || typeof clientId !== "string") {
      return null;
    }
    const scopes =
      typeof claims.scope === "string"
        ? claims.scope.split(" ").filter(Boolean)
        : [];
    return {
      userId: claims.sub,
      clientId,
      scopes,
      issuedAt: iat,
      expiresAt: exp,
    };
  }
}

export type McpGrant = {
  id: string;
  scopes: string[];
  createdAt: Date;
  user: { role: ServerRole; banned: boolean; authzVersion: number };
};

/**
 * The grant behind an access token: the person's consent for the client,
 * given before the token was issued (a token from before a revocation is
 * refused even after a new consent), of a client that is not disabled.
 */
export async function findGrant(
  db: Queryable,
  token: McpAccessToken,
): Promise<McpGrant | null> {
  const { rows } = await db.query<{
    id: string;
    scopes: unknown;
    created_at: Date;
    role: string | null;
    banned: boolean | null;
    ban_expires: Date | null;
    authz_version: number | null;
  }>(
    `SELECT c.id, c.scopes, c."createdAt" AS created_at, u.role, u.banned,
            u."banExpires" AS ban_expires, u."authzVersion" AS authz_version
       FROM "oauthConsent" c
       JOIN "user" u ON u.id = c."userId"
       JOIN "oauthClient" cl ON cl."clientId" = c."clientId"
      WHERE c."userId"::text = $1 AND c."clientId" = $2
        AND cl.disabled IS NOT TRUE
      ORDER BY c."updatedAt" DESC LIMIT 1`,
    [token.userId, token.clientId],
  );
  const row = rows[0];
  if (!row) return null;
  if (Math.floor(row.created_at.getTime() / 1000) > token.issuedAt) return null;
  const banned =
    row.banned === true &&
    (!row.ban_expires || new Date(row.ban_expires).getTime() > Date.now());
  return {
    id: row.id,
    scopes: Array.isArray(row.scopes)
      ? row.scopes.filter((s): s is string => typeof s === "string")
      : [],
    createdAt: row.created_at,
    user: {
      role: serverRole(row.role),
      banned,
      authzVersion: row.authz_version ?? 0,
    },
  };
}

export type ConnectedApp = {
  clientId: string;
  name: string | null;
  uri: string | null;
  scopes: string[];
  createdAt: string;
  updatedAt: string;
};

/** The MCP clients an account consented to (Settings → Connected apps). */
export async function listConnectedApps(
  db: Queryable,
  userId: string,
): Promise<ConnectedApp[]> {
  const { rows } = await db.query<{
    client_id: string;
    name: string | null;
    uri: string | null;
    scopes: unknown;
    created_at: Date;
    updated_at: Date;
  }>(
    `SELECT c."clientId" AS client_id, cl.name, cl.uri, c.scopes,
            c."createdAt" AS created_at, c."updatedAt" AS updated_at
       FROM "oauthConsent" c
       JOIN "oauthClient" cl ON cl."clientId" = c."clientId"
      WHERE c."userId" = $1
      ORDER BY c."updatedAt" DESC`,
    [userId],
  );
  return rows.map((row) => ({
    clientId: row.client_id,
    name: row.name,
    uri: row.uri,
    scopes: Array.isArray(row.scopes)
      ? row.scopes.filter((s): s is string => typeof s === "string")
      : [],
    createdAt: row.created_at.toISOString(),
    updatedAt: row.updated_at.toISOString(),
  }));
}

/**
 * Revokes a connected app, in one transaction: the consent and every
 * refresh and access token the client holds for the account. Its JWT
 * access tokens still in flight are refused at once, since the check looks
 * the consent up. Returns whether there was one.
 */
export async function revokeConnectedApp(
  pool: Pool,
  userId: string,
  clientId: string,
): Promise<boolean> {
  const client = await pool.connect();
  try {
    await client.query("BEGIN");
    await client.query(
      `DELETE FROM "oauthAccessToken" WHERE "userId" = $1 AND "clientId" = $2`,
      [userId, clientId],
    );
    await client.query(
      `DELETE FROM "oauthRefreshToken" WHERE "userId" = $1 AND "clientId" = $2`,
      [userId, clientId],
    );
    const { rowCount } = await client.query(
      `DELETE FROM "oauthConsent" WHERE "userId" = $1 AND "clientId" = $2`,
      [userId, clientId],
    );
    await client.query("COMMIT");
    return (rowCount ?? 0) > 0;
  } catch (error) {
    await client.query("ROLLBACK").catch(() => undefined);
    throw error;
  } finally {
    client.release();
  }
}

/**
 * Registered MCP clients nobody consented to within a week are deleted
 * (registration is open, so it must not grow without bound).
 */
export async function pruneOAuthClients(db: Queryable): Promise<number> {
  const { rowCount } = await db.query(
    `DELETE FROM "oauthClient" cl
      WHERE cl."createdAt" < now() - interval '7 days'
        AND cl."userId" IS NULL
        AND NOT EXISTS (SELECT 1 FROM "oauthConsent" c WHERE c."clientId" = cl."clientId")
        AND NOT EXISTS (SELECT 1 FROM "oauthRefreshToken" r WHERE r."clientId" = cl."clientId")`,
  );
  return rowCount ?? 0;
}
