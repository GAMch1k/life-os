import { apiKey } from "@better-auth/api-key";
import { oauthProvider } from "@better-auth/oauth-provider";
import { passkey } from "@better-auth/passkey";
import { betterAuth, type BetterAuthOptions } from "better-auth";
import {
  APIError,
  createAuthMiddleware,
  getSessionFromCtx,
} from "better-auth/api";
import { admin, jwt, twoFactor, username } from "better-auth/plugins";
import { createAccessControl } from "better-auth/plugins/access";
import { randomBytes } from "node:crypto";
import { Pool } from "pg";

import {
  COOKIE_PREFIX,
  FIRST_USER_ID,
  INSECURE_SESSION_COOKIE,
  KEY_NAME_MAX,
  KEY_RANDOM_LENGTH,
  MCP_ACCESS_TOKEN_SECONDS,
  MCP_RESOURCE_PATH,
  OAUTH_CONSENT_PAGE,
  OAUTH_SCOPES,
  PLACEHOLDER_EMAIL_DOMAIN,
  SESSION_COOKIE,
  SESSION_EXPIRES_SECONDS,
  SESSION_FRESH_SECONDS,
  SESSION_UPDATE_SECONDS,
  SETUP_CODE_HEADER,
  TOKEN_MAX_DAYS,
} from "./constants";
import type { AuthEnv } from "./env";
import {
  anonymousWriteAllowed,
  isLoopbackHost,
  isOwnOrigin,
  requestHost,
} from "./origin";
import { hashPassword, verifyPassword } from "./password";
import { hasSecondFactor, isFresh } from "./roles";
import {
  beforeSecurityHook,
  isAdminAction,
  securityLogPlugin,
} from "./security-hooks";
import { recordSecurityEvent } from "./security-log";
import {
  accountCount,
  claimSetupCode,
  noteSetupCodeUser,
  setupCodeValid,
} from "./store";
import { LoginThrottle } from "./throttle";

/**
 * Better Auth inside the Dashboard (docs/architecture/multi-user.md): its
 * own database, auth_db, whose schema is our migrations
 * (apps/dashboard/db/migrations), never Better Auth's at start. Only the
 * plugins P21-03 needs: username, admin (without impersonation), two-factor
 * (TOTP and ten recovery codes), and passkeys. Nothing is keyed by email.
 */

// Server roles: the owner and admins manage accounts, never content, and
// nobody impersonates anyone (the permission is in no role, and the
// endpoints are off as well).
const statements = {
  user: [
    "create",
    "list",
    "set-role",
    "ban",
    "impersonate",
    "impersonate-admins",
    "delete",
    "set-password",
    "set-email",
    "set-phone",
    "get",
    "update",
  ],
  session: ["list", "revoke", "delete"],
} as const;
const ac = createAccessControl(statements);
const manageAccounts = {
  user: [
    "list",
    "set-role",
    "ban",
    "delete",
    "set-password",
    "get",
    "update",
  ] as Array<(typeof statements.user)[number]>,
  session: ["list", "revoke", "delete"] as Array<
    (typeof statements.session)[number]
  >,
};
const roles = {
  owner: ac.newRole(manageAccounts),
  admin: ac.newRole(manageAccounts),
  member: ac.newRole({ user: [], session: [] }),
};

/** Endpoints Life OS does not offer: email, social, impersonation, and,
 * until P21-07, a second account. Better Auth compares these with the
 * request's path, so a route with a parameter (/reset-password/:token) is
 * refused in the before hook instead. */
