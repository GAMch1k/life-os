import type { components } from "@/lib/api/generated/calendar";
import { addDays, dateTimeFormat, dayKey, startOfDay } from "@/lib/time-zone";

// Recurring events, as the event form's Repeat control and the details show
// them. Calendar stores the rule (`Recurrence`, an iCalendar RRULE subset);
// these helpers turn it into plain words, into the form's values, and back.
// A rule repeats from the event's start: weekly on the start's weekday (or
// each of `byWeekday`), monthly on its day of the month, yearly on its date.

export type Recurrence = components["schemas"]["Recurrence"];
export type Weekday = components["schemas"]["Weekday"];
export type Frequency = Recurrence["frequency"];

/** The days of the week in week order, Monday first, as Calendar returns them. */
export const WEEK: Weekday[] = ["MO", "TU", "WE", "TH", "FR", "SA", "SU"];
const WORKWEEK: Weekday[] = ["MO", "TU", "WE", "TH", "FR"];

export const WEEKDAY_NAME: Record<Weekday, string> = {
  MO: "Monday",
  TU: "Tuesday",
  WE: "Wednesday",
  TH: "Thursday",
  FR: "Friday",
  SA: "Saturday",
  SU: "Sunday",
};

const MONTH_NAME = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

const DAY_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

/** The weekday of a YYYY-MM-DD day, else undefined. */
export function weekdayOf(day: string): Weekday | undefined {
  if (!DAY_PATTERN.test(day)) return undefined;
  const date = new Date(`${day}T00:00:00Z`);
  if (Number.isNaN(date.getTime())) return undefined;
  // getUTCDay counts from Sunday.
  return WEEK[(date.getUTCDay() + 6) % 7];
}

/** Each day once, in week order. */
export function weekOrder(days: Weekday[]): Weekday[] {
  return WEEK.filter((day) => days.includes(day));
}

/** The days moved `shift` days later (earlier when negative), in week order. */
export function shiftWeekdays(days: Weekday[], shift: number): Weekday[] {
  const by = ((shift % 7) + 7) % 7;
  return weekOrder(days.map((day) => WEEK[(WEEK.indexOf(day) + by) % 7]));
}

const sameDays = (a: Weekday[], b: Weekday[]) =>
  a.length === b.length && a.every((day, index) => day === b[index]);

/** "17" and "September 17" for a YYYY-MM-DD day. */
function dayParts(day: string) {
  if (!weekdayOf(day)) return undefined;
  const month = Number(day.slice(5, 7));
  const date = Number(day.slice(8, 10));
  return { date, monthDay: `${MONTH_NAME[month - 1]} ${date}` };
}

/** "Monday", "Monday and Wednesday", "Monday, Wednesday, and Friday". */
function dayList(days: Weekday[]) {
  const names = days.map((day) => WEEKDAY_NAME[day]);
  if (names.length <= 2) return names.join(" and ");
  return `${names.slice(0, -1).join(", ")}, and ${names.at(-1)}`;
}

const UNIT: Record<Frequency, string> = {
  daily: "day",
  weekly: "week",
  monthly: "month",
  yearly: "year",
};

/** "31 Dec 2026": a YYYY-MM-DD day, the same in every timezone. */
function longDate(day: string) {
  return dateTimeFormat("UTC", {
    day: "numeric",
    month: "short",
    year: "numeric",
  }).format(new Date(`${day}T00:00:00Z`));
}

/**
 * The rule in plain words, such as "Weekly on Monday and Wednesday", "Every
 * weekday", "Annually on September 17", "Every 2 weeks on Tuesday, until 31
 * Dec 2026", or "Monthly on day 15, 10 times". `startDate` is the day the
 * series starts (or any of its occurrences' original days), which decides
 * the weekday, day of the month, and date it repeats on; `until` reads as a
 * day in `timeZone`, the zone the series repeats in.
 */
