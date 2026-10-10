import assert from "node:assert/strict";
import test from "node:test";

import {
  apiErrorMessage,
  type CalendarEvent,
  createRequest,
  draftFromEvent,
  type EventDraft,
  eventWhenLabel,
  fieldErrorsFromApi,
  fromReminderDraft,
  moveStart,
  newDraft,
  parseDay,
  parseView,
  readDraft,
  readOnlyReason,
  remindersSummary,
  reminderLabel,
  rescheduleRequest,
  sourceLabel,
  splitSearchResults,
  toEventInput,
  toReminderDraft,
  updateRequest,
  viewOf,
} from "./calendar-workspace";

const event = (
  id: string,
  fields: Partial<CalendarEvent> & { allDay: boolean },
): CalendarEvent => ({
  id,
  title: `Event ${id}`,
  status: "active",
  source: { kind: "manual" },
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  ...fields,
});

const timed = event("timed", {
  allDay: false,
  startAt: "2026-10-16T13:00:00Z",
  endAt: "2026-10-16T14:00:00Z",
});
const trip = event("trip", {
  allDay: true,
  startDate: "2026-10-16",
  endDate: "2026-10-18",
});

const draft = (fields: Partial<EventDraft>): EventDraft => ({
  title: "Dentist",
  allDay: false,
  startDate: "2026-10-16",
  startTime: "15:00",
  endDate: "2026-10-16",
  endTime: "16:00",
  timeZone: "",
  location: "",
  notes: "",
  reminderMode: "default",
  reminders: [],
  ...fields,
});

test("views and days from the URL are validated", () => {
  assert.equal(parseView("week"), "week");
  assert.equal(parseView("timeGridWeek"), undefined);
  assert.equal(parseView(undefined), undefined);
  assert.equal(viewOf("listAgenda"), "list");
  assert.equal(viewOf("dayGridMonth"), "month");
  assert.equal(parseDay("2027-01-14"), "2027-01-14");
  assert.equal(parseDay("2027-02-30"), undefined);
  assert.equal(parseDay(["2027-01-14"]), undefined);
});

test("all-day events end one day later in FullCalendar, which is exclusive", () => {
  const input = toEventInput(trip);
  assert.equal(input?.allDay, true);
  assert.equal(input?.start, "2026-10-16");
  assert.equal(input?.end, "2026-10-19");
  const single = toEventInput({ ...trip, endDate: "2026-10-16" });
  assert.equal(single?.end, "2026-10-17");
});

test("timed events keep their instants; only Life OS events can be dragged", () => {
  const input = toEventInput(timed);
  assert.equal(input?.start, "2026-10-16T13:00:00Z");
  assert.equal(input?.end, "2026-10-16T14:00:00Z");
  assert.equal(input?.editable, true);
  assert.deepEqual(input?.classNames, ["lifeos-event"]);
  assert.deepEqual(input?.extendedProps, { event: timed });

  const imported = toEventInput({
    ...timed,
    source: { kind: "ics", sourceId: "s", externalUid: "u" },
  });
  assert.equal(imported?.editable, false);
  assert.deepEqual(imported?.classNames, [
    "lifeos-event",
    "lifeos-event-imported",
  ]);
  const cancelled = toEventInput({ ...timed, status: "cancelled" });
  assert.equal(cancelled?.editable, false);
  assert.ok(
    (cancelled?.classNames as string[]).includes("lifeos-event-cancelled"),
  );
  const recurring = toEventInput({
    ...timed,
    recurrence: { frequency: "weekly", interval: 1 },
  });
  assert.equal(recurring?.editable, false);
  assert.equal(toEventInput({ ...timed, startAt: null }), undefined);
});

test("a dragged all-day event is stored with an inclusive last day", () => {
  // FullCalendar's dates are the browser's local midnights.
  assert.deepEqual(
    rescheduleRequest(
      {
        allDay: true,
        start: new Date(2026, 9, 20),
        end: new Date(2026, 9, 23),
      },
      trip,
    ),
    { allDay: true, startDate: "2026-10-20", endDate: "2026-10-22" },
  );
  assert.deepEqual(
    rescheduleRequest(
      { allDay: true, start: new Date(2026, 9, 20), end: null },
      timed,
    ),
    { allDay: true, startDate: "2026-10-20", endDate: "2026-10-20" },
  );
});