export const DISABLED_PATHS = [
  "/sign-in/email",
  "/sign-in/social",
  "/request-password-reset",
  "/send-verification-email",
  "/verify-email",
  "/change-email",
  "/delete-user",
  "/delete-user/callback",
  "/is-username-available",
  "/link-social",
  "/unlink-account",
  "/list-accounts",
  "/account-info",
  "/get-access-token",
  "/refresh-token",
  "/admin/create-user",
  "/admin/impersonate-user",
  "/admin/stop-impersonating",
  "/admin/set-user-password",
  "/admin/update-user",
  // Signing out every session is Life OS's own "Sign out everywhere"
  // (/api/account/sign-out-everywhere), which ends the devices, tokens, and
  // connected apps in the same transaction.
  "/revoke-sessions",
  // API keys: device keys and personal tokens are made, listed, and revoked
  // through Life OS's own endpoints (/api/auth/devices/, /api/account/),
  // which set their kind and scopes; Better Auth's would let anyone signed
  // in make a key of any kind.
  "/api-key/create",
  "/api-key/verify",
  "/api-key/get",
  "/api-key/update",
  "/api-key/delete",
  "/api-key/list",
  "/api-key/delete-all-expired-api-keys",
  // The jwt plugin signs MCP's access tokens only, never a session's.
  "/token",
  // The OAuth provider serves MCP clients: registration, authorization,
  // consent, tokens, and revocation. Clients and grants are managed in
  // Settings → Connected apps; nothing OpenID Connect (no openid scope).
  "/oauth2/create-client",
  "/oauth2/get-client",
  "/oauth2/get-clients",
  "/oauth2/update-client",
  "/oauth2/delete-client",
  "/oauth2/client/rotate-secret",
  "/oauth2/client/rotate-keys",
  "/oauth2/get-consent",
  "/oauth2/get-consents",
  "/oauth2/update-consent",
  "/oauth2/delete-consent",
  "/oauth2/introspect",
  "/oauth2/userinfo",
  "/oauth2/continue",
  "/oauth2/public-client-prelogin",
  "/oauth2/end-session",
];

/** Paths refused by prefix in the before hook (they have parameters). */
const DISABLED_PREFIXES = ["/admin/oauth2/", "/oauth2/end-session/"];

const SIGN_IN_PATHS = new Set(["/sign-in/username"]);

const SETUP_CODE_INVALID_MESSAGE =
  "That setup code is wrong, used, or expired. Print a new one with ./setup.sh setup-code.";

export type AuthKind = "secure" | "insecure";

export type LifeOsAuth = ReturnType<typeof createAuth>;

function clientAddress(headers: Headers | undefined) {
  const value =
    headers?.get("x-real-ip") ?? headers?.get("x-forwarded-for") ?? "";
  return value.split(",")[0].trim() || "unknown";
}

function placeholderEmail() {
  return `u-${randomBytes(9).toString("hex")}@${PLACEHOLDER_EMAIL_DOMAIN}`;
}

/** Whether a host may sign in over plain HTTP (LIFE_OS_INSECURE_LOGIN_HOSTS). */
export function insecureLoginAllowed(env: AuthEnv, headers: Headers) {
  return env.insecureLoginHosts.includes(requestHost(headers));
}

/**
 * Whether the request's page is somewhere a login works: HTTPS, a loopback
 * host, or a host the owner allowed over plain HTTP. The browser sends
 * Origin with every sign-in.
 */
function loginOriginAllowed(env: AuthEnv, kind: AuthKind, headers: Headers) {
  const origin = headers.get("origin");
  if (!origin || origin === "null") return true;
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    return false;
  }
  if (url.protocol === "https:" || isLoopbackHost(url.hostname)) {
    return kind === "secure";
  }
  return kind === "insecure" && insecureLoginAllowed(env, headers);
}

/** The MCP resource OAuth access tokens are bound to. */
export function mcpResource(env: Pick<AuthEnv, "publicUrl">) {
  return `${env.publicUrl}${MCP_RESOURCE_PATH}`;
}

/** Loopback hosts, as a URL's hostname spells them. */
const LOOPBACK_REDIRECT_HOSTS = new Set(["127.0.0.1", "localhost", "[::1]"]);

