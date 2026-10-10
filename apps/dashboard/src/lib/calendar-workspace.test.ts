import assert from "node:assert/strict";
import test from "node:test";

import { NO_REPEAT } from "./calendar-recurrence";
import {
  apiErrorMessage,
  type CalendarEvent,
  type CalendarSource,
  createRequest,
  defaultPriorityNote,
  defaultRemindersNote,
  deleteScopes,
  draftFromEvent,
  editScopes,
  type EventDraft,
  type EventFields,
  eventRecurrenceLabel,
  eventWhenLabel,
  fieldErrorsFromApi,
  formDefaults,
  fromReminderDraft,
  movedFields,
  movedSeries,
  moveStart,
  newDraft,
  occurrenceEdit,
  occurrenceKey,
  occurrenceRequest,
  parseDay,
  parseView,
  priorityBadge,
  readDraft,
  readOnlyReason,
  readReminders,
  reminderAndPriorityChanges,
  reminderChanges,
  reminderLabel,
  reminderLines,
  rescheduleRequest,
  sameReminders,
  seriesRequest,
  settingsRequest,
  sourceDefaultsRequest,
  sourceLabel,
  splitRequest,
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
  reminders: [],
  useDefaultReminders: true,
  defaultReminders: [],
  effectiveReminders: [],
  priority: "medium",
  defaultPriority: "medium",
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
  useDefaultReminders: true,
  reminders: [],
  priority: "medium",
  repeat: NO_REPEAT,
  ...fields,
});

const halfHour = { kind: "relative" as const, minutesBefore: 30 };
const threeHours = { kind: "relative" as const, minutesBefore: 180 };
const evening = {
  kind: "localTime" as const,
  daysBefore: 1,
  localTime: "19:00",
};
const zawSource = {
  kind: "ics" as const,
  sourceId: "s",
  externalUid: "u",
  name: "ZAW",
};

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
  // An occurrence can be dragged: the owner then picks which ones move.
  const recurring = toEventInput({
    ...timed,
    recurrence: { frequency: "weekly", interval: 1 },
  });
  assert.equal(recurring?.editable, true);
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
  const endAt = (original: CalendarEvent) => {
    const times = rescheduleRequest(
      { allDay: false, start, end: null },
      original,
    );
    return times.allDay ? undefined : times.endAt;
  };
  const long = { ...timed, endAt: "2026-10-16T16:00:00Z" };
  assert.equal(endAt(long), "2026-10-20T11:00:00.000Z");
  assert.equal(endAt(trip), "2026-10-20T09:00:00.000Z");
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

test("reminders read as words", () => {
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
});

test("the details list every reminder that applies and where defaults come from", () => {
  // A Life OS event: the owner's default, then its own reminders.
  assert.deepEqual(
    reminderLines({
      ...timed,
      reminders: [threeHours, halfHour],
      defaultReminders: [halfHour],
      effectiveReminders: [halfHour, threeHours],
    }),
    [
      { label: "30 minutes before", origin: "default" },
      { label: "3 hours before" },
    ],
  );
  // An imported event's defaults are its source's.
  assert.deepEqual(
    reminderLines({
      ...timed,
      source: zawSource,
      reminders: [halfHour],
      defaultReminders: [evening],
      effectiveReminders: [evening, halfHour],
    }),
    [
      { label: "1 day before at 19:00", origin: "ZAW default" },
      { label: "30 minutes before" },
    ],
  );
  // Defaults turned off: an own reminder equal to a default is just own.
  assert.deepEqual(
    reminderLines({
      ...timed,
      reminders: [halfHour],
      useDefaultReminders: false,
      defaultReminders: [halfHour],
      effectiveReminders: [halfHour],
    }),
    [{ label: "30 minutes before" }],
  );
  assert.deepEqual(reminderLines(timed), []);
});

test("the form offers the source's defaults, else the owner's for the event's kind", () => {
  const owner = { timed: [halfHour], allDay: [evening] };
  assert.deepEqual(formDefaults(undefined, false, owner), [halfHour]);
  assert.deepEqual(formDefaults(undefined, true, owner), [evening]);
  assert.equal(formDefaults(undefined, true, "loading"), "loading");
  const imported = {
    ...timed,
    source: zawSource,
    defaultReminders: [evening],
  };
  assert.deepEqual(formDefaults(imported, false, owner), [evening]);
  assert.deepEqual(formDefaults(imported, false, "unavailable"), [evening]);
  // While the settings load, an event's own defaults stand in for its kind.
  const own = { ...timed, defaultReminders: [threeHours] };
  assert.deepEqual(formDefaults(own, false, "loading"), [threeHours]);
  assert.equal(formDefaults(own, true, "loading"), "loading");
  assert.deepEqual(formDefaults(own, true, owner), [evening]);
});

