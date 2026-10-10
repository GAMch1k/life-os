import { cookies } from "next/headers";

import { resolveTimeZone, TIME_ZONE_COOKIE } from "@/lib/time-zone";

// Server only (it reads the request's cookies); client components receive
// the resolved zone as a prop.

/** The configured fallback: LIFE_OS_TIMEZONE, or UTC when unset or invalid. */
export function configuredTimeZone() {
  return resolveTimeZone(process.env.LIFE_OS_TIMEZONE);
}

/**
 * The zone this request renders in: the browser's (the `lifeos-tz` cookie
 * TimeZoneSync stores), else LIFE_OS_TIMEZONE, else UTC.
 */
export async function userTimeZone() {
  const store = await cookies();
  return resolveTimeZone(
    store.get(TIME_ZONE_COOKIE)?.value,
    process.env.LIFE_OS_TIMEZONE,
  );
}
