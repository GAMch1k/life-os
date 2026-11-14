"use client";

import { useSyncExternalStore } from "react";

const noSubscription = () => () => {};

/**
 * Whether this page is a secure context (HTTPS or localhost), where the
 * Secure session cookie works. Unknown (undefined) on the server and while
 * hydrating.
 */
export function useSecureContext(): boolean | undefined {
  return useSyncExternalStore(
    noSubscription,
    () => window.isSecureContext,
    () => undefined,
  );
}

/**
 * Whether passkeys work here: a secure context with WebAuthn, on the public
 * address's origin, which passkeys are bound to.
 */
export function usePasskeysHere(passkeyOrigin: string): boolean | undefined {
  return useSyncExternalStore(
    noSubscription,
    () =>
      window.isSecureContext &&
      typeof window.PublicKeyCredential !== "undefined" &&
      window.location.origin === passkeyOrigin,
    () => undefined,
  );
}
