// The timezone the Dashboard shows dates and times in: the owner's browser
// zone when the browser has reported it (the `lifeos-tz` cookie, see
// TimeZoneSync), else LIFE_OS_TIMEZONE (an IANA name such as Europe/Berlin),
// else UTC. The server resolves it once per request (user-time-zone.ts) and
// passes it to client components as a prop, so every "today", day grouping,
// and displayed date or time agrees between the server HTML and the hydrated
// page, whatever the browser's language. Stored data stays UTC instants.

export const DEFAULT_TIME_ZONE = "UTC";

/** Fixed formatting locale, so server and browser render identical text. */
export const DATE_LOCALE = "en-GB";

/** Cookie holding the browser's IANA timezone, written by TimeZoneSync. */
export const TIME_ZONE_COOKIE = "lifeos-tz";

/** A year, in seconds: the browser rewrites the cookie when it changes. */
const TIME_ZONE_COOKIE_MAX_AGE = 365 * 24 * 60 * 60;

export function isValidTimeZone(timeZone: string) {
  if (!timeZone.trim()) return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone });
    return true;
  } catch {
    return false;
  }
}

/** The first valid IANA timezone among the candidates, else UTC. */
export function resolveTimeZone(...candidates: (string | null | undefined)[]) {
  for (const candidate of candidates) {
    const timeZone = candidate?.trim();
    if (timeZone && isValidTimeZone(timeZone)) return timeZone;
  }
  return DEFAULT_TIME_ZONE;
}

/** Browser only (events and effects): the device's timezone, if valid. */
export function browserTimeZone() {
  try {
    const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return timeZone && isValidTimeZone(timeZone) ? timeZone : undefined;
  } catch {
    return undefined;
  }
}

/** The `document.cookie` assignment that stores the browser's timezone. */
export function timeZoneCookie(timeZone: string) {
  return `${TIME_ZONE_COOKIE}=${encodeURIComponent(timeZone)}; Path=/; Max-Age=${TIME_ZONE_COOKIE_MAX_AGE}; SameSite=Lax`;
}

/** The timezone stored in a `document.cookie` string, if any. */
export function storedTimeZone(cookie: string) {
  for (const pair of cookie.split(/;\s*/)) {
    const [name, ...value] = pair.split("=");
    if (name === TIME_ZONE_COOKIE) {
      try {
        return decodeURIComponent(value.join("="));
      } catch {
        return undefined;
      }
    }
  }
  return undefined;
}

/**
 * What TimeZoneSync does about the browser's timezone: store it when the
 * cookie holds something else, and re-render (refresh) when the page was
 * rendered in another zone. When the cookie already holds the browser's zone
 * but the server still rendered another, the server cannot use it (for
 * example its ICU lacks that zone), so refreshing again would loop.
 */
export function timeZoneSync({
  browser,
  rendered,
  stored,
}: {
  browser: string | undefined;
  rendered: string;
  stored: string | undefined;
}) {
  if (!browser) return { store: false, refresh: false };
  const store = stored !== browser;
  return { store, refresh: store && rendered !== browser };
}

const formats = new Map<string, Intl.DateTimeFormat>();

/** A cached DATE_LOCALE formatter for instants in the given timezone. */
export function dateTimeFormat(
  timeZone: string,
  options: Intl.DateTimeFormatOptions,
) {
  const key = `${timeZone}|${JSON.stringify(options)}`;
  let format = formats.get(key);
  if (!format) {
    format = new Intl.DateTimeFormat(DATE_LOCALE, { ...options, timeZone });
    formats.set(key, format);
  }
  return format;
}

const dayKeyFormats = new Map<string, Intl.DateTimeFormat>();

/** The calendar day (YYYY-MM-DD) of an instant in the given timezone. */
export function dayKey(instant: Date | string, timeZone: string) {
  let format = dayKeyFormats.get(timeZone);
  if (!format) {
    format = new Intl.DateTimeFormat("en-CA", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    });
    dayKeyFormats.set(timeZone, format);
  }
  return format.format(new Date(instant));
}

/** The day `days` after a YYYY-MM-DD day (negative goes back). */
export function addDays(day: string, days: number) {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** Offset of the timezone from UTC at an instant, in milliseconds. */
function offsetAt(instant: Date, timeZone: string) {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(instant);
  const value = (type: string) =>
    Number(parts.find((part) => part.type === type)?.value);
  const asUtc = Date.UTC(
    value("year"),
    value("month") - 1,
    value("day"),
    value("hour"),
    value("minute"),
    value("second"),
  );
  return asUtc - Math.floor(instant.getTime() / 1000) * 1000;
}

/** The instant a YYYY-MM-DD day starts (local midnight) in the timezone. */
export function startOfDay(day: string, timeZone: string) {
  const midnightUtc = new Date(`${day}T00:00:00Z`);
  // Two passes settle the offset across a DST change on that day.
  let start = new Date(midnightUtc.getTime() - offsetAt(midnightUtc, timeZone));
  start = new Date(midnightUtc.getTime() - offsetAt(start, timeZone));
  return start;
}
