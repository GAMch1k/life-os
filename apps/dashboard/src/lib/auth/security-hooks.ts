import type { BetterAuthPlugin } from "better-auth";
import {
  APIError,
  createAuthMiddleware,
  getSessionFromCtx,
} from "better-auth/api";
import type { Pool } from "pg";

import { revokeCredentials } from "./keys";
import {
  recordFailedSignIn,
  recordSecurityEvent,
  type SecurityEventKind,
  type SecurityOutcome,
} from "./security-log";

/** The scopes a consent gave, as a code: read, write, or read_write. */
function consentDetail(scope: unknown): string {
  const scopes =
    typeof scope === "string" ? scope.split(" ") : ["mcp:read", "mcp:write"];
  const read = scopes.includes("mcp:read");
  const write = scopes.includes("mcp:write");
  return read && write ? "mcp:read_write" : write ? "mcp:write" : "mcp:read";
}

/**
 * What Better Auth's endpoints write into the security log
 * (src/lib/auth/security-log.ts): Better Auth's before hook resolves who
 * makes the request (its session, or whom a reset link is for), and the
 * after hook records what happened with the outcome. Never a typed
 * username, password, or code.
 */

// The hooks' context, as far as these functions read it.
type HookContext = {
  path: string;
  body?: unknown;
  headers?: Headers;
  getSignedCookie?: (
    name: string,
    secret: string,
  ) => Promise<string | false | null>;
  context: {
    returned?: unknown;
    newSession?: { user: { id: string }; session: { id: string } } | null;
    secret?: string;
    createAuthCookie?: (name: string) => { name: string };
    internalAdapter?: {
      findVerificationValue: (
        identifier: string,
      ) => Promise<{ value: string } | null>;
    };
  };
};

/** The user (and session) a request was made by, before the endpoint ran. */
const before = new WeakMap<
  object,
  { userId: string | null; sessionId?: string | null }
>();

/** Endpoints whose actor is the signed-in user. */
const SESSION_PATHS = new Set([
  "/sign-out",
  "/change-password",
  "/revoke-session",
  "/revoke-sessions",
  "/revoke-other-sessions",
  "/two-factor/verify-totp",
  "/two-factor/verify-backup-code",
  "/two-factor/disable",
  "/two-factor/generate-backup-codes",
  "/passkey/verify-registration",
  "/passkey/delete-passkey",
  "/admin/set-role",
  "/admin/ban-user",
  "/admin/unban-user",
  "/admin/remove-user",
  "/admin/revoke-user-session",
  "/admin/revoke-user-sessions",
  "/oauth2/consent",
  "/oauth2/register",
]);

/** Admin endpoints that change something, with their entry. */
const ADMIN_ACTIONS: Record<string, SecurityEventKind> = {
  "/admin/set-role": "role_changed",
  "/admin/ban-user": "user_banned",
  "/admin/unban-user": "user_unbanned",
  "/admin/remove-user": "user_deleted",
  "/admin/revoke-user-session": "user_sessions_revoked",
  "/admin/revoke-user-sessions": "user_sessions_revoked",
};

function bodyOf(ctx: HookContext) {
  return (ctx.body ?? {}) as Record<string, unknown>;
}

function failure(returned: unknown): {
  outcome: SecurityOutcome;
  code: string;
} | null {
  if (!(returned instanceof APIError)) return null;
  const body = (returned.body ?? {}) as { code?: unknown };
  const code =
    typeof body.code === "string" && body.code
      ? body.code
      : String(returned.status ?? returned.statusCode);
  return {
    outcome: returned.statusCode === 403 ? "refused" : "failed",
    code,
  };
}

/** Before the endpoint: who makes the request, for the after hook. */
export async function beforeSecurityHook(ctx: HookContext) {
  if (ctx.path === "/reset-password") {
    // Whom a reset link is for: the token's verification names the user
    // (Better Auth deletes it once the password is set).
    const token = bodyOf(ctx).token;
    if (typeof token === "string" && token && ctx.context.internalAdapter) {
      const found = await ctx.context.internalAdapter
        .findVerificationValue(`reset-password:${token}`)
        .catch(() => null);
      before.set(ctx.context, { userId: found?.value ?? null });
    }
    return;
  }
  if (!SESSION_PATHS.has(ctx.path)) return;
  const session = (await getSessionFromCtx(
    ctx as unknown as Parameters<typeof getSessionFromCtx>[0],
  ).catch(() => null)) as {
    user?: { id: string };
    session?: { id: string };
  } | null;
  before.set(ctx.context, {
    userId: session?.user?.id ?? null,
    sessionId: session?.session?.id ?? null,
  });
}