test("the defaults note lists what would apply, or where to set some", () => {
  assert.equal(
    defaultRemindersNote([evening], { allDay: false, source: "ZAW" }),
    "1 day before at 19:00 (ZAW)",
  );
  assert.equal(
    defaultRemindersNote([halfHour], { allDay: false }),
    "30 minutes before (your default for timed events)",
  );
  assert.equal(
    defaultRemindersNote([evening, halfHour], { allDay: true }),
    "1 day before at 19:00, 30 minutes before (your defaults for all-day events)",
  );
  assert.equal(
    defaultRemindersNote([], { allDay: true }),
    "No default reminders for all-day events yet; set them in Reminder defaults.",
  );
  assert.equal(
    defaultRemindersNote([], { allDay: false, source: "ZAW" }),
    "ZAW has no default reminders yet; set them in Reminder defaults.",
  );
  assert.equal(
    defaultRemindersNote("loading", { allDay: false }),
    "Loading your default reminders…",
  );
  assert.equal(
    defaultRemindersNote("unavailable", { allDay: false }),
    "Your default reminders could not be loaded.",
  );
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
  assert.deepEqual(
    readReminders([
      { kind: "relative", amount: "3", unit: "hours" },
      { kind: "localTime", daysBefore: "1", time: "19:00" },
    ]),
    { reminders: [threeHours, evening] },
  );
  assert.deepEqual(
    readReminders([
      { kind: "relative", amount: "3", unit: "hours" },
      { kind: "relative", amount: "", unit: "minutes" },
    ]),
    {
      error:
        "Complete reminder 2: a whole number and, for a day reminder, a time.",
    },
  );
  assert.ok(sameReminders(null, []));
  assert.ok(sameReminders([evening], [{ ...evening }]));
  assert.ok(!sameReminders([halfHour, evening], [evening, halfHour]));
  assert.ok(!sameReminders([halfHour], [{ ...halfHour, minutesBefore: 31 }]));
});

test("an event fills the form in its own zone, else the owner's", () => {
  const berlin = draftFromEvent(timed, "Europe/Berlin");
  assert.equal(berlin.startDate, "2026-10-16");
  assert.equal(berlin.startTime, "15:00");
  assert.equal(berlin.endTime, "16:00");
  assert.equal(berlin.timeZone, "");
  assert.equal(berlin.useDefaultReminders, true);
  assert.deepEqual(berlin.reminders, []);

  const scheduled = draftFromEvent(
    {
      ...timed,
      timeZone: "America/New_York",
      reminders: [{ kind: "relative", minutesBefore: 10 }, evening],
    },
    "Europe/Berlin",
  );
  assert.equal(scheduled.startTime, "09:00");
  assert.equal(scheduled.timeZone, "America/New_York");
  assert.deepEqual(scheduled.reminders, [
    { kind: "relative", amount: "10", unit: "minutes" },
    { kind: "localTime", daysBefore: "1", time: "19:00" },
  ]);

  const allDay = draftFromEvent(
    { ...trip, useDefaultReminders: false },
    "Europe/Berlin",
  );
  assert.equal(allDay.allDay, true);
  assert.equal(allDay.startDate, "2026-10-16");
  assert.equal(allDay.endDate, "2026-10-18");
  assert.equal(allDay.useDefaultReminders, false);
});

test("a new event starts at the next full hour, or the range picked", () => {
  const now = new Date("2026-10-10T08:20:00Z");
  const next = newDraft("Europe/Berlin", now);
  assert.deepEqual(
    [next.startDate, next.startTime, next.endDate, next.endTime],
    ["2026-10-10", "11:00", "2026-10-10", "12:00"],
  );
  assert.equal(next.useDefaultReminders, true);
  assert.deepEqual(next.reminders, []);
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
    reminders: [],
    useDefaultReminders: true,
    priority: "medium",
    recurrence: null,
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
    draft({ reminders: [{ kind: "relative", amount: "", unit: "minutes" }] }),
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
    useDefaultReminders: true,
    startDate: "2026-10-16",
    endDate: "2026-10-16",
  });
});

