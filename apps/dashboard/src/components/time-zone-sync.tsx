"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

import {
  browserTimeZone,
  storedTimeZone,
  timeZoneCookie,
  timeZoneSync,
} from "@/lib/time-zone";

/** Zones this page load already refreshed for, so a refresh never repeats. */
const refreshedFor = new Set<string>();

/**
 * Tells the server the browser's timezone. Pages render in the zone the
 * server resolved (`timeZone`: the `lifeos-tz` cookie, else
 * LIFE_OS_TIMEZONE, else UTC); when the browser's zone differs, this stores
 * it in the cookie and refreshes once, so the page re-renders in the
 * browser's zone without a hydration mismatch. It checks again when the tab
 * becomes visible, for a device that changed zone while the page was open.
 */
export function TimeZoneSync({ timeZone }: { timeZone: string }) {
  const router = useRouter();

  useEffect(() => {
    function sync() {
      const browser = browserTimeZone();
      const { store, refresh } = timeZoneSync({
        browser,
        rendered: timeZone,
        stored: storedTimeZone(document.cookie),
      });
      if (!browser || !store) return;
      document.cookie = timeZoneCookie(browser);
      // Refresh only when the cookie stuck (cookies may be blocked).
      if (
        refresh &&
        storedTimeZone(document.cookie) === browser &&
        !refreshedFor.has(browser)
      ) {
        refreshedFor.add(browser);
        router.refresh();
      }
    }
    sync();
    function onVisibilityChange() {
      if (document.visibilityState === "visible") sync();
    }
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () =>
      document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [router, timeZone]);

  return null;
}
