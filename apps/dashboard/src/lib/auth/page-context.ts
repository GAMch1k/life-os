import { headers } from "next/headers";
import { redirect } from "next/navigation";

import { adminGate } from "./admin";
import { type CheckResult, checkRequest } from "./check";
import { recentlyAuthenticated } from "./reauth";
import { isAdminRole } from "./roles";
import { authRuntime } from "./runtime";
import { plainHttpLoginAllowed } from "./server";
import { firstAccountExists } from "./store";

/**
 * What the account pages (login, setup, reset, Security) render from, on
 * the server: who this request is, and the server's facts they show.
 */

export { safeNext } from "./safe-next";

export type LoginContext = {
  publicUrl: string;
  /**
   * Signing in over plain HTTP works for this host: one the owner allowed
   * (LIFE_OS_INSECURE_LOGIN_HOSTS), or this machine's loopback names (an
   * SSH tunnel).
   */
  insecureAllowed: boolean;
  accountExists: boolean;
  /** Passkeys work only on the public address's origin. */
  passkeyOrigin: string;
  signedIn: boolean;
  /** When this browser's session signed in (ms since 1970), if it has one. */
  signedInAt: number | null;
  /** Signed in, or the password re-entered, within 12 hours. */
  recentAuth: boolean;
  /**
   * Email works on this server (docs/install/email.md): "Forgot password?"
   * and sign-in codes by email are offered only then.
   */
  emailEnabled: boolean;
};

export async function loginContext(): Promise<LoginContext> {
  const rt = authRuntime();
  const requestHeaders = new Headers(await headers());
  let result: CheckResult | null = null;
  try {
    result = await checkRequest(rt, "GET", requestHeaders, { count: false });
  } catch {
    result = null;
  }
  const session =
    result?.status === 204 && result.principal.via === "session"
      ? result.principal
      : null;
  return {
    publicUrl: rt.env.publicUrl,
    insecureAllowed: plainHttpLoginAllowed(rt.env, requestHeaders),
    accountExists: await firstAccountExists(rt.pool).catch(() => true),
    passkeyOrigin: new URL(rt.env.publicUrl).origin,
    signedIn: session !== null,
    signedInAt: session?.sessionCreatedAt?.getTime() ?? null,
    recentAuth: session
      ? await recentlyAuthenticated(rt.pool, {
          id: session.sessionId,
          createdAt: session.sessionCreatedAt,
        }).catch(() => false)
      : false,
    emailEnabled: (await rt.email?.enabled().catch(() => false)) ?? false,
  };
}

/**
 * A page of the account's own (Settings): a real sign-in, else the login
 * page, and back here afterwards.
 */
export async function accountPage(next: string) {
  const { rt, result } = await currentCheck();
  if (result.status !== 204 || result.principal.via !== "session") {
    redirect(`/login?next=${encodeURIComponent(next)}`);
  }
  const principal = result.principal;
  return { rt, principal, admin: isAdminRole(principal.role) };
}

/**
 * An admin page (src/lib/auth/admin.ts): a real sign-in, else the login
 * page; then the gate, which the page shows when it refuses.
 */
export async function adminPage(next: string) {
  const { rt, result } = await currentCheck();
  if (result.status !== 204 || result.principal.via !== "session") {
    redirect(`/login?next=${encodeURIComponent(next)}`);
  }
  const gate = await adminGate(rt, result, { legacy: "refuse" });
  return {
    rt,
    gate,
    principal: result.principal,
    admin: isAdminRole(result.principal.role),
  };
}

/** This request's check, for a page the proxy already let through. */
export async function currentCheck() {
  const rt = authRuntime();
  return {
    rt,
    result: await checkRequest(rt, "GET", new Headers(await headers()), {
      count: false,
    }),
  };
}
