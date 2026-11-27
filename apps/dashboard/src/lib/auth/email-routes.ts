import type { CheckRuntime, Principal } from "./check";
import {
  accountByLogin,
  accountEmail,
  createResetLink,
  createVerificationLink,
  emailPreferences,
  hasAuthenticator,
  LIMITS,
  normalizeAddress,
  RESET_LINK_MINUTES,
  setAccountEmail,
  setEmailPreferences,
  setSignInCodes,
  SIGN_IN_CODE_MINUTES,
  signInCodesOn,
  consumeVerificationLink,
  VERIFY_LINK_HOURS,
} from "./email-account";
import { ALERT_KINDS, type AlertKind } from "./email-kinds";
import {
  passwordResetMessage,
  signInCodeMessage,
  verifyEmailMessage,
} from "./email-templates";
import { anonymousWriteAllowed } from "./origin";
import {
  confirmPassword,
  mayAddSecondFactor,
  ReauthLimiter,
  recentlyAuthenticated,
} from "./reauth";
import { hasSecondFactor } from "./roles";
import { clientAddress, recordSecurityEvent } from "./security-log";
import { addressKey } from "./throttle";

/**
 * Life OS's own email endpoints (docs/install/email.md), next to Better
 * Auth's:
 *
 *   GET  /api/account/email                  the account's address and switches
 *   PUT  /api/account/email                  {address}: set or change it (null removes)
 *   POST /api/account/email/verification     mail a verification link
 *   PUT  /api/account/email/preferences      {alerts: {kind: on}}: the switches
 *   POST /api/account/email/sign-in-codes    {enabled, password}: the second factor
 *   POST /api/auth/email/forgot-password     {login}: mail a reset link (no sign-in)
 *   POST /api/auth/email/verify              {token}: use a verification link
 *
 * With email off on the server, they answer 404 (but verify: a link already
 * mailed still works).
 */

function json(status: number, body: unknown) {
  return Response.json(body, {
    status,
    headers: { "cache-control": "no-store" },
  });
}

function problem(status: number, code: string, message: string) {
  return json(status, { error: { code, message } });
}