test("a timed event dropped without an end keeps its length, else an hour", () => {
  const start = new Date("2026-10-20T08:00:00Z");
  assert.deepEqual(
    rescheduleRequest({ allDay: false, start, end: null }, timed),
    {
      allDay: false,
      startAt: "2026-10-20T08:00:00.000Z",
      endAt: "2026-10-20T09:00:00.000Z",
    },
  );
  const long = { ...timed, endAt: "2026-10-16T16:00:00Z" };
  assert.equal(
    rescheduleRequest({ allDay: false, start, end: null }, long).endAt,
    "2026-10-20T11:00:00.000Z",
  );
  assert.equal(
    rescheduleRequest({ allDay: false, start, end: null }, trip).endAt,
    "2026-10-20T09:00:00.000Z",
  );
});

test("event times read in the owner's zone, with the year when it differs", () => {
  const today = "2026-10-10";
  assert.equal(
    eventWhenLabel(timed, "Europe/Berlin", today),
    "Fri 16 Oct · 15:00–16:00",
  );
  assert.equal(
    eventWhenLabel(timed, "America/New_York", today),
    "Fri 16 Oct · 09:00–10:00",
  );
  const late = {
    ...timed,
    startAt: "2026-10-16T21:30:00Z",
    endAt: "2026-10-16T22:30:00Z",
  };
  assert.equal(
    eventWhenLabel(late, "Europe/Berlin", today),
    "Fri 16 Oct · 23:30 – Sat 17 Oct · 00:30",
  );
  const nextYear = {
    ...timed,
    startAt: "2027-01-14T09:00:00Z",
    endAt: "2027-01-14T10:00:00Z",
  };
  assert.equal(
    eventWhenLabel(nextYear, "Europe/Berlin", today),
    "Thu 14 Jan 2027 · 10:00–11:00",
  );
  assert.equal(
    eventWhenLabel(trip, "America/Los_Angeles", today),
    "Fri 16 Oct – Sun 18 Oct · All day",
  );
  assert.equal(
    eventWhenLabel({ ...trip, endDate: "2026-10-16" }, "UTC", "2027-03-01"),
    "Fri 16 Oct 2026 · All day",
  );
});

test("reminders read as words; null follows the defaults, [] means none", () => {
  assert.equal(
    reminderLabel({ kind: "relative", minutesBefore: 0 }),
    "At the start",
  );
  assert.equal(
    reminderLabel({ kind: "relative", minutesBefore: 30 }),
    "30 minutes before",
  );
  assert.equal(
    reminderLabel({ kind: "relative", minutesBefore: 120 }),
    "2 hours before",
  );
  assert.equal(
    reminderLabel({ kind: "relative", minutesBefore: 1440 }),
    "1 day before",
  );
  assert.equal(
    reminderLabel({ kind: "localTime", daysBefore: 1, localTime: "19:00" }),
    "1 day before at 19:00",
  );
  assert.equal(
    reminderLabel({ kind: "localTime", daysBefore: 0, localTime: "08:00" }),
    "On the day at 08:00",
  );
  assert.deepEqual(remindersSummary(null), ["Default reminders"]);
  assert.deepEqual(remindersSummary(undefined), ["Default reminders"]);
  assert.deepEqual(remindersSummary([]), ["No reminders"]);
});

test("reminder rows round-trip through the form", () => {
  for (const reminder of [
    { kind: "relative" as const, minutesBefore: 45 },
    { kind: "relative" as const, minutesBefore: 180 },
    { kind: "relative" as const, minutesBefore: 2880 },
    { kind: "relative" as const, minutesBefore: 0 },
    { kind: "localTime" as const, daysBefore: 1, localTime: "19:00" },
  ]) {
    assert.deepEqual(fromReminderDraft(toReminderDraft(reminder)), reminder);
  }
  assert.deepEqual(toReminderDraft({ kind: "relative", minutesBefore: 180 }), {
    kind: "relative",
    amount: "3",
    unit: "hours",
  });
  assert.equal(
    fromReminderDraft({ kind: "relative", amount: "1.5", unit: "hours" }),
    undefined,
  );
  assert.equal(
    fromReminderDraft({ kind: "localTime", daysBefore: "1", time: "" }),
    undefined,
  );
});

