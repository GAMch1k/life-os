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

/** Better Auth's secret is at least this many bytes (./setup.sh makes 64). */
export const AUTH_SECRET_MIN_BYTES = 32;

/** Whether a value is still one of .env.example's replace-with-… placeholders. */
export function isPlaceholder(value: string): boolean {
  return /^replace-with-/i.test(value.trim());
}

/**
 * A value that must not be public: set, and not .env.example's placeholder,
 * which anyone can read. Refused at start with what to do instead.
 */
function notPlaceholder(source: Source, name: string): string {
  const value = required(source, name);
  if (isPlaceholder(value)) {
    throw new Error(
      `${name} is still .env.example's placeholder: ./setup.sh update writes a new one, or see docs/install/accounts.md`,
    );
  }
  return value;
}

/** LIFE_OS_AUTH_SECRET: not the placeholder, and long enough to be a key. */
function authSecret(source: Source): string {
  const value = notPlaceholder(source, "LIFE_OS_AUTH_SECRET");
  if (Buffer.byteLength(value, "utf8") < AUTH_SECRET_MIN_BYTES) {
    throw new Error(
      `LIFE_OS_AUTH_SECRET is shorter than ${AUTH_SECRET_MIN_BYTES} bytes: make one with openssl rand -hex 32 (see docs/install/accounts.md). A new secret signs everyone out and turns two-factor sign-in off for every account.`,
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
    secret: authSecret(source),
    signingKey: notPlaceholder(source, "LIFE_OS_IDENTITY_SIGNING_KEY"),
    trustedKeys: notPlaceholder(source, "LIFE_OS_TRUSTED_KEYS"),
    publicUrl,
    insecureLoginHosts: hostList(source.LIFE_OS_INSECURE_LOGIN_HOSTS),
    legacyAccessDefault: onOff(source.LIFE_OS_LEGACY_ACCESS, false),
  };
}
