import { headers } from "next/headers";

import { type CheckResult, checkRequest } from "./check";
import { authRuntime } from "./runtime";
import { insecureLoginAllowed } from "./server";
import { firstAccountExists } from "./store";

/**
 * What the account pages (login, setup, reset, Security) render from, on
 * the server: who this request is, and the server's facts they show.
 */

/** A `next` page to go back to after signing in: same site, a path only. */
export function safeNext(value: string | string[] | undefined): string {
  const next = Array.isArray(value) ? value[0] : value;
  if (!next || !next.startsWith("/") || next.startsWith("//")) return "/";
  if (/^\/(login|setup|reset)(\/|\?|$)/.test(next)) return "/";
  return next;
}

export type LoginContext = {
  publicUrl: string;
  /** Plain HTTP is allowed for this host (LIFE_OS_INSECURE_LOGIN_HOSTS). */
  insecureAllowed: boolean;
  accountExists: boolean;
  /** Passkeys work only on the public address's origin. */
  passkeyOrigin: string;
  signedIn: boolean;
};

export async function loginContext(): Promise<LoginContext> {
  const rt = authRuntime();
  const requestHeaders = new Headers(await headers());
  let result: CheckResult | null = null;
  try {
    result = await checkRequest(rt, "GET", requestHeaders);
  } catch {
    result = null;
  }
  return {
    publicUrl: rt.env.publicUrl,
    insecureAllowed: insecureLoginAllowed(rt.env, requestHeaders),
    accountExists: await firstAccountExists(rt.pool).catch(() => true),
    passkeyOrigin: new URL(rt.env.publicUrl).origin,
    signedIn: result?.status === 204 && result.principal.via === "session",
  };
}

/** This request's check, for a page the proxy already let through. */
export async function currentCheck() {
  const rt = authRuntime();
  return {
    rt,
    result: await checkRequest(rt, "GET", new Headers(await headers())),
  };
}