test("an event fills the form in its own zone, else the owner's", () => {
  const berlin = draftFromEvent(timed, "Europe/Berlin");
  assert.equal(berlin.startDate, "2026-10-16");
  assert.equal(berlin.startTime, "15:00");
  assert.equal(berlin.endTime, "16:00");
  assert.equal(berlin.timeZone, "");
  assert.equal(berlin.reminderMode, "default");

  const scheduled = draftFromEvent(
    {
      ...timed,
      timeZone: "America/New_York",
      reminders: [{ kind: "relative", minutesBefore: 10 }],
    },
    "Europe/Berlin",
  );
  assert.equal(scheduled.startTime, "09:00");
  assert.equal(scheduled.timeZone, "America/New_York");
  assert.equal(scheduled.reminderMode, "custom");
  assert.equal(scheduled.reminders.length, 1);

  const allDay = draftFromEvent({ ...trip, reminders: [] }, "Europe/Berlin");
  assert.equal(allDay.allDay, true);
  assert.equal(allDay.startDate, "2026-10-16");
  assert.equal(allDay.endDate, "2026-10-18");
  assert.equal(allDay.reminderMode, "none");
});

test("a new event starts at the next full hour, or the range picked", () => {
  const now = new Date("2026-10-10T08:20:00Z");
  const next = newDraft("Europe/Berlin", now);
  assert.deepEqual(
    [next.startDate, next.startTime, next.endDate, next.endTime],
    ["2026-10-10", "11:00", "2026-10-10", "12:00"],
  );
  const days = newDraft("Europe/Berlin", now, {
    allDay: true,
    start: new Date(2026, 9, 14),
    end: new Date(2026, 9, 16),
  });
  assert.equal(days.allDay, true);
  assert.equal(days.startDate, "2026-10-14");
  assert.equal(days.endDate, "2026-10-15");
  const slot = newDraft("UTC", now, {
    allDay: false,
    start: new Date("2026-10-14T09:00:00Z"),
    end: new Date("2026-10-14T09:30:00Z"),
  });
  assert.deepEqual([slot.startTime, slot.endTime], ["09:00", "10:00"]);
});

test("moving the start keeps the event's length", () => {
  const moved = moveStart(draft({}), {
    startDate: "2026-10-16",
    startTime: "23:30",
  });
  assert.deepEqual([moved.endDate, moved.endTime], ["2026-10-17", "00:30"]);
  const days = moveStart(
    draft({ allDay: true, startDate: "2026-10-16", endDate: "2026-10-18" }),
    { startDate: "2026-10-20", startTime: "09:00" },
  );
  assert.equal(days.endDate, "2026-10-22");
});

test("the form is read in the zone its times are in", () => {
  const read = readDraft(
    draft({ location: " Praxis ", notes: "" }),
    "Europe/Berlin",
  );
  assert.ok("fields" in read);
  assert.deepEqual(read.fields, {
    title: "Dentist",
    description: null,
    location: "Praxis",
    reminders: null,
    allDay: false,
    startAt: "2026-10-16T13:00:00.000Z",
    endAt: "2026-10-16T14:00:00.000Z",
    timeZone: null,
  });
  const scheduled = readDraft(
    draft({ timeZone: "America/New_York" }),
    "Europe/Berlin",
  );
  assert.ok("fields" in scheduled);
  assert.equal(
    scheduled.fields.allDay === false && scheduled.fields.startAt,
    "2026-10-16T19:00:00.000Z",
  );
});

test("the form checks what it can and leaves the rest to the API", () => {
  const missing = readDraft(
    draft({ title: " ", startTime: "", endDate: "" }),
    "UTC",
  );
  assert.ok("errors" in missing);
  assert.deepEqual(Object.keys(missing.errors).sort(), [
    "end",
    "start",
    "title",
  ]);
  // An end before the start is the API's call (400), shown at the field.
  assert.ok("fields" in readDraft(draft({ endTime: "14:00" }), "UTC"));
  const reminder = readDraft(
    draft({
      reminderMode: "custom",
      reminders: [{ kind: "relative", amount: "", unit: "minutes" }],
    }),
    "UTC",
  );
  assert.ok("errors" in reminder && reminder.errors.reminders);
});

