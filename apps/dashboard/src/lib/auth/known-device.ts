import { createHmac, timingSafeEqual } from "node:crypto";

/**
 * The known-device cookie (throttle.ts, docs/install/accounts.md#failed-sign-ins):
 * a browser that signed in to an account with its password gets one, and
 * the account's delay after failed sign-ins from everywhere never holds it
 * back, so a stranger's failures cannot lock the account's person out. It
 * names no account and holds nothing but an HMAC of the username under a
 * key derived from LIFE_OS_AUTH_SECRET: only this server makes one, and one
 * account's never counts for another. It proves nothing else, and opens
 * nothing: the per-address delay, the password, and the second factor
 * still apply. A new LIFE_OS_AUTH_SECRET makes every one void.
 */

/** On HTTPS: host-only and Secure, as the session cookie. */
export const KNOWN_DEVICE_COOKIE = "__Host-lifeos.known";
/** On loopback and an allowed plain-HTTP host, as the session cookie there. */
export const INSECURE_KNOWN_DEVICE_COOKIE = "lifeos.known";
/** Kept 400 days (browsers' cap), renewed by every sign-in. */
export const KNOWN_DEVICE_MAX_AGE_SECONDS = 400 * 24 * 60 * 60;

const CONTEXT = "lifeos-known-device:v1";

/** The cookie's value for a username, as this server's secret makes it. */
export function knownDeviceValue(secret: string, username: string): string {
  const key = createHmac("sha256", secret).update(CONTEXT).digest();
  return createHmac("sha256", key)
    .update(username.trim().toLowerCase())
    .digest("base64url");
}

/** Whether a cookie's value is this username's, made with this secret. */
export function isKnownDevice(
  secret: string,
  username: string,
  value: string | null | undefined,
): boolean {
  if (!value || !username.trim()) return false;
  const want = Buffer.from(knownDeviceValue(secret, username));
  const got = Buffer.from(value);
  return got.length === want.length && timingSafeEqual(got, want);
}
