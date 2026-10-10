// The date picker (components/date-picker.tsx) holds calendar days as
// YYYY-MM-DD strings, as the native date inputs it replaced did, so the
// forms' values, validation, and API payloads stay the same. Its calendar
// grid (react-day-picker) works in Date objects at the browser's local
// midnight; a day never passes through an instant or another timezone, so
// the day picked is the day stored, wherever the browser is.

/** How many years either side of today the month and year dropdowns reach. */
export const PICKER_YEARS = 100;

const DAY = /^(\d{4})-(\d{2})-(\d{2})$/;

const pad = (value: number, length = 2) => String(value).padStart(length, "0");

/** The YYYY-MM-DD wall date of a Date in the browser's timezone. */
export function dateToDay(date: Date) {
  return `${pad(date.getFullYear(), 4)}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/**
 * The Date (local midnight) the calendar grid shows for a YYYY-MM-DD day;
 * undefined for "" or anything that is not a real day, such as 2026-02-30.
 */
export function dayToDate(day: string | undefined) {
  const match = day ? DAY.exec(day) : null;
  if (!match) return undefined;
  const date = new Date(
    Number(match[1]),
    Number(match[2]) - 1,
    Number(match[3]),
  );
  return dateToDay(date) === day ? date : undefined;
}

const labels = new Map<string, Intl.DateTimeFormat>();

/**
 * A day as the picker's button shows it, in `locale` (the browser's): "Wed,
 * 21 Oct 2026" in en-GB, "Wed, Oct 21, 2026" in en-US.
 */
export function pickerLabel(day: string, locale: string) {
  if (!dayToDate(day)) return day;
  let format = labels.get(locale);
  if (!format) {
    format = new Intl.DateTimeFormat(locale, {
      weekday: "short",
      day: "numeric",
      month: "short",
      year: "numeric",
      timeZone: "UTC",
    });
    labels.set(locale, format);
  }
  return format.format(new Date(`${day}T00:00:00Z`));
}

/**
 * The first and last month the picker's calendar reaches (as YYYY-MM-DD
 * days): `min` and `max` when set, otherwise PICKER_YEARS before and after
 * today, widened to reach the chosen day.
 */
export function pickerRange({
  max,
  min,
  today,
  value,
}: {
  max?: string;
  min?: string;
  today: string;
  value?: string;
}) {
  const years = [today, value]
    .filter((day): day is string => Boolean(dayToDate(day)))
    .map((day) => Number(day.slice(0, 4)));
  if (!years.length) years.push(new Date().getFullYear());
  const first = Math.min(...years) - PICKER_YEARS;
  const last = Math.max(...years) + PICKER_YEARS;
  return {
    start: dayToDate(min) ? min! : `${pad(first, 4)}-01-01`,
    end: dayToDate(max) ? max! : `${pad(last, 4)}-12-31`,
  };
}
