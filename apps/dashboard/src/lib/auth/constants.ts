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
 * Whether phones and watches can be paired with an account (P21-06). Until
 * they can, the phone and watch apps need legacy access, so the daily
 * reminder to turn it off (src/components/legacy-access-reminder.tsx) asks
 * for something the owner cannot do yet, and stays hidden. P21-06 sets this
 * to true when it ships device pairing. The switch in Settings → Security
 * works either way.
 */
export const DEVICE_PAIRING_AVAILABLE = false;

/** Password hashing: at most this many scrypt runs at once. */
export const PASSWORD_HASH_CONCURRENCY = 2;
