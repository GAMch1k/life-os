/**
 * The Dashboard's account settings, from the environment compose.yaml passes
 * (docs/architecture/multi-user.md, docs/operations/deployment.md). Read on
 * first use, never at import, so that `next build` needs none of them.
 */
export type AuthEnv = {
  /** postgres:// URL of auth_db as auth_user. */
  databaseUrl: string;
  /** Better Auth's secret: signs cookies, encrypts the TOTP secrets. */
  secret: string;
  /** The Ed25519 private key (a JWK) that signs identity tokens. */
  signingKey: string;
  /** The public keys services trust, as a JWKS (LIFE_OS_TRUSTED_KEYS). */
  trustedKeys: string;
  /** The address Life OS is reached at (LIFE_OS_PUBLIC_URL). */
  publicUrl: string;
  /** Plain-HTTP hosts on which signing in is allowed anyway. */
  insecureLoginHosts: string[];
  /** Legacy access the first time auth_db is used: on after an upgrade. */
  legacyAccessDefault: boolean;
};

type Source = Record<string, string | undefined>;

function required(source: Source, name: string): string {
  const value = source[name]?.trim();
  if (!value) {
    throw new Error(
      `${name} is not set: the Dashboard needs it for accounts (see .env.example)`,
    );
  }
  return value;
}

/** Host names, lowercase, without a port, from a comma-separated list. */
export function hostList(value: string | undefined): string[] {
  return (value ?? "")
    .split(",")
    .map((host) => host.trim().toLowerCase().replace(/:\d+$/, ""))
    .filter(Boolean);
}

export function onOff(value: string | undefined, fallback: boolean): boolean {
  switch ((value ?? "").trim().toLowerCase()) {
    case "on":
    case "yes":
    case "true":
    case "1":
      return true;
    case "off":
    case "no":
    case "false":
    case "0":
      return false;
    default:
      return fallback;
  }
}

export function readAuthEnv(source: Source = process.env): AuthEnv {
  const publicUrl = required(source, "LIFE_OS_PUBLIC_URL").replace(/\/+$/, "");
  try {
    new URL(publicUrl);
  } catch {
    throw new Error(`LIFE_OS_PUBLIC_URL is not a URL: ${publicUrl}`);
  }
  return {
    databaseUrl: required(source, "AUTH_DATABASE_URL"),
    secret: required(source, "LIFE_OS_AUTH_SECRET"),
    signingKey: required(source, "LIFE_OS_IDENTITY_SIGNING_KEY"),
    trustedKeys: required(source, "LIFE_OS_TRUSTED_KEYS"),
    publicUrl,
    insecureLoginHosts: hostList(source.LIFE_OS_INSECURE_LOGIN_HOSTS),
    legacyAccessDefault: onOff(source.LIFE_OS_LEGACY_ACCESS, false),
  };
}
