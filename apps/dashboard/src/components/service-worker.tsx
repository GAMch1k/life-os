"use client";

import { useEffect } from "react";

import { SERVICE_WORKER_PATH } from "@/lib/web-app";

/**
 * Registers the service worker (public/sw.js), which makes the Dashboard
 * installable and caches nothing. Browsers offer service workers only in a
 * secure context: over https (LIFE_OS_PUBLIC_URL) or on localhost, not over
 * plain http such as the WireGuard address.
 */
export function ServiceWorker() {
  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    navigator.serviceWorker
      .register(SERVICE_WORKER_PATH, { scope: "/", updateViaCache: "none" })
      .catch(() => {
        // Without it the Dashboard works the same; it only cannot install.
      });
  }, []);
  return null;
}