test("creating leaves out empty fields; updating clears them", () => {
  const read = readDraft(
    draft({ useDefaultReminders: false, timeZone: "Europe/Berlin" }),
    "UTC",
  );
  assert.ok("fields" in read);
  assert.deepEqual(createRequest(read.fields), {
    title: "Dentist",
    allDay: false,
    useDefaultReminders: false,
    startAt: "2026-10-16T13:00:00.000Z",
    endAt: "2026-10-16T14:00:00.000Z",
    timeZone: "Europe/Berlin",
  });
  const cleared = readDraft(draft({}), "UTC");
  assert.ok("fields" in cleared);
  assert.deepEqual(updateRequest(cleared.fields, timed), {
    title: "Dentist",
    description: null,
    location: null,
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
  const body = updateRequest(allDay.fields, timed);
  assert.equal(body.allDay, true);
  assert.equal(body.endDate, "2026-10-17");
  assert.equal("timeZone" in body, false);
});

test("several own reminders are created alongside the defaults", () => {
  const read = readDraft(
    draft({
      reminders: [
        { kind: "relative", amount: "1", unit: "days" },
        { kind: "relative", amount: "3", unit: "hours" },
        { kind: "relative", amount: "30", unit: "minutes" },
      ],
    }),
    "UTC",
  );
  assert.ok("fields" in read);
  const body = createRequest(read.fields);
  assert.deepEqual(body.reminders, [
    { kind: "relative", minutesBefore: 1440 },
    threeHours,
    halfHour,
  ]);
  assert.equal(body.useDefaultReminders, true);
});

test("a PATCH names only the reminder fields that changed", () => {
  const stored = { ...timed, reminders: [halfHour] };
  assert.deepEqual(
    reminderChanges(
      { reminders: [halfHour], useDefaultReminders: true },
      stored,
    ),
    {},
  );
  assert.deepEqual(
    reminderChanges(
      { reminders: [halfHour, threeHours], useDefaultReminders: true },
      stored,
    ),
    { reminders: [halfHour, threeHours] },
  );
  // Emptying the list sends [] (no own reminders), not null.
  assert.deepEqual(
    reminderChanges({ reminders: [], useDefaultReminders: false }, stored),
    { reminders: [], useDefaultReminders: false },
  );
  const read = readDraft(
    draft({ reminders: [{ kind: "relative", amount: "3", unit: "hours" }] }),
    "UTC",
  );
  assert.ok("fields" in read);
  assert.deepEqual(updateRequest(read.fields, stored).reminders, [threeHours]);
  assert.equal(
    "useDefaultReminders" in updateRequest(read.fields, stored),
    false,
  );
});

test("Reminder defaults saves only the lists that changed", () => {
  const saved = { timed: [halfHour], allDay: [evening] };
  assert.equal(
    settingsRequest(saved, { timed: [halfHour], allDay: [evening] }),
    undefined,
  );
  assert.deepEqual(
    settingsRequest(saved, {
      timed: [halfHour, threeHours],
      allDay: [evening],
    }),
    { defaultReminders: { timed: [halfHour, threeHours] } },
  );
  assert.deepEqual(settingsRequest(saved, { timed: [], allDay: [] }), {
    defaultReminders: { timed: [], allDay: [] },
  });
});

// Priority --------------------------------------------------------------------

test("details name high and low priority, not the usual medium", () => {
  assert.equal(priorityBadge({ priority: "high" }), "High priority");
  assert.equal(priorityBadge({ priority: "low" }), "Low priority");
  assert.equal(priorityBadge({ priority: "medium" }), undefined);
});

test("high-priority events are marked on the calendar", () => {
  assert.ok(
    toEventInput({ ...timed, priority: "high" })?.classNames?.includes(
      "lifeos-event-high",
    ),
  );
  for (const priority of ["medium", "low"] as const) {
    assert.ok(
      !toEventInput({ ...timed, priority })?.classNames?.includes(
        "lifeos-event-high",
      ),
    );
  }
});

test("the form edits the priority and sends it only when it changed", () => {
  assert.equal(newDraft("UTC", new Date()).priority, "medium");
  const urgent = { ...timed, priority: "high" as const };
  assert.equal(draftFromEvent(urgent, "UTC").priority, "high");

  // Created: medium (the default) is left out.
  const plain = readDraft(draft({}), "UTC");
  assert.ok("fields" in plain);
  assert.equal("priority" in createRequest(plain.fields), false);
  const high = readDraft(draft({ priority: "high" }), "UTC");
  assert.ok("fields" in high);
  assert.equal(high.fields.priority, "high");
  assert.equal(createRequest(high.fields).priority, "high");

  // Updated: only a change is sent.
  assert.equal(updateRequest(high.fields, urgent).priority, undefined);
  assert.equal(updateRequest(plain.fields, urgent).priority, "medium");
  assert.equal(updateRequest(high.fields, timed).priority, "high");
});

test("an imported event sends only its reminder and priority changes", () => {
  const zaw = {
    ...timed,
    source: zawSource,
    priority: "high" as const,
    defaultPriority: "high" as const,
  };
  const fields = { reminders: [], useDefaultReminders: true };
  assert.deepEqual(
    reminderAndPriorityChanges({ ...fields, priority: "high" }, zaw),
    {},
  );
  assert.deepEqual(
    reminderAndPriorityChanges(
      { ...fields, useDefaultReminders: false, priority: "low" },
      zaw,
    ),
    { useDefaultReminders: false, priority: "low" },
  );
  assert.equal(
    defaultPriorityNote(zaw),
    "ZAW events are high priority unless you choose another.",
  );
  assert.equal(defaultPriorityNote(timed), undefined);
});

test("Reminder defaults saves a source's changed default priority", () => {
  const source = {
    id: "s",
    defaultReminders: [evening],
    defaultPriority: "medium",
  } as CalendarSource;
  assert.equal(
    sourceDefaultsRequest(source, { reminders: [evening], priority: "medium" }),
    undefined,
  );
  assert.deepEqual(
    sourceDefaultsRequest(source, { reminders: [evening], priority: "high" }),
    { defaultPriority: "high" },
  );
  assert.deepEqual(
    sourceDefaultsRequest(source, { reminders: [], priority: "low" }),
    { defaultReminders: [], defaultPriority: "low" },
  );
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
  // Calendar names the source, such as the ZAW waste-collection feed.
  const zaw = { ...timed, source: { ...ics.source, name: "ZAW" } };
  assert.equal(sourceLabel(zaw), "ZAW");
  assert.match(
    readOnlyReason(zaw) ?? "",
    /^Imported from ZAW, so it can only be changed there\. Its reminders/,
  );
  assert.equal(
    sourceLabel({ ...timed, source: { ...ics.source, name: " " } }),
    "ICS feed",
  );
  assert.equal(
    sourceLabel({ ...timed, source: { kind: "google" } }),
    "Google Calendar",
  );
  assert.equal(sourceLabel(timed), undefined);
  assert.equal(readOnlyReason(timed), undefined);
});

test("occurrences of a recurring event get distinct keys", () => {
  const base = { id: "e1" } as Parameters<typeof occurrenceKey>[0];
  assert.equal(occurrenceKey(base), "e1");
  assert.equal(
    occurrenceKey({
      ...base,
      occurrence: {
        seriesId: "e1",
        recurrenceId: "20261017T070000Z",
        modified: false,
      },
    }),
    "e1:20261017T070000Z",
  );
});

// Recurring events ------------------------------------------------------------

const BERLIN = "Europe/Berlin";

// Gym on Mondays and Wednesdays, 18:00–19:00 in Berlin, from Mon 5 Oct 2026
// (summer time), as Calendar stores the series.
const gym = event("gym", {
  allDay: false,
  title: "Gym",
  startAt: "2026-10-05T16:00:00Z",
  endAt: "2026-10-05T17:00:00Z",
  recurrence: { frequency: "weekly", interval: 1, byWeekday: ["MO", "WE"] },
});
// Its occurrence on Wed 4 Nov, after the clocks went back (CET).
const gymNov: CalendarEvent = {
  ...gym,
  startAt: "2026-11-04T17:00:00Z",
  endAt: "2026-11-04T18:00:00Z",
  occurrence: {
    seriesId: "gym",
    recurrenceId: "2026-11-04T17:00:00Z",
    modified: false,
  },
};
// A birthday, every 17 September, and its occurrence in 2027.
const birthday = event("birthday", {
  allDay: true,
  title: "Anna's birthday",
  startDate: "2026-09-17",
  endDate: "2026-09-17",
  recurrence: { frequency: "yearly", interval: 1 },
});
const birthday2027: CalendarEvent = {
  ...birthday,
  startDate: "2027-09-17",
  endDate: "2027-09-17",
  occurrence: {
    seriesId: "birthday",
    recurrenceId: "2027-09-17",
    modified: false,
  },
};

/** The form's fields for an occurrence, after `edit` changes the draft. */
function edited(
  occurrence: CalendarEvent,
  edit: Partial<EventDraft> = {},
): EventFields {
  const read = readDraft(
    { ...draftFromEvent(occurrence, BERLIN), ...edit },
    BERLIN,
  );
  assert.ok("fields" in read);
  return read.fields;
}

const options = { repeatChanged: false, ownerZone: BERLIN };

test("an occurrence says how its series repeats, from its original start", () => {
  assert.equal(
    eventRecurrenceLabel(gymNov, BERLIN),
    "Weekly on Monday and Wednesday",
  );
  // Moved to Tuesday on its own, it still belongs to a Monday series.
  const weekly = {
    ...gymNov,
    recurrence: { frequency: "weekly" as const, interval: 1 },
    startAt: "2026-11-03T17:00:00Z",
    endAt: "2026-11-03T18:00:00Z",
    occurrence: {
      seriesId: "gym",
      recurrenceId: "2026-11-02T17:00:00Z",
      modified: true,
    },
  };
  assert.equal(eventRecurrenceLabel(weekly, BERLIN), "Weekly on Monday");
  assert.equal(
    eventRecurrenceLabel(birthday2027, BERLIN),
    "Annually on September 17",
  );
  // The stored series reads from its own start.
  assert.equal(
    eventRecurrenceLabel(birthday, BERLIN),
    "Annually on September 17",
  );
  assert.equal(eventRecurrenceLabel(timed, BERLIN), undefined);
});

test("the form fills Repeat from the series and reads it back", () => {
  const form = draftFromEvent(gymNov, BERLIN);
  assert.equal(form.startDate, "2026-11-04");
  assert.equal(form.repeat.preset, "custom");
  // Wednesday is the start's day; Monday is chosen besides.
  assert.deepEqual(form.repeat.days, ["MO"]);
  assert.deepEqual(edited(gymNov).recurrence, {
    frequency: "weekly",
    interval: 1,
    byWeekday: ["MO", "WE"],
  });
  assert.equal(draftFromEvent(birthday2027, BERLIN).repeat.preset, "yearly");
  assert.equal(draftFromEvent(timed, BERLIN).repeat.preset, "none");
  assert.equal(newDraft(BERLIN, new Date()).repeat.preset, "none");
});

test("a new event is created with its repeat", () => {
  const read = readDraft(
    draft({
      title: "Anna's birthday",
      allDay: true,
      startDate: "2026-09-17",
      endDate: "2026-09-17",
      repeat: { ...NO_REPEAT, preset: "yearly" },
    }),
    BERLIN,
  );
  assert.ok("fields" in read);
  assert.deepEqual(createRequest(read.fields), {
    title: "Anna's birthday",
    allDay: true,
    useDefaultReminders: true,
    recurrence: { frequency: "yearly", interval: 1 },
    startDate: "2026-09-17",
    endDate: "2026-09-17",
  });
  // Every 2 weeks until the end of 31 December in the event's zone.
  const custom = readDraft(
    draft({
      repeat: {
        ...NO_REPEAT,
        preset: "custom",
        frequency: "weekly",
        interval: "2",
        ends: "on",
        until: "2026-12-31",
      },
    }),
    BERLIN,
  );
  assert.ok("fields" in custom);
  assert.deepEqual(custom.fields.recurrence, {
    frequency: "weekly",
    interval: 2,
    until: "2026-12-31T22:59:59Z",
  });
  const invalid = readDraft(
    draft({ repeat: { ...NO_REPEAT, preset: "custom", interval: "0" } }),
    BERLIN,
  );
  assert.ok("errors" in invalid && invalid.errors.repeat);
  // A one-off event that starts repeating says so; one that doesn't, doesn't.
  const weekly = readDraft(
    draft({ repeat: { ...NO_REPEAT, preset: "weekly" } }),
    BERLIN,
  );
  assert.ok("fields" in weekly);
  assert.deepEqual(updateRequest(weekly.fields, timed).recurrence, {
    frequency: "weekly",
    interval: 1,
  });
});

test("This event sends only what changed in the occurrence", () => {
  assert.deepEqual(occurrenceEdit(edited(gymNov), gymNov, false), {
    changed: false,
    seriesOnly: false,
  });
  assert.deepEqual(
    occurrenceRequest(edited(gymNov, { title: "Gym (late)" }), gymNov),
    { title: "Gym (late)" },
  );
  assert.deepEqual(
    occurrenceRequest(
      edited(gymNov, { startTime: "19:00", endTime: "20:00" }),
      gymNov,
    ),
    { startAt: "2026-11-04T18:00:00.000Z", endAt: "2026-11-04T19:00:00.000Z" },
  );
  assert.deepEqual(
    occurrenceRequest(
      edited(gymNov, {
        location: "Gym West",
        reminders: [{ kind: "relative", amount: "1", unit: "hours" }],
      }),
      gymNov,
    ),
    {
      location: "Gym West",
      reminders: [{ kind: "relative", minutesBefore: 60 }],
    },
  );
  assert.deepEqual(
    occurrenceRequest(
      edited(birthday2027, { startDate: "2027-09-18", endDate: "2027-09-18" }),
      birthday2027,
    ),
    { startDate: "2027-09-18", endDate: "2027-09-18" },
  );
});

test("a priority change is offered for every scope, like other fields", () => {
  const important = edited(gymNov, { priority: "high" });
  const edit = occurrenceEdit(important, gymNov, false);
  assert.deepEqual(edit, { changed: true, seriesOnly: false });
  assert.deepEqual(editScopes(edit), ["this", "following", "all"]);
  assert.deepEqual(occurrenceRequest(important, gymNov), { priority: "high" });
  assert.deepEqual(splitRequest(important, gymNov, options), {
    priority: "high",
  });
  assert.deepEqual(seriesRequest(important, gymNov, gym, options), {
    priority: "high",
  });
  // A drag keeps the occurrence's priority.
  const urgent = { ...gymNov, priority: "high" as const };
  assert.equal(
    movedFields(
      {
        allDay: false,
        start: new Date("2026-11-04T18:00:00Z"),
        end: new Date("2026-11-04T19:00:00Z"),
      },
      urgent,
    ).priority,
    "high",
  );
});

test("Repeat, All day, and time zone changes aren't offered for This event", () => {
  const all = ["this", "following", "all"];
  assert.deepEqual(
    editScopes(
      occurrenceEdit(edited(gymNov, { title: "Swim" }), gymNov, false),
    ),
    all,
  );
  const repeat = occurrenceEdit(edited(gymNov), gymNov, true);
  assert.deepEqual(repeat, { changed: true, seriesOnly: true });
  assert.deepEqual(editScopes(repeat), ["following", "all"]);
  assert.equal(
    occurrenceEdit(edited(gymNov, { allDay: true }), gymNov, false).seriesOnly,
    true,
  );
  assert.equal(
    occurrenceEdit(edited(gymNov, { timeZone: "Europe/London" }), gymNov, false)
      .seriesOnly,
    true,
  );
  // A drag into the all-day row changes the kind too.
  const toAllDay = movedFields(
    { allDay: true, start: new Date(2026, 10, 4), end: new Date(2026, 10, 5) },
    gymNov,
  );
  assert.equal(occurrenceEdit(toAllDay, gymNov, false).seriesOnly, true);
  // Deleting: a skipped occurrence can't be skipped again.
  assert.deepEqual(deleteScopes(gymNov), all);
  assert.deepEqual(deleteScopes({ ...gymNov, status: "cancelled" }), [
    "following",
    "all",
  ]);
});

test("This and following starts the new series at the new time", () => {
  assert.deepEqual(
    splitRequest(
      edited(gymNov, { startTime: "19:00", endTime: "20:00" }),
      gymNov,
      options,
    ),
    { startAt: "2026-11-04T18:00:00.000Z", endAt: "2026-11-04T19:00:00.000Z" },
  );
  assert.deepEqual(
    splitRequest(edited(gymNov, { title: "Swim" }), gymNov, options),
    { title: "Swim" },
  );
  // A day later: its weekly days move with it.
  assert.deepEqual(
    splitRequest(
      edited(gymNov, { startDate: "2026-11-05", endDate: "2026-11-05" }),
      gymNov,
      options,
    ),
    {
      startAt: "2026-11-05T17:00:00.000Z",
      endAt: "2026-11-05T18:00:00.000Z",
      recurrence: { frequency: "weekly", interval: 1, byWeekday: ["TU", "TH"] },
    },
  );
  // A new repeat is sent as the form has it, also "Does not repeat".
  assert.deepEqual(
    splitRequest(
      edited(gymNov, { repeat: { ...NO_REPEAT, preset: "weekly" } }),
      gymNov,
      { ...options, repeatChanged: true },
    ),
    { recurrence: { frequency: "weekly", interval: 1 } },
  );
  assert.deepEqual(
    splitRequest(edited(gymNov, { repeat: NO_REPEAT }), gymNov, {
      ...options,
      repeatChanged: true,
    }),
    { recurrence: null },
  );
});

test("All events moves every occurrence by the dragged one's offset", () => {
  // Wed 4 Nov 18:00 dragged to Thu 5 Nov 19:00 (CET): the series, which
  // starts Mon 5 Oct 18:00 (CEST), starts Tue 6 Oct 19:00 on Tuesdays and
  // Thursdays.
  const dragged = movedFields(
    {
      allDay: false,
      start: new Date("2026-11-05T18:00:00Z"),
      end: new Date("2026-11-05T19:00:00Z"),
    },
    gymNov,
  );
  assert.deepEqual(seriesRequest(dragged, gymNov, gym, options), {
    startAt: "2026-10-06T17:00:00.000Z",
    endAt: "2026-10-06T18:00:00.000Z",
    recurrence: { frequency: "weekly", interval: 1, byWeekday: ["TU", "TH"] },
  });
  // An hour later the same day keeps the days; the wall clock, not the
  // instant, moves across the change to winter time.
  const later = movedFields(
    {
      allDay: false,
      start: new Date("2026-11-04T18:00:00Z"),
      end: new Date("2026-11-04T19:00:00Z"),
    },
    gymNov,
  );
  assert.deepEqual(seriesRequest(later, gymNov, gym, options), {
    startAt: "2026-10-05T17:00:00.000Z",
    endAt: "2026-10-05T18:00:00.000Z",
  });
  // Resized: every occurrence gets the new length.
  const longer = movedFields(
    {
      allDay: false,
      start: new Date("2026-11-04T17:00:00Z"),
      end: new Date("2026-11-04T18:30:00Z"),
    },
    gymNov,
  );
  assert.equal(
    seriesRequest(longer, gymNov, gym, options).endAt,
    "2026-10-05T17:30:00.000Z",
  );
  // A form edit of the title only leaves the times alone.
  assert.deepEqual(
    seriesRequest(edited(gymNov, { title: "Swim" }), gymNov, gym, options),
    { title: "Swim" },
  );
  // An all-day series moves by days.
  const nextDay = movedFields(
    { allDay: true, start: new Date(2027, 8, 18), end: new Date(2027, 8, 19) },
    birthday2027,
  );
  assert.deepEqual(movedSeries(nextDay, birthday2027, birthday, BERLIN), {
    schedule: { allDay: true, startDate: "2026-09-18", endDate: "2026-09-18" },
    days: 1,
  });
  assert.deepEqual(seriesRequest(nextDay, birthday2027, birthday, options), {
    startDate: "2026-09-18",
    endDate: "2026-09-18",
  });
});

test("the API's repeat messages land at Repeat", () => {
  assert.deepEqual(
    fieldErrorsFromApi(
      "recurrence byWeekday must include the weekday the event starts on (SA)",
    ),
    { repeat: "The repeat days must include Saturday, the day it starts." },
  );
  assert.deepEqual(
    fieldErrorsFromApi("recurrence until must not be before the event starts"),
    { repeat: "The repeat can't end before the event starts." },
  );
  assert.deepEqual(
    fieldErrorsFromApi("recurrence count must be between 1 and 1000"),
    { repeat: "Recurrence count must be between 1 and 1000." },
  );
  assert.deepEqual(
    fieldErrorsFromApi(
      "timeZone belongs to the series: change all events, or this and following",
    ),
    {
      timeZone:
        "TimeZone belongs to the series: change all events, or this and following.",
    },
  );
});