export function recurrenceLabel(
  recurrence: Recurrence,
  startDate: string,
  timeZone: string,
) {
  const n = recurrence.interval;
  const parts = dayParts(startDate);
  let rule: string;
  switch (recurrence.frequency) {
    case "daily":
      rule = n > 1 ? `Every ${n} days` : "Daily";
      break;
    case "weekly": {
      const start = weekdayOf(startDate);
      const days = weekOrder(recurrence.byWeekday ?? (start ? [start] : []));
      const workweek = sameDays(days, WORKWEEK);
      const on = workweek
        ? "weekdays"
        : days.length === 7
          ? "every day"
          : dayList(days);
      if (n > 1) rule = on ? `Every ${n} weeks on ${on}` : `Every ${n} weeks`;
      else if (workweek) rule = "Every weekday";
      else rule = on ? `Weekly on ${on}` : "Weekly";
      break;
    }
    case "monthly": {
      const on = parts ? ` on day ${parts.date}` : "";
      rule = n > 1 ? `Every ${n} months${on}` : `Monthly${on}`;
      break;
    }
    case "yearly": {
      const on = parts ? ` on ${parts.monthDay}` : "";
      rule = n > 1 ? `Every ${n} years${on}` : `Annually${on}`;
      break;
    }
  }
  if (recurrence.until) {
    return `${rule}, until ${longDate(dayKey(recurrence.until, timeZone))}`;
  }
  if (recurrence.count) {
    return recurrence.count === 1
      ? `${rule}, once`
      : `${rule}, ${recurrence.count} times`;
  }
  return rule;
}

// The Repeat control -------------------------------------------------------------

/** The Repeat menu's choices, like Google Calendar's. */
export type RepeatPreset =
  "none" | "daily" | "weekly" | "weekdays" | "monthly" | "yearly" | "custom";

export type RepeatEnds = "never" | "on" | "after";

/** The Repeat control's values, as the inputs hold them. */
export type RepeatDraft = {
  preset: RepeatPreset;
  /** Custom: every `interval` days, weeks, months, or years. */
  frequency: Frequency;
  interval: string;
  /**
   * Custom weekly: the days chosen besides the start's weekday, which always
   * repeats (and follows the start date when it changes).
   */
  days: Weekday[];
  ends: RepeatEnds;
  /** Ends on this day (YYYY-MM-DD), inclusive. */
  until: string;
  /** Ends after this many occurrences. */
  count: string;
};

export const NO_REPEAT: RepeatDraft = {
  preset: "none",
  frequency: "weekly",
  interval: "1",
  days: [],
  ends: "never",
  until: "",
  count: "10",
};

/**
 * The Repeat menu, its labels following the form's start date: "Weekly on
 * Thursday", "Monthly on day 17", "Annually on September 17". "Every
 * weekday" needs a start on a weekday.
 */
export function repeatOptions(startDate: string) {
  const weekday = weekdayOf(startDate);
  const parts = dayParts(startDate);
  return [
    { value: "none", label: "Does not repeat" },
    { value: "daily", label: "Daily" },
    {
      value: "weekly",
      label: weekday ? `Weekly on ${WEEKDAY_NAME[weekday]}` : "Weekly",
    },
    {
      value: "weekdays",
      label: "Every weekday (Monday to Friday)",
      disabled: weekday === "SA" || weekday === "SU",
    },
    {
      value: "monthly",
      label: parts ? `Monthly on day ${parts.date}` : "Monthly",
    },
    {
      value: "yearly",
      label: parts ? `Annually on ${parts.monthDay}` : "Annually",
    },
    { value: "custom", label: "Custom…" },
  ] satisfies { value: RepeatPreset; label: string; disabled?: boolean }[];
}

/**
 * The control's values when Custom is chosen: the rule picked so far, so
 * "Weekly on Thursday" becomes every 1 week on Thursday (the start's day).
 */
export function customFrom(draft: RepeatDraft): RepeatDraft {
  const base = { ...draft, preset: "custom" as const, interval: "1" };
  switch (draft.preset) {
    case "daily":
      return { ...base, frequency: "daily" };
    case "weekdays":
      return { ...base, frequency: "weekly", days: WORKWEEK };
    case "monthly":
      return { ...base, frequency: "monthly" };
    case "yearly":
      return { ...base, frequency: "yearly" };
    case "custom":
      return draft;
    default:
      return { ...base, frequency: "weekly", days: [] };
  }
}

/** The weekly days a custom rule repeats on: those chosen plus the start's. */
export function customDays(draft: RepeatDraft, startDate: string) {
  const weekday = weekdayOf(startDate);
  return weekOrder(weekday ? [...draft.days, weekday] : draft.days);
}

/** Whether the control holds the same choice; "changed" offers no This event. */
export function sameRepeat(a: RepeatDraft, b: RepeatDraft) {
  if (a.preset !== b.preset) return false;
  if (a.preset !== "custom") return true;
  return (
    a.frequency === b.frequency &&
    a.interval.trim() === b.interval.trim() &&
    (a.frequency !== "weekly" ||
      sameDays(weekOrder(a.days), weekOrder(b.days))) &&
    a.ends === b.ends &&
    (a.ends !== "on" || a.until === b.until) &&
    (a.ends !== "after" || a.count.trim() === b.count.trim())
  );
}