/**
 * The OAuth provider for MCP clients
 * (docs/architecture/multi-user.md#devices-tokens-mcp): dynamic client
 * registration, the consent page in the Dashboard, PKCE (required for every
 * public client), 15-minute JWT access tokens bound to <public URL>/api/mcp,
 * and rotating refresh tokens (offline_access). Plain-HTTP redirect URIs
 * only on loopback hosts.
 */
function oauthOptions(env: AuthEnv) {
  const resource = mcpResource(env);
  return {
    scopes: OAUTH_SCOPES,
    clientRegistrationDefaultScopes: OAUTH_SCOPES,
    allowDynamicClientRegistration: true,
    allowUnauthenticatedClientRegistration: true,
    // Native MCP clients (Claude Code, Cursor) often omit application_type
    // and use a loopback callback: treat them as native.
    clientRegistrationDefaultApplicationType: "infer" as const,
    allowInsecureRedirectUri: (url: URL) =>
      url.protocol === "http:" && LOOPBACK_REDIRECT_HOSTS.has(url.hostname),
    grantTypes: ["authorization_code" as const, "refresh_token" as const],
    resources: [
      { identifier: resource, accessTokenTtl: MCP_ACCESS_TOKEN_SECONDS },
    ],
    clientRegistrationDefaultResources: [resource],
    clientRegistrationAllowedResources: [resource],
    accessTokenExpiresIn: MCP_ACCESS_TOKEN_SECONDS,
    refreshTokenReuseInterval: 0,
    loginPage: "/login",
    consentPage: OAUTH_CONSENT_PAGE,
  };
}

