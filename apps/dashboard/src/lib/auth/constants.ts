/**
 * Fixed names and values of Life OS's accounts (docs/architecture/multi-user.md).
 * Nothing here reads the environment, so the browser may import it too.
 */

/**
 * The first account on every server, and its personal space: fixed, so that
 * the services' migrations can give the single-user data an owner without
 * reading auth_db (P21-07), and so that legacy access acts as this account.
 */
export const FIRST_USER_ID = "00000000-0000-4000-8000-000000000001";
export const FIRST_SPACE_ID = "00000000-0000-4000-8000-000000000002";

/** Server roles. Exactly one owner (a partial unique index in auth_db). */
export const SERVER_ROLES = ["owner", "admin", "member"] as const;
export type ServerRole = (typeof SERVER_ROLES)[number];

/** The session cookie on HTTPS and on localhost: host-only and Secure. */
export const SESSION_COOKIE = "__Host-lifeos.session";
/**
 * The session cookie on a plain-HTTP host the owner allowed
 * (LIFE_OS_INSECURE_LOGIN_HOSTS): a browser refuses a Secure cookie there.
 */
export const INSECURE_SESSION_COOKIE = "lifeos.session";
/** The prefix of every other cookie Better Auth sets (two-factor, passkeys). */
export const COOKIE_PREFIX = "lifeos";

/** The one header a service reads to learn who calls (pkg/identity). */
export const IDENTITY_HEADER = "X-Life-OS-Identity";
export const IDENTITY_ISSUER = "lifeos-auth";
export const IDENTITY_AUDIENCE = "lifeos-services";
export const IDENTITY_TYPE = "lifeos-id+jwt";
/** Identity tokens live five minutes. */
export const IDENTITY_TTL_SECONDS = 300;
/**
 * The check hands out a cached token only while at least 4 minutes of it
 * are left, and mints a new one otherwise: the gateway asks the check
 * before it receives a request's body, and a service verifies the token
 * after, so a slow upload (a 25 MB voice note on a weak connection) needs a
 * young token. 4 minutes, plus pkg/identity's 30 s leeway, is what an
 * upload may take; the cache still saves most signing (a token serves the
 * first minute of its life).
 */
export const IDENTITY_REMINT_SECONDS = 240;
/** The check refuses to mint a token larger than this (nginx's header buffer). */
export const IDENTITY_MAX_BYTES = 2048;

/** Sessions last 30 days, renewed once a day while used. */
export const SESSION_EXPIRES_SECONDS = 30 * 24 * 60 * 60;
export const SESSION_UPDATE_SECONDS = 24 * 60 * 60;
/** Sensitive actions need a session younger than this. */
export const SESSION_FRESH_SECONDS = 12 * 60 * 60;

/** Setup codes are valid for 24 hours, once. */
export const SETUP_CODE_TTL_HOURS = 24;
/** The header the setup page sends the setup code in. */
export const SETUP_CODE_HEADER = "x-lifeos-setup-code";

/** Accounts without an email store one under .invalid, which never delivers. */
export const PLACEHOLDER_EMAIL_DOMAIN = "users.invalid";

/**
 * Whether phones and watches can be paired with an account. P21-06 ships
 * device pairing, so the daily reminder to turn legacy access off
 * (src/components/legacy-access-reminder.tsx) shows while legacy access is
 * on and an account exists. The switch in Settings → Security works either
 * way.
 */
export const DEVICE_PAIRING_AVAILABLE = true;

/**
 * Device keys and personal API tokens (docs/architecture/multi-user.md#devices-tokens-mcp):
 * Better Auth API keys, told apart by their prefix and their metadata.
 */
export const DEVICE_KEY_PREFIX = "lo_dev_";
export const TOKEN_PREFIX = "lo_tok_";
/** The random part of a key: 64 letters. */
export const KEY_RANDOM_LENGTH = 64;
/** A device's or token's name: 1 to 64 characters. */
export const KEY_NAME_MAX = 64;

/** A pairing code works once, for 5 minutes. */
export const PAIRING_CODE_TTL_SECONDS = 5 * 60;
/** An account has at most this many unused pairing codes. */
export const PAIRING_CODES_PER_ACCOUNT = 5;
/** The deep link the phone app opens: always this, never another address. */
export const PAIRING_LINK = "lifeos://pair";

/** Personal API tokens: 90 days by default, at most a year. */
export const TOKEN_DEFAULT_DAYS = 90;
export const TOKEN_MAX_DAYS = 365;

/**
 * The modules a personal token's scopes name (`<module>:read` or
 * `<module>:write`, which includes reading), and MCP grants cover.
 */
export const SCOPE_MODULES = [
  "tasks",
  "calendar",
  "inbox",
  "food",
  "finance",
  "voice",
  "notifications",
  "activity",
] as const;
export type ScopeModule = (typeof SCOPE_MODULES)[number];

/**
 * MCP over OAuth (docs/services/mcp.md#signing-in-with-oauth): the resource
 * the access tokens are bound to is <public URL>/api/mcp; consent offers
 * two scopes, and offline_access for the rotating refresh tokens.
 */
export const MCP_RESOURCE_PATH = "/api/mcp";
export const MCP_SCOPES = ["mcp:read", "mcp:write"] as const;
export const OAUTH_SCOPES = [...MCP_SCOPES, "offline_access"];
/** MCP access tokens live 15 minutes. */
export const MCP_ACCESS_TOKEN_SECONDS = 15 * 60;
/** Where the OAuth provider sends a person to consent. */
export const OAUTH_CONSENT_PAGE = "/oauth/consent";

/** Password hashing: at most this many scrypt runs at once. */
export const PASSWORD_HASH_CONCURRENCY = 2;