/**
 * The instant a series ending on `day` may last start: the end of that day
 * in `timeZone` (the zone a timed series repeats in; the owner's for all-day).
 */
export function untilInstant(day: string, timeZone: string) {
  const end = startOfDay(addDays(day, 1), timeZone).getTime() - 1000;
  return new Date(end).toISOString().replace(".000Z", "Z");
}

const wholeNumber = (value: string) =>
  /^\d+$/.test(value.trim()) ? Number(value.trim()) : undefined;

/**
 * The rule the control describes (null: does not repeat), or what to fix.
 * `startDate` is the form's start day; `timeZone` the zone it repeats in.
 * Calendar checks the rest, such as an end before the start.
 */
export function readRepeat(
  draft: RepeatDraft,
  startDate: string,
  timeZone: string,
): { recurrence: Recurrence | null } | { error: string } {
  switch (draft.preset) {
    case "none":
      return { recurrence: null };
    case "daily":
      return { recurrence: { frequency: "daily", interval: 1 } };
    case "weekly":
      return { recurrence: { frequency: "weekly", interval: 1 } };
    case "weekdays":
      return {
        recurrence: { frequency: "weekly", interval: 1, byWeekday: WORKWEEK },
      };
    case "monthly":
      return { recurrence: { frequency: "monthly", interval: 1 } };
    case "yearly":
      return { recurrence: { frequency: "yearly", interval: 1 } };
  }
  const interval = wholeNumber(draft.interval);
  if (!interval) {
    return { error: "Repeat every needs a whole number, 1 or more." };
  }
  const recurrence: Recurrence = { frequency: draft.frequency, interval };
  if (draft.frequency === "weekly") {
    const days = customDays(draft, startDate);
    // Just the start's weekday is what weekly means anyway.
    if (days.length > 1) recurrence.byWeekday = days;
  }
  if (draft.ends === "on") {
    if (!weekdayOf(draft.until)) {
      return { error: "Choose the last day it repeats on." };
    }
    recurrence.until = untilInstant(draft.until, timeZone);
  } else if (draft.ends === "after") {
    const count = wholeNumber(draft.count);
    if (!count || count > 1000) {
      return { error: "Ends after needs a number of occurrences, 1 to 1000." };
    }
    recurrence.count = count;
  }
  return { recurrence };
}

/**
 * The control's values for a stored rule: a menu choice when it is one
 * (every 1, no end), else Custom. `startDate` is the form's start day and
 * `timeZone` the zone `until` reads in.
 */
export function repeatDraftFrom(
  recurrence: Recurrence | null | undefined,
  startDate: string,
  timeZone: string,
): RepeatDraft {
  if (!recurrence) return NO_REPEAT;
  const weekday = weekdayOf(startDate);
  const days = weekOrder(recurrence.byWeekday ?? (weekday ? [weekday] : []));
  const endless = !recurrence.until && !recurrence.count;
  if (recurrence.interval === 1 && endless) {
    const preset: RepeatPreset | undefined =
      recurrence.frequency === "weekly"
        ? weekday && sameDays(days, [weekday])
          ? "weekly"
          : sameDays(days, WORKWEEK)
            ? "weekdays"
            : undefined
        : recurrence.frequency;
    if (preset) return { ...NO_REPEAT, preset };
  }
  return {
    preset: "custom",
    frequency: recurrence.frequency,
    interval: String(recurrence.interval),
    days:
      recurrence.frequency === "weekly"
        ? days.filter((day) => day !== weekday)
        : [],
    ends: recurrence.until ? "on" : recurrence.count ? "after" : "never",
    until: recurrence.until ? dayKey(recurrence.until, timeZone) : "",
    count: recurrence.count ? String(recurrence.count) : NO_REPEAT.count,
  };
}

/** "day", "weeks": the unit of a custom rule, as many as `interval` says. */
export function unitLabel(frequency: Frequency, interval: string) {
  const unit = UNIT[frequency];
  return wholeNumber(interval) === 1 ? unit : `${unit}s`;
}

/**
 * What a custom monthly or yearly rule repeats on, as the control explains
 * it: "On day 31 of the month; months without one are skipped.", "On
 * February 29, in leap years."
 */
export function repeatsOnNote(frequency: Frequency, startDate: string) {
  const parts = dayParts(startDate);
  if (!parts) return undefined;
  if (frequency === "monthly") {
    return parts.date > 28
      ? `On day ${parts.date} of the month; months without one are skipped.`
      : `On day ${parts.date} of the month.`;
  }
  if (frequency === "yearly") {
    return parts.monthDay === "February 29"
      ? "On February 29, in leap years."
      : `On ${parts.monthDay}.`;
  }
  return undefined;
}