export function createAuth(
  env: AuthEnv,
  pool: Pool,
  options: {
    kind?: AuthKind;
    throttle?: LoginThrottle;
    rateLimit?: boolean;
  } = {},
) {
  const kind = options.kind ?? "secure";
  const throttle = options.throttle ?? new LoginThrottle();
  const publicUrl = new URL(env.publicUrl);
  const secure = kind === "secure";

  const config = {
    appName: "Life OS",
    baseURL: env.publicUrl,
    basePath: "/api/auth",
    secret: env.secret,
    database: pool,
    telemetry: { enabled: false },
    disabledPaths: DISABLED_PATHS,
    // A write with the cookie must come from Life OS's own origin: the
    // public URL, or the host the request names (the gateway serves only
    // its own hosts).
    trustedOrigins: (request?: Request) => {
      const origin = request?.headers.get("origin");
      return origin && request && isOwnOrigin(request.headers, origin)
        ? [origin]
        : [];
    },
    emailAndPassword: {
      enabled: true,
      autoSignIn: true,
      minPasswordLength: 10,
      maxPasswordLength: 256,
      revokeSessionsOnPasswordReset: true,
      password: { hash: hashPassword, verify: verifyPassword },
    },
    session: {
      expiresIn: SESSION_EXPIRES_SECONDS,
      updateAge: SESSION_UPDATE_SECONDS,
      freshAge: SESSION_FRESH_SECONDS,
      // Off: the check reads the session from the database on every
      // request, so a ban or a sign-out counts at once.
      cookieCache: { enabled: false },
      additionalFields: {
        // Whether the session signed in with a second factor (an
        // authenticator code, a recovery code, or a passkey), or proved
        // one while signed in (turning on the authenticator app, adding a
        // passkey): set by the security log's after hook
        // (src/lib/auth/security-hooks.ts), never by a client. The
        // identity token's mfa claim, which the services' admin routes
        // require (docs/architecture/multi-user.md#the-identity-token).
        secondFactor: {
          type: "boolean",
          required: false,
          defaultValue: false,
          input: false,
        },
      },
    },
    user: {
      additionalFields: {
        // Bumped (by a trigger in auth_db) by every change an identity
        // token reflects; the check re-mints a token when it moved.
        authzVersion: {
          type: "number",
          required: false,
          defaultValue: 1,
          input: false,
        },
      },
    },
    verification: { storeIdentifier: "hashed" },
    rateLimit: {
      enabled: options.rateLimit ?? true,
      storage: "memory",
      window: 60,
      max: 120,
      customRules: {
        "/two-factor/*": { window: 60, max: 10 },
        "/passkey/verify-authentication": { window: 60, max: 10 },
        "/reset-password": { window: 60, max: 5 },
        // An MCP client registers once and then refreshes its token every
        // 15 minutes; the OAuth provider's own limits are tighter still.
        "/oauth2/register": { window: 60, max: 5 },
        "/oauth2/token": { window: 60, max: 30 },
      },
    },
    advanced: {
      cookiePrefix: COOKIE_PREFIX,
      // The names are set here, so Better Auth adds no __Secure- prefix;
      // every cookie is Secure on HTTPS and localhost all the same.
      useSecureCookies: false,
      defaultCookieAttributes: {
        secure,
        sameSite: "lax",
        httpOnly: true,
        path: "/",
      },
      cookies: {
        session_token: {
          name: secure ? SESSION_COOKIE : INSECURE_SESSION_COOKIE,
        },
      },
      database: { generateId: "uuid" },
      ipAddress: { ipAddressHeaders: ["x-real-ip", "x-forwarded-for"] },
    },
    databaseHooks: {
      user: {
        create: {
          // The first account gets the fixed id and is the server owner.
          // Until invites (P21-07) there is no other. The setup code is
          // used up here, in one UPDATE, right before the insert (this
          // hook runs after the plugins' and after every check of the
          // sign-up's input): of two sign-ups racing with one code, only
          // one gets this far, and the fixed id's primary key would stop
          // a second insert all the same.
          before: async (
            user: Record<string, unknown>,
            context: { headers?: Headers } | null,
          ) => {
            if ((await accountCount(pool)) > 0) {
              throw new APIError("FORBIDDEN", {
                code: "REGISTRATION_CLOSED",
                message: "New accounts need an invite.",
              });
            }
            const code = context?.headers?.get(SETUP_CODE_HEADER) ?? "";
            if (!(await claimSetupCode(pool, code))) {
              throw new APIError("FORBIDDEN", {
                code: "SETUP_CODE_INVALID",
                message: SETUP_CODE_INVALID_MESSAGE,
              });
            }
            return { data: { ...user, id: FIRST_USER_ID, role: "owner" } };
          },
        },
      },
    },
    hooks: {
      before: createAuthMiddleware(async (ctx) => {
        const headers = ctx.headers ?? new Headers();
        const path = ctx.path;

        // Better Auth's disabledPaths compares the request's path, so the
        // pattern /reset-password/:token there never matches: the route is
        // matched here (a reset link is /reset/<token>, a Dashboard page).
        if (path === "/reset-password/:token") {
          throw new APIError("NOT_FOUND", { message: "Not found" });
        }
        if (DISABLED_PREFIXES.some((prefix) => path.startsWith(prefix))) {
          throw new APIError("NOT_FOUND", { message: "Not found" });
        }
        // MCP's OAuth runs at the public address only, never on a
        // plain-HTTP host the owner allowed for signing in.
        if (!secure && path.startsWith("/oauth2/")) {
          throw new APIError("NOT_FOUND", { message: "Not found" });
        }

        // Every write from a browser must come from Life OS's own origin,
        // with or without a cookie: a form on another site must not sign
        // anyone in (or out) either. Better Auth checks only requests that
        // carry its cookies.
        if (
          ctx.request &&
          !anonymousWriteAllowed(ctx.request.method, headers)
        ) {
          throw new APIError("FORBIDDEN", {
            code: "FORBIDDEN_ORIGIN",
            message: "This request did not come from Life OS.",
          });
        }

        // Who makes the request, for the security log's entry afterwards.
        await beforeSecurityHook(ctx);

        if (path === "/sign-up/email" || SIGN_IN_PATHS.has(path)) {
          if (!loginOriginAllowed(env, kind, headers)) {
            throw new APIError("FORBIDDEN", {
              code: "INSECURE_ORIGIN",
              message:
                "Sign in at Life OS's HTTPS address: this one is plain HTTP.",
            });
          }
        }

        // The first account needs the one-time setup code; every other
        // account an invite, and invites open only in P21-07.
        if (path === "/sign-up/email") {
          if ((await accountCount(pool)) > 0) {
            throw new APIError("FORBIDDEN", {
              code: "REGISTRATION_CLOSED",
              message: "New accounts need an invite.",
            });
          }
          // A first look, for a clear answer before anything else; the
          // code is used up in the user's create hook, right before the
          // insert, so that a sign-up that fails on the way (a short
          // password, say) leaves it working.
          const code = headers.get(SETUP_CODE_HEADER) ?? "";
          if (!(await setupCodeValid(pool, code))) {
            await recordSecurityEvent(pool, {
              kind: "setup_code_refused",
              outcome: "refused",
              headers,
            });
            throw new APIError("FORBIDDEN", {
              code: "SETUP_CODE_INVALID",
              message: SETUP_CODE_INVALID_MESSAGE,
            });
          }
          const body = (ctx.body ?? {}) as Record<string, unknown>;
          const email =
            typeof body.email === "string" && body.email.trim()
              ? body.email.trim()
              : placeholderEmail();
          return {
            context: {
              body: {
                ...body,
                email,
                name:
                  typeof body.name === "string" && body.name.trim()
                    ? body.name
                    : body.username,
              },
            },
          };
        }

        if (SIGN_IN_PATHS.has(path)) {
          const name = String(
            (ctx.body as Record<string, unknown> | undefined)?.username ?? "",
          );
          const wait = throttle.waitMs(name, clientAddress(headers));
          // Not in the security log: the failures that led here are, and an
          // entry per refused retry would let one address flood it.
          if (wait > 0) {
            throw new APIError("TOO_MANY_REQUESTS", {
              code: "TOO_MANY_FAILED_SIGN_INS",
              message: `Too many failed sign-ins. Try again in ${Math.ceil(wait / 1000)} s.`,
            });
          }
        }

        // The admin endpoints: the owner and admins, with a second factor
        // (TOTP or a passkey) and a session younger than 12 hours. The
        // admin surfaces themselves are P21-04's.
        if (path.startsWith("/admin/")) {
          // Before hooks run ahead of the endpoint's own session lookup.
          const session = await getSessionFromCtx(ctx);
          const user = session?.user as
            { id: string; role?: string | null } | undefined;
          if (!session || !user) return;
          if (user.role !== "owner" && user.role !== "admin") return;
          // A refused admin action goes into the security log (reads such
          // as the users list do not).
          const refuse = async (
            status: "FORBIDDEN" | "BAD_REQUEST",
            code: string,
            message: string,
          ): Promise<never> => {
            if (isAdminAction(path)) {
              await recordSecurityEvent(pool, {
                kind: "admin_refused",
                outcome: "refused",
                actorUserId: user.id,
                via: "session",
                headers,
                detail: `${path.slice("/admin/".length)}:${code}`,
              });
            }
            throw new APIError(status, { code, message });
          };
          if (!(await hasSecondFactor(pool, user.id))) {
            await refuse(
              "FORBIDDEN",
              "SECOND_FACTOR_REQUIRED",
              "Admin actions need two-factor sign-in: turn on an authenticator app or add a passkey in Settings → Security.",
            );
          }
          // And the session signed in with it (the identity token's mfa).
          if (
            (session.session as { secondFactor?: boolean | null })
              .secondFactor !== true
          ) {
            await refuse(
              "FORBIDDEN",
              "SECOND_FACTOR_SIGN_IN_REQUIRED",
              "Admin actions need a sign-in with your authenticator app or a passkey: sign out, and sign in again with it.",
            );
          }
          if (!isFresh(session.session)) {
            await refuse(
              "FORBIDDEN",
              "SESSION_NOT_FRESH",
              "Sign in again to do this.",
            );
          }
          const body = (ctx.body ?? {}) as Record<string, unknown>;
          const target = typeof body.userId === "string" ? body.userId : "";
          if (path === "/admin/set-role") {
            if (user.role !== "owner") {
              await refuse(
                "FORBIDDEN",
                "OWNER_ONLY",
                "Only the server owner changes roles.",
              );
            }
            if (body.role !== "admin" && body.role !== "member") {
              await refuse(
                "BAD_REQUEST",
                "ROLE_INVALID",
                "The role is admin or member.",
              );
            }
            if (target === user.id) {
              await refuse(
                "FORBIDDEN",
                "OWN_ROLE",
                "You cannot change your own role.",
              );
            }
          }
          if (
            (path === "/admin/ban-user" || path === "/admin/remove-user") &&
            target === FIRST_USER_ID
          ) {
            await refuse(
              "FORBIDDEN",
              "OWNER_PROTECTED",
              "The server owner cannot be disabled or deleted.",
            );
          }
        }
      }),
      after: createAuthMiddleware(async (ctx) => {
        const headers = ctx.headers ?? new Headers();
        const returned = ctx.context.returned;
        const failed = returned instanceof APIError;

        if (ctx.path === "/sign-up/email" && !failed) {
          const userId = ctx.context.newSession?.user.id;
          if (userId) {
            await noteSetupCodeUser(
              pool,
              headers.get(SETUP_CODE_HEADER) ?? "",
              userId,
            );
          }
        }

        if (SIGN_IN_PATHS.has(ctx.path)) {
          const name = String(
            (ctx.body as Record<string, unknown> | undefined)?.username ?? "",
          );
          const address = clientAddress(headers);
          if (failed && (returned as APIError).statusCode === 401) {
            throttle.failed(name, address);
          }
          if (!failed) throttle.succeeded(name, address);
        }
      }),
    },
    plugins: [
      username(),
      admin({
        ac,
        roles,
        defaultRole: "member",
        adminRoles: ["owner", "admin"],
        bannedUserMessage: "This account is disabled.",
      }),
      twoFactor({
        issuer: "Life OS",
        backupCodeOptions: { amount: 10, length: 10 },
      }),
      ...(secure
        ? [
            passkey({
              rpID: publicUrl.hostname,
              rpName: "Life OS",
              origin: publicUrl.origin,
            }),
          ]
        : []),
      // Device keys and personal API tokens (src/lib/auth/keys.ts): stored
      // hashed, shown once, told apart by their prefix and metadata. Made
      // only through Life OS's own endpoints; the check verifies them.
      apiKey({
        enableMetadata: true,
        requireName: true,
        maximumNameLength: KEY_NAME_MAX,
        defaultKeyLength: KEY_RANDOM_LENGTH,
        // The plugin's own counters would write the key's row on every
        // request; the check notes the last use once a minute instead.
        rateLimit: { enabled: false },
        keyExpiration: {
          defaultExpiresIn: null,
          minExpiresIn: 1,
          maxExpiresIn: TOKEN_MAX_DAYS + 1,
        },
        startingCharactersConfig: { shouldStore: true, charactersLength: 11 },
      }),
      // Only for the MCP clients' OAuth access tokens, which only the
      // Dashboard verifies (identity tokens are Life OS's own, pinned keys).
      jwt({
        jwt: { issuer: env.publicUrl },
        disableSettingJwtHeader: true,
      }),
      oauthProvider(oauthOptions(env)),
      // Last: its after hook runs after the plugins', when two-factor has
      // turned a password sign-in into a challenge (and no session).
      securityLogPlugin(pool),
    ],
  } satisfies BetterAuthOptions;

  return betterAuth(config);
}
