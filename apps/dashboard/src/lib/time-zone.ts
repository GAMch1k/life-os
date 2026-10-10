// The owner's timezone (LIFE_OS_TIMEZONE, an IANA name such as Europe/Berlin).
// Every "today", day grouping, and displayed date or time in the Dashboard uses
// it, on the server and in the browser alike, so server-rendered and hydrated
// output agree regardless of the browser's own timezone or language.

export const DEFAULT_TIME_ZONE = "UTC";

/** Fixed formatting locale, so server and browser render identical text. */
export const DATE_LOCALE = "en-GB";

export function isValidTimeZone(timeZone: string) {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone });
    return true;
  } catch {
    return false;
  }
}

/** Server only: the configured timezone, or UTC when unset or invalid. */
export function userTimeZone() {
  const configured = process.env.LIFE_OS_TIMEZONE?.trim();
  return configured && isValidTimeZone(configured)
    ? configured
    : DEFAULT_TIME_ZONE;
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