async function body(request: Request): Promise<Record<string, unknown>> {
  const text = await request.text().catch(() => "");
  if (text.length > 4096) return {};
  try {
    const value = JSON.parse(text) as unknown;
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

const EMAIL_OFF = () =>
  problem(
    404,
    "email_off",
    "Email is not set up on this server: its owner turns it on with ./setup.sh email.",
  );

const LIMITER = Symbol.for("lifeos.auth.email-reauth-limiter");

/** Wrong re-entered passwords, per session (as the credential routes). */
function reauthLimiter(): ReauthLimiter {
  const holder = globalThis as { [LIMITER]?: ReauthLimiter };
  holder[LIMITER] ??= new ReauthLimiter();
  return holder[LIMITER];
}

export type EmailSettings = {
  /** Email works on this server. */
  enabled: boolean;
  address: string | null;
  verified: boolean;
  signInCodes: boolean;
  /** The account has a stronger second factor (authenticator, passkey). */
  strongFactor: boolean;
  authenticator: boolean;
  alerts: Record<AlertKind, boolean>;
};

export async function emailSettings(
  rt: CheckRuntime,
  userId: string,
): Promise<EmailSettings> {
  const [enabled, account, codes, strong, authenticator, alerts] =
    await Promise.all([
      rt.email?.enabled() ?? Promise.resolve(false),
      accountEmail(rt.pool, userId),
      signInCodesOn(rt.pool, userId),
      hasSecondFactor(rt.pool, userId),
      hasAuthenticator(rt.pool, userId),
      emailPreferences(rt.pool, userId),
    ]);
  return {
    enabled,
    address: account?.address ?? null,
    verified: account?.verified ?? false,
    signInCodes: codes,
    strongFactor: strong,
    authenticator,
    alerts,
  };
}

/** Mails a verification link to the account's address. */
export async function sendVerificationLink(
  rt: CheckRuntime,
  userId: string,
): Promise<Response> {
  const account = await accountEmail(rt.pool, userId);
  if (!account?.address) {
    return problem(400, "no_address", "Add your email address first.");
  }
  if (account.verified) {
    return problem(409, "already_verified", "This address is verified.");
  }
  const wait =
    rt.emailLimiter?.take(`verify:${userId}`, LIMITS.verifyLink) ?? 0;
  if (wait > 0) {
    return problem(
      429,
      "too_many_emails",
      `A link went out a moment ago. Try again in ${Math.ceil(wait / 1000)} s.`,
    );
  }
  const link = await createVerificationLink(
    rt.pool,
    { userId, address: account.address },
    rt.env.publicUrl,
  );
  const result = await rt.email!.send({
    to: account.address,
    ...verifyEmailMessage({
      publicUrl: rt.env.publicUrl,
      name: account.username ?? account.name,
      link,
      hours: VERIFY_LINK_HOURS,
    }),
    kind: "verify_email",
  });
  if (!result.ok) {
    return problem(
      502,
      result.code,
      result.code === "email_recipient_refused"
        ? "The mail server refused this address: check it, and change it if it is wrong."
        : "Life OS could not send the email now. Try again later; the server's admin sees why on the Status page.",
    );
  }
  return json(202, { sent: true, to: account.address });
}

/** /api/account/email…, for a principal signed in with a session; null otherwise. */
export async function emailRoute(
  rt: CheckRuntime,
  request: Request,
  principal: Principal,
  pathname: string,
): Promise<Response | null> {
  if (!pathname.startsWith("/api/account/email")) return null;
  const method = request.method;
  const userId = principal.userId;
  const enabled = (await rt.email?.enabled()) ?? false;

  if (pathname === "/api/account/email" && method === "GET") {
    return json(200, await emailSettings(rt, userId));
  }
  if (!enabled) return EMAIL_OFF();

  if (pathname === "/api/account/email" && method === "PUT") {
    if (
      !(await recentlyAuthenticated(rt.pool, {
        id: principal.sessionId,
        createdAt: principal.sessionCreatedAt,
      }))
    ) {
      return problem(
        403,
        "reauth_required",
        "Confirm your password first: you signed in more than 12 hours ago.",
      );
    }
    const input = await body(request);
    const address =
      input.address === null || input.address === ""
        ? null
        : normalizeAddress(input.address);
    if (address === null && input.address !== null && input.address !== "") {
      return problem(
        400,
        "bad_request",
        "That is not an email address. Enter one such as you@example.com.",
      );
    }
    if (await signInCodesOn(rt.pool, userId)) {
      return problem(
        409,
        "sign_in_codes_on",
        "Sign-in codes by email go to this address: turn them off in Settings → Security first.",
      );
    }
    const before = await accountEmail(rt.pool, userId);
    const result = await setAccountEmail(rt.pool, userId, address);
    if (result === "taken") {
      return problem(
        409,
        "address_taken",
        "Another account on this server has this address.",
      );
    }
    if (result === "ok") {
      await recordSecurityEvent(rt.pool, {
        kind: "email_changed",
        actorUserId: userId,
        subjectUserId: userId,
        via: "session",
        headers: request.headers,
        detail: address ? "set" : "removed",
        // The alert goes to the old address, when it was a verified one.
        alertTo: before?.verified ? before.address : null,
      });
    }
    const settings = await emailSettings(rt, userId);
    // A new address gets its link at once.
    if (result === "ok" && address) {
      const sent = await sendVerificationLink(rt, userId);
      return json(200, { ...settings, linkSent: sent.status === 202 });
    }
    return json(200, settings);
  }

  if (pathname === "/api/account/email/verification" && method === "POST") {
    return sendVerificationLink(rt, userId);
  }

  if (pathname === "/api/account/email/preferences" && method === "PUT") {
    const input = await body(request);
    const alerts = input.alerts;
    if (!alerts || typeof alerts !== "object" || Array.isArray(alerts)) {
      return problem(
        400,
        "bad_request",
        'Send {"alerts": {"new_device": false, …}}.',
      );
    }
    const changes: Partial<Record<AlertKind, boolean>> = {};
    for (const [kind, value] of Object.entries(alerts)) {
      if (
        !(ALERT_KINDS as readonly string[]).includes(kind) ||
        typeof value !== "boolean"
      ) {
        return problem(
          400,
          "bad_request",
          `Each switch is one of ${ALERT_KINDS.join(", ")}, on (true) or off (false).`,
        );
      }
      changes[kind as AlertKind] = value;
    }
    await setEmailPreferences(rt.pool, userId, changes);
    return json(200, { alerts: await emailPreferences(rt.pool, userId) });
  }

  if (pathname === "/api/account/email/sign-in-codes" && method === "POST") {
    const input = await body(request);
    if (typeof input.enabled !== "boolean") {
      return problem(
        400,
        "bad_request",
        'Send {"enabled": true, "password": "…"}.',
      );
    }
    if (!principal.sessionId) {
      return problem(
        401,
        "sign_in_required",
        "Sign in with your account to do this.",
      );
    }
    const on = input.enabled;
    if (on) {
      const account = await accountEmail(rt.pool, userId);
      if (!account?.verified) {
        return problem(
          409,
          "email_not_verified",
          "Verify your email address first, in Settings → Email.",
        );
      }
    }
    const confirmed = await confirmPassword(
      rt.pool,
      reauthLimiter(),
      { id: principal.sessionId, userId },
      input.password,
    );
    if (confirmed === "limited") {
      return problem(
        429,
        "too_many_attempts",
        "Too many wrong passwords. Try again in 15 minutes, or sign in again.",
      );
    }
    if (confirmed === "invalid") {
      return problem(400, "password_invalid", "That password is not right.");
    }
    // Adding a second factor needs the current one (reauth.ts): with an
    // authenticator app or a passkey on the account, a session that signed
    // in with it; the password, just confirmed, otherwise.
    if (on) {
      const may = await mayAddSecondFactor(rt.pool, {
        id: principal.sessionId,
        userId,
        secondFactor: principal.mfa,
      });
      if (may !== "ok") {
        return problem(
          403,
          "second_factor_sign_in_required",
          "Sign in again with your authenticator app or passkey to add another second factor.",
        );
      }
    }
    const changed = await setSignInCodes(rt.pool, userId, on);
    if (changed) {
      await recordSecurityEvent(rt.pool, {
        kind: on ? "two_factor_enabled" : "two_factor_disabled",
        actorUserId: userId,
        subjectUserId: userId,
        via: "session",
        headers: request.headers,
        detail: "email_code",
      });
    }
    return json(200, await emailSettings(rt, userId));
  }

  return problem(404, "not_found", "Not found");
}

/** /api/auth/email/…: no sign-in, so every write needs Life OS's own origin. */
export async function emailAuthRoute(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const { pathname } = new URL(request.url);
  if (request.method !== "POST") {
    return problem(405, "method_not_allowed", "POST only.");
  }
  if (!anonymousWriteAllowed(request.method, request.headers)) {
    return problem(
      403,
      "forbidden_origin",
      "This request did not come from Life OS.",
    );
  }

  if (pathname === "/api/auth/email/verify") {
    const input = await body(request);
    const verified = await consumeVerificationLink(rt.pool, input.token);
    if (!verified) {
      return problem(
        400,
        "invalid_link",
        "This link is wrong, used, or expired. Send a new one in Settings → Email.",
      );
    }
    await recordSecurityEvent(rt.pool, {
      kind: "email_verified",
      subjectUserId: verified.userId,
      headers: request.headers,
    });
    return json(200, { verified: true });
  }

  if (pathname === "/api/auth/email/forgot-password") {
    if (!(await rt.email?.enabled())) return EMAIL_OFF();
    const network = addressKey(clientAddress(request.headers) ?? "unknown");
    const wait =
      rt.emailLimiter?.take(`reset-net:${network}`, LIMITS.resetPerNetwork) ??
      0;
    if (wait > 0) {
      return problem(
        429,
        "too_many_requests",
        `Too many reset links asked for from here. Try again in ${Math.ceil(wait / 60_000)} min.`,
      );
    }
    const input = await body(request);
    const login = typeof input.login === "string" ? input.login : "";
    if (!login.trim() || login.length > 254) {
      return problem(
        400,
        "bad_request",
        "Enter your username or your email address.",
      );
    }
    // The same answer whether or not the account exists, has a verified
    // address, or got a link a moment ago; the email goes out after it.
    void sendResetLink(rt, login, request.headers).catch((error) =>
      console.error(
        "email: a reset link was not sent",
        error instanceof Error ? error.message : error,
      ),
    );
    return json(200, {
      ok: true,
      minutes: RESET_LINK_MINUTES,
    });
  }

  return problem(404, "not_found", "Not found");
}

/** Mails a reset link, when the login names an account with a verified address. */
async function sendResetLink(
  rt: CheckRuntime,
  login: string,
  headers: Headers,
) {
  const account = await accountByLogin(rt.pool, login);
  if (!account?.verified || !account.address) return;
  const { rows } = await rt.pool.query<{ banned: boolean | null }>(
    'SELECT banned FROM "user" WHERE id = $1',
    [account.userId],
  );
  if (rows[0]?.banned) return;
  if (
    (rt.emailLimiter?.take(
      `reset:${account.address}`,
      LIMITS.resetPerAddress,
    ) ?? 0) > 0
  ) {
    return;
  }
  const link = await createResetLink(rt.pool, account.userId, rt.env.publicUrl);
  const result = await rt.email!.send({
    to: account.address,
    ...passwordResetMessage({
      publicUrl: rt.env.publicUrl,
      name: account.username ?? account.name,
      username: account.username,
      link,
      minutes: RESET_LINK_MINUTES,
    }),
    kind: "password_reset",
  });
  await recordSecurityEvent(rt.pool, {
    kind: "password_reset_requested",
    outcome: result.ok ? "ok" : "failed",
    subjectUserId: account.userId,
    headers,
    detail: result.ok ? "email" : result.code,
  });
}

/** Mails a sign-in code (Better Auth's two-factor OTP) to the account's verified address. */
export async function sendSignInCode(
  rt: Pick<CheckRuntime, "pool" | "email"> & { publicUrl: string },
  userId: string,
  code: string,
): Promise<boolean> {
  const account = await accountEmail(rt.pool, userId);
  if (!rt.email || !account?.verified || !account.address) return false;
  const result = await rt.email.send({
    to: account.address,
    ...signInCodeMessage({
      publicUrl: rt.publicUrl,
      name: account.username ?? account.name,
      code,
      minutes: SIGN_IN_CODE_MINUTES,
    }),
    kind: "sign_in_code",
  });
  return result.ok;
}
