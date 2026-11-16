import {
  createPrivateKey,
  createPublicKey,
  type KeyObject,
  randomUUID,
  sign,
  verify,
} from "node:crypto";

import {
  IDENTITY_AUDIENCE,
  IDENTITY_ISSUER,
  IDENTITY_MAX_BYTES,
  IDENTITY_REMINT_SECONDS,
  IDENTITY_TTL_SECONDS,
  IDENTITY_TYPE,
  type ServerRole,
} from "./constants";

/**
 * Identity tokens (docs/architecture/multi-user.md#the-identity-token): a
 * compact JWS, EdDSA over Ed25519, signed with LIFE_OS_IDENTITY_SIGNING_KEY
 * and verified by the services with the public keys of
 * LIFE_OS_TRUSTED_KEYS (pkg/identity), never fetched from anywhere.
 */

/**
 * How the caller proved who they are: a browser's session, legacy access,
 * a paired device's key, a personal API token, or an MCP client's OAuth
 * access token (P21-06).
 */
export type Via = "session" | "legacy" | "device" | "token" | "mcp";
/** A role in a space: owner, admin, member. */
export type SpaceRole = "o" | "a" | "m";

export type IdentityClaims = {
  sub: string;
  /** The credential: the session, the API key, the OAuth grant, "legacy". */
  sid: string;
  via: Via;
  role: ServerRole;
  sp: Record<string, SpaceRole>;
  psp?: string;
  /**
   * Scopes, on personal tokens and MCP grants: `<module>:read` or
   * `<module>:write` (which includes reading). Absent: all the user's rights.
   */
  scp?: string[];
  /**
   * Only true, and only on a browser session that signed in with a second
   * factor (an authenticator code, a recovery code, or a passkey): what the
   * services' admin routes require. Never on a key, a token, or an MCP grant.
   */
  mfa?: true;
  /**
   * A personal token marked as a development tool: its name, which makes
   * the activity actor dev:<name>.
   */
  dev?: string;
};

export type Signer = {
  kid: string;
  sign(claims: IdentityClaims, nowSeconds?: number): string;
};

type Jwk = {
  kty?: string;
  crv?: string;
  kid?: string;
  x?: string;
  d?: string;
};

function parseJwk(text: string, what: string): Jwk {
  let jwk: unknown;
  try {
    jwk = JSON.parse(text);
  } catch {
    throw new Error(`${what} is not JSON (a JWK)`);
  }
  if (typeof jwk !== "object" || jwk === null) {
    throw new Error(`${what} is not a JWK`);
  }
  const key = jwk as Jwk;
  if (key.kty !== "OKP" || key.crv !== "Ed25519") {
    throw new Error(`${what} is not an Ed25519 key (kty OKP, crv Ed25519)`);
  }
  if (!key.kid || !/^[A-Za-z0-9._-]{1,64}$/.test(key.kid)) {
    throw new Error(`${what} has no valid kid`);
  }
  return key;
}

const base64url = (value: string | Buffer) =>
  Buffer.from(value).toString("base64url");

/** The signer of LIFE_OS_IDENTITY_SIGNING_KEY (a private JWK with its kid). */
export function createSigner(signingKey: string): Signer {
  const jwk = parseJwk(signingKey, "LIFE_OS_IDENTITY_SIGNING_KEY");
  if (!jwk.d || !jwk.x) {
    throw new Error("LIFE_OS_IDENTITY_SIGNING_KEY has no private part (d)");
  }
  const kid = jwk.kid as string;
  const key = createPrivateKey({
    key: { kty: "OKP", crv: "Ed25519", d: jwk.d, x: jwk.x },
    format: "jwk",
  });
  const header = base64url(
    JSON.stringify({ alg: "EdDSA", typ: IDENTITY_TYPE, kid }),
  );
  return {
    kid,
    sign(claims, nowSeconds = Math.floor(Date.now() / 1000)) {
      const payload = {
        iss: IDENTITY_ISSUER,
        aud: IDENTITY_AUDIENCE,
        ...claims,
        iat: nowSeconds,
        nbf: nowSeconds,
        exp: nowSeconds + IDENTITY_TTL_SECONDS,
        jti: randomUUID(),
      };
      const input = `${header}.${base64url(JSON.stringify(payload))}`;
      const token = `${input}.${base64url(sign(null, Buffer.from(input), key))}`;
      if (token.length > IDENTITY_MAX_BYTES) {
        throw new Error(
          `the identity token would be ${token.length} bytes, over ${IDENTITY_MAX_BYTES}`,
        );
      }
      return token;
    },
  };
}