/**
 * Marks a session as signed in with a second factor (the identity token's
 * mfa claim, docs/architecture/multi-user.md#the-identity-token): an
 * authenticator code, a recovery code, or a passkey at sign-in, or turning
 * on the authenticator app or adding a passkey while signed in, which
 * prove the factor as well.
 */
export async function markSecondFactor(
  pool: Pool,
  sessionId: string | null | undefined,
) {
  if (!sessionId) return;
  await pool
    .query(`UPDATE session SET "secondFactor" = true WHERE id = $1`, [
      sessionId,
    ])
    .catch((error) =>
      console.error(
        "sign-in: the second factor was not noted",
        error instanceof Error ? error.message : error,
      ),
    );
}

/** The user whose sign-in waits for a second factor (the two_factor cookie). */
async function pendingTwoFactorUser(ctx: HookContext) {
  try {
    if (!ctx.context.createAuthCookie || !ctx.getSignedCookie) return null;
    const cookie = ctx.context.createAuthCookie("two_factor");
    const signed = await ctx.getSignedCookie(
      cookie.name,
      ctx.context.secret ?? "",
    );
    if (!signed || !ctx.context.internalAdapter) return null;
    const found =
      await ctx.context.internalAdapter.findVerificationValue(signed);
    return found?.value ?? null;
  } catch {
    return null;
  }
}

/** Whether an admin endpoint changes something (and is logged). */
export function isAdminAction(path: string) {
  return path in ADMIN_ACTIONS;
}

/** The account a typed username names, if any (the name is never kept). */
export async function userByUsername(pool: Pool, value: unknown) {
  if (typeof value !== "string" || !value.trim()) return null;
  const { rows } = await pool
    .query<{ id: string }>('SELECT id FROM "user" WHERE username = lower($1)', [
      value.trim(),
    ])
    .catch(() => ({ rows: [] as { id: string }[] }));
  return rows[0]?.id ?? null;
}

/**
 * The after hook as a plugin of its own, listed last: Better Auth runs the
 * options' after hook before the plugins', and only after the two-factor
 * plugin's has a password sign-in that needs a code no session.
 */
export function securityLogPlugin(pool: Pool) {
  return {
    id: "lifeos-security-log",
    hooks: {
      after: [
        {
          matcher: () => true,
          handler: createAuthMiddleware(async (ctx) => {
            await afterSecurityHook(ctx as unknown as HookContext, pool);
          }),
        },
      ],
    },
  } satisfies BetterAuthPlugin;
}

