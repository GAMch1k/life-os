"use client";

import { passkeyClient } from "@better-auth/passkey/client";
import { twoFactorClient, usernameClient } from "better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";

/**
 * Better Auth's client for the login, setup, reset, and Security pages: on
 * the page's own origin (/api/auth), whichever host the owner opened.
 */
export const authClient = createAuthClient({
  plugins: [usernameClient(), twoFactorClient(), passkeyClient()],
});

type BetterAuthError = {
  code?: string;
  message?: string;
  status?: number;
} | null;

/** A sentence for a failed Better Auth call. */
export function authErrorMessage(error: BetterAuthError, fallback: string) {
  if (!error) return fallback;
  switch (error.code) {
    case "INVALID_USERNAME_OR_PASSWORD":
    case "INVALID_EMAIL_OR_PASSWORD":
      return "Wrong username or password.";
    case "INVALID_CODE":
    case "INVALID_TWO_FACTOR_COOKIE":
      return "That code is not right. Check the time on your phone, then try the newest code.";
    case "INVALID_BACKUP_CODE":
      return "That recovery code is not right, or was already used.";
    case "BANNED_USER":
      return "This account is disabled. Ask the server's owner.";
    case "USERNAME_IS_ALREADY_TAKEN":
    case "USERNAME_IS_ALREADY_TAKEN_PLEASE_TRY_ANOTHER":
      return "That username is taken.";
    case "PASSWORD_TOO_SHORT":
      return "The password needs at least 10 characters.";
    case "PASSWORD_TOO_LONG":
      return "The password is too long.";
    case "SESSION_NOT_FRESH":
      return "Sign in again to do this: it needs a sign-in from the last 12 hours.";
    case "INVALID_PASSWORD":
      return "That password is not right.";
  }
  if (error.status === 429) {
    return error.message?.startsWith("Too many")
      ? error.message
      : "Too many attempts. Wait a minute, then try again.";
  }
  return error.message || fallback;
}