/** The public keys of LIFE_OS_TRUSTED_KEYS ({"keys":[JWK, ...]}), by kid. */
export function parseTrustedKeys(text: string): Map<string, KeyObject> {
  let set: unknown;
  try {
    set = JSON.parse(text);
  } catch {
    throw new Error("LIFE_OS_TRUSTED_KEYS is not JSON (a JWKS)");
  }
  const keys = (set as { keys?: unknown })?.keys;
  if (!Array.isArray(keys) || keys.length === 0) {
    throw new Error("LIFE_OS_TRUSTED_KEYS has no keys");
  }
  const result = new Map<string, KeyObject>();
  for (const entry of keys) {
    const jwk = parseJwk(
      JSON.stringify(entry),
      "a key in LIFE_OS_TRUSTED_KEYS",
    );
    if (jwk.d) {
      throw new Error(
        "LIFE_OS_TRUSTED_KEYS holds a private key: list public keys only",
      );
    }
    result.set(
      jwk.kid as string,
      createPublicKey({
        key: { kty: "OKP", crv: "Ed25519", x: jwk.x as string },
        format: "jwk",
      }),
    );
  }
  return result;
}

/**
 * Verifies a token as pkg/identity does; for tests and for the Dashboard's
 * own checks of its configuration (the signer's key must be trusted).
 */
export function verifyIdentityToken(
  token: string,
  keys: Map<string, KeyObject>,
  nowSeconds = Math.floor(Date.now() / 1000),
): (IdentityClaims & Record<string, unknown>) | null {
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  try {
    const header = JSON.parse(Buffer.from(parts[0], "base64url").toString());
    if (header.alg !== "EdDSA" || header.typ !== IDENTITY_TYPE) return null;
    const key = keys.get(header.kid);
    if (!key) return null;
    const ok = verify(
      null,
      Buffer.from(`${parts[0]}.${parts[1]}`),
      key,
      Buffer.from(parts[2], "base64url"),
    );
    if (!ok) return null;
    const claims = JSON.parse(Buffer.from(parts[1], "base64url").toString());
    if (claims.iss !== IDENTITY_ISSUER || claims.aud !== IDENTITY_AUDIENCE) {
      return null;
    }
    if (claims.exp + 30 < nowSeconds || claims.nbf - 30 > nowSeconds) {
      return null;
    }
    return claims;
  } catch {
    return null;
  }
}

type Cached = { token: string; exp: number; version: string };

/**
 * Checks, once at startup, that the services will accept what the signer
 * mints: LIFE_OS_TRUSTED_KEYS must list the signing key's kid with its
 * public key. A probe token is signed and verified; a pair that does not
 * match (the same kid, another key) fails here, loudly, rather than as a
 * 401 on every request.
 */
export function assertSignerTrusted(
  signer: Signer,
  keys: Map<string, KeyObject>,
): void {
  if (!keys.has(signer.kid)) {
    throw new Error(
      `LIFE_OS_TRUSTED_KEYS does not list the signing key ${signer.kid}: run ./setup.sh rotate-keys auth, or add its public key`,
    );
  }
  const probe = signer.sign({
    sub: "00000000-0000-4000-8000-000000000000",
    sid: "startup-probe",
    via: "session",
    role: "member",
    sp: {},
  });
  if (!verifyIdentityToken(probe, keys)) {
    throw new Error(
      `LIFE_OS_TRUSTED_KEYS lists ${signer.kid} with another public key than LIFE_OS_IDENTITY_SIGNING_KEY has: the services would refuse every token (docs/install/accounts.md#keys)`,
    );
  }
}

/**
 * Minted tokens, per credential: the check hands one out again only while
 * at least IDENTITY_REMINT_SECONDS (4 of its 5 minutes) are left, so that a
 * token a service gets is never older than a minute; it re-mints sooner when
 * what the token reflects changed (the user's authz_version, role).
 */
export class TokenCache {
  private readonly entries = new Map<string, Cached>();

  constructor(private readonly maxEntries = 10_000) {}

  get(
    credential: string,
    version: string,
    mint: () => string,
    nowSeconds = Math.floor(Date.now() / 1000),
  ): string {
    const cached = this.entries.get(credential);
    if (
      cached &&
      cached.version === version &&
      cached.exp - nowSeconds > IDENTITY_REMINT_SECONDS
    ) {
      return cached.token;
    }
    const token = mint();
    if (this.entries.size >= this.maxEntries) this.prune(nowSeconds);
    this.entries.set(credential, {
      token,
      exp: nowSeconds + IDENTITY_TTL_SECONDS,
      version,
    });
    return token;
  }

  forget(credential: string) {
    this.entries.delete(credential);
  }

  clear() {
    this.entries.clear();
  }

  private prune(nowSeconds: number) {
    for (const [credential, entry] of this.entries) {
      if (entry.exp - nowSeconds <= IDENTITY_REMINT_SECONDS) {
        this.entries.delete(credential);
      }
    }
    // Still full: drop the oldest half (Map keeps insertion order).
    if (this.entries.size >= this.maxEntries) {
      let drop = Math.floor(this.entries.size / 2);
      for (const credential of this.entries.keys()) {
        if (drop-- <= 0) break;
        this.entries.delete(credential);
      }
    }
  }
}