/** After the endpoint: records what happened. */
export async function afterSecurityHook(ctx: HookContext, pool: Pool) {
  const headers = ctx.headers;
  const returned = ctx.context.returned;
  const failed = failure(returned);
  const actor = before.get(ctx.context)?.userId ?? null;
  const signedIn = ctx.context.newSession?.user.id ?? null;
  const record = (
    kind: SecurityEventKind,
    fields: {
      actor?: string | null;
      subject?: string | null;
      detail?: string | null;
      outcome?: SecurityOutcome;
    } = {},
  ) =>
    recordSecurityEvent(pool, {
      kind,
      outcome: fields.outcome ?? failed?.outcome ?? "ok",
      actorUserId: fields.actor ?? null,
      subjectUserId: fields.subject ?? null,
      via: fields.actor ? "session" : null,
      headers,
      detail: fields.detail ?? null,
    });

  switch (ctx.path) {
    case "/sign-up/email": {
      if (!failed && signedIn) {
        await record("account_created", {
          actor: signedIn,
          subject: signedIn,
          detail: "setup_code",
        });
      } else if (failed?.code === "SETUP_CODE_INVALID") {
        await record("setup_code_refused", { outcome: "refused" });
      }
      return;
    }
    case "/sign-in/username": {
      if (failed) {
        await recordFailedSignIn(pool, {
          subjectUserId: await userByUsername(pool, bodyOf(ctx).username),
          headers,
          detail: `password:${failed.code}`,
        });
      } else if (signedIn) {
        await record("sign_in", {
          actor: signedIn,
          subject: signedIn,
          detail: "password",
        });
      }
      // A two-factor sign-in is recorded once the second factor is given.
      return;
    }
    case "/two-factor/verify-totp":
    case "/two-factor/verify-backup-code": {
      const method =
        ctx.path === "/two-factor/verify-totp" ? "totp" : "recovery_code";
      if (actor) {
        // Signed in already: turning two-factor on (the first code).
        if (!failed && ctx.path === "/two-factor/verify-totp") {
          const { rows } = await pool
            .query<{ enabled: boolean | null }>(
              'SELECT "twoFactorEnabled" AS enabled FROM "user" WHERE id = $1',
              [actor],
            )
            .catch(() => ({ rows: [] as { enabled: boolean | null }[] }));
          if (rows[0]?.enabled && signedIn) {
            // The first code proves the authenticator: the session that
            // turned it on (Better Auth replaces it with a new one) counts
            // as signed in with it.
            await markSecondFactor(
              pool,
              ctx.context.newSession?.session.id ??
                before.get(ctx.context)?.sessionId,
            );
            await record("two_factor_enabled", {
              actor,
              subject: actor,
              detail: "totp",
            });
          }
        }
        return;
      }
      if (failed) {
        await recordFailedSignIn(pool, {
          subjectUserId: await pendingTwoFactorUser(ctx),
          headers,
          detail: `${method}:${failed.code}`,
        });
      } else if (signedIn) {
        await markSecondFactor(pool, ctx.context.newSession?.session.id);
        await record("sign_in", {
          actor: signedIn,
          subject: signedIn,
          detail: method,
        });
      }
      return;
    }
    case "/passkey/verify-authentication": {
      if (failed) {
        await recordFailedSignIn(pool, {
          headers,
          detail: `passkey:${failed.code}`,
        });
      } else if (signedIn) {
        await markSecondFactor(pool, ctx.context.newSession?.session.id);
        await record("sign_in", {
          actor: signedIn,
          subject: signedIn,
          detail: "passkey",
        });
      }
      return;
    }
    case "/oauth2/register":
      // An MCP client registered itself (dynamic client registration):
      // nothing of what it sent (its name, its addresses) is kept here.
      if (!failed) {
        await record("oauth_client_registered", {
          actor,
          subject: actor,
          detail: "dynamic",
        });
      }
      return;
    case "/oauth2/consent":
      if (actor && !failed && bodyOf(ctx).accept === true) {
        await record("oauth_consent_given", {
          actor,
          subject: actor,
          detail: consentDetail(bodyOf(ctx).scope),
        });
      }
      return;
    case "/sign-out":
      if (actor && !failed) {
        await record("sign_out", { actor, subject: actor });
      }
      return;
    case "/change-password":
      if (actor) {
        await record("password_changed", {
          actor,
          subject: actor,
          detail: failed?.code ?? null,
        });
      }
      return;
    case "/reset-password": {
      const subject = before.get(ctx.context)?.userId ?? null;
      if (subject || !failed) {
        await record("password_reset", {
          subject,
          detail: failed?.code ?? "reset_link",
        });
      }
      return;
    }
    case "/revoke-session":
    case "/revoke-sessions":
    case "/revoke-other-sessions":
      if (actor && !failed) {
        await record("sessions_revoked", {
          actor,
          subject: actor,
          detail:
            ctx.path === "/revoke-session"
              ? "one"
              : ctx.path === "/revoke-sessions"
                ? "all"
                : "others",
        });
      }
      return;
    case "/two-factor/disable":
      if (actor) {
        await record("two_factor_disabled", {
          actor,
          subject: actor,
          detail: failed?.code ?? null,
        });
      }
      return;
    case "/two-factor/generate-backup-codes":
      if (actor && !failed) {
        await record("recovery_codes_created", { actor, subject: actor });
      }
      return;
    case "/passkey/verify-registration":
      if (actor) {
        if (!failed) {
          await markSecondFactor(pool, before.get(ctx.context)?.sessionId);
        }
        await record("passkey_added", {
          actor,
          subject: actor,
          detail: failed?.code ?? null,
        });
      }
      return;
    case "/passkey/delete-passkey":
      if (actor && !failed) {
        await record("passkey_removed", { actor, subject: actor });
      }
      return;
  }

  const adminKind = ADMIN_ACTIONS[ctx.path];
  if (adminKind && actor) {
    const body = bodyOf(ctx);
    const target = typeof body.userId === "string" ? body.userId : null;
    let detail: string | null = failed?.code ?? null;
    if (
      !failed &&
      adminKind === "role_changed" &&
      typeof body.role === "string"
    ) {
      detail = body.role;
    }
    // Signing an account out, and disabling it, end everything it signed
    // in with: sessions (Better Auth's endpoint did), device keys, tokens,
    // and connected apps (docs/architecture/multi-user.md#devices-tokens-mcp).
    if (
      !failed &&
      target &&
      (ctx.path === "/admin/revoke-user-sessions" ||
        ctx.path === "/admin/ban-user")
    ) {
      await revokeCredentials(pool, target).catch((error) =>
        console.error(
          "admin: the account's keys were not revoked",
          error instanceof Error ? error.message : error,
        ),
      );
    }
    await record(adminKind, { actor, subject: target, detail });
  }
}