test("all-day forms keep an inclusive last day", () => {
  const read = readDraft(
    draft({ allDay: true, startDate: "2026-10-16", endDate: "" }),
    "UTC",
  );
  assert.ok("fields" in read);
  assert.deepEqual(createRequest(read.fields), {
    title: "Dentist",
    allDay: true,
    startDate: "2026-10-16",
    endDate: "2026-10-16",
  });
});

test("creating leaves out empty fields; updating clears them", () => {
  const read = readDraft(
    draft({ reminderMode: "none", timeZone: "Europe/Berlin" }),
    "UTC",
  );
  assert.ok("fields" in read);
  assert.deepEqual(createRequest(read.fields), {
    title: "Dentist",
    allDay: false,
    reminders: [],
    startAt: "2026-10-16T13:00:00.000Z",
    endAt: "2026-10-16T14:00:00.000Z",
    timeZone: "Europe/Berlin",
  });
  const cleared = readDraft(draft({}), "UTC");
  assert.ok("fields" in cleared);
  assert.deepEqual(updateRequest(cleared.fields), {
    title: "Dentist",
    description: null,
    location: null,
    reminders: null,
    allDay: false,
    startAt: "2026-10-16T15:00:00.000Z",
    endAt: "2026-10-16T16:00:00.000Z",
    timeZone: null,
  });
  const allDay = readDraft(
    draft({ allDay: true, endDate: "2026-10-17" }),
    "UTC",
  );
  assert.ok("fields" in allDay);
  const body = updateRequest(allDay.fields);
  assert.equal(body.allDay, true);
  assert.equal(body.endDate, "2026-10-17");
  assert.equal("timeZone" in body, false);
});

test("API messages land at the field they concern", () => {
  assert.deepEqual(fieldErrorsFromApi("endAt must be after startAt"), {
    end: "The end must be after the start.",
  });
  assert.deepEqual(fieldErrorsFromApi("endDate must not precede startDate"), {
    end: "The end date can't be before the start date.",
  });
  assert.deepEqual(fieldErrorsFromApi("title must not be empty"), {
    title: "Title must not be empty.",
  });
  assert.deepEqual(
    fieldErrorsFromApi("minutesBefore must be between 0 and 40320 (28 days)"),
    { reminders: "MinutesBefore must be between 0 and 40320 (28 days)." },
  );
  assert.deepEqual(fieldErrorsFromApi("invalid event request"), {
    form: "Invalid event request.",
  });
  assert.equal(
    apiErrorMessage({ error: { code: "bad_request", message: "nope" } }),
    "nope",
  );
  assert.equal(apiErrorMessage("nope"), undefined);
  assert.equal(apiErrorMessage(undefined), undefined);
});

test("search results list upcoming events soonest first, then past ones", () => {
  const now = new Date("2026-10-10T12:00:00Z");
  const at = (id: string, startAt: string) =>
    event(id, {
      allDay: false,
      startAt,
      endAt: new Date(Date.parse(startAt) + 3_600_000).toISOString(),
    });
  const { upcoming, past } = splitSearchResults(
    [
      at("far", "2027-01-14T09:00:00Z"),
      at("old", "2026-06-01T09:00:00Z"),
      at("soon", "2026-10-11T09:00:00Z"),
      at("recent", "2026-10-01T09:00:00Z"),
      // Started, not over: still upcoming.
      at("now", "2026-10-10T11:30:00Z"),
      event("today", {
        allDay: true,
        startDate: "2026-10-10",
        endDate: "2026-10-10",
      }),
      event("yesterday", {
        allDay: true,
        startDate: "2026-10-09",
        endDate: "2026-10-09",
      }),
    ],
    now,
    "Europe/Berlin",
  );
  assert.deepEqual(
    upcoming.map((item) => item.id),
    ["today", "now", "soon", "far"],
  );
  assert.deepEqual(
    past.map((item) => item.id),
    ["yesterday", "recent", "old"],
  );
});

test("imported events name their source and why they are read-only", () => {
  const ics = {
    ...timed,
    source: { kind: "ics" as const, sourceId: "s", externalUid: "u" },
  };
  assert.equal(sourceLabel(ics), "ICS feed");
  assert.match(readOnlyReason(ics) ?? "", /^Imported from an ICS feed/);
  assert.equal(
    sourceLabel({ ...timed, source: { kind: "google" } }),
    "Google Calendar",
  );
  assert.equal(sourceLabel(timed), undefined);
  assert.equal(readOnlyReason(timed), undefined);
});
