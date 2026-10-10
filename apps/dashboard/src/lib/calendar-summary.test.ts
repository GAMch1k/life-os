import assert from "node:assert/strict";
import test from "node:test";

import { eventTimeLabel } from "../components/calendar-summary";
import {
  agendaWindow,
  toCalendarSummary,
  type CalendarEvent,
} from "./calendar-summary";

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

// 23:30 UTC on Friday is 01:30 on Saturday in Berlin (summer time).
const nearMidnight = new Date("2026-10-09T23:30:00Z");

test("the agenda window is the owner's today plus seven days", () => {
  const agenda = agendaWindow(nearMidnight, "Europe/Berlin");
  assert.equal(agenda.today, "2026-10-10");
  assert.equal(agenda.from.toISOString(), "2026-10-09T22:00:00.000Z");
  assert.equal(agenda.tomorrow.toISOString(), "2026-10-10T22:00:00.000Z");
  // Berlin leaves summer time on 2026-10-25, inside the window.
  assert.equal(agenda.to.toISOString(), "2026-10-17T22:00:00.000Z");
});

test("today means the owner's day for timed and all-day events", () => {
  const state = toCalendarSummary(
    [
      // Friday 21:00–22:00 Berlin: already over for the owner.
      event("friday-evening", {
        allDay: false,
        startAt: "2026-10-09T19:00:00Z",
        endAt: "2026-10-09T20:00:00Z",
      }),
      // Friday all day: the Calendar API returns it for a UTC Friday `from`.
      event("friday", {
        allDay: true,
        startDate: "2026-10-09",
        endDate: "2026-10-09",
      }),
      event("saturday-dentist", {
        allDay: false,
        startAt: "2026-10-10T13:00:00Z",
        endAt: "2026-10-10T14:00:00Z",
      }),
      event("weekend", {
        allDay: true,
        startDate: "2026-10-10",
        endDate: "2026-10-11",
      }),
      event("sunday", {
        allDay: false,
        startAt: "2026-10-10T22:30:00Z",
        endAt: "2026-10-10T23:00:00Z",
      }),
    ],
    nearMidnight,
    "Europe/Berlin",
  );
  assert.equal(state.kind, "ready");
  if (state.kind !== "ready") return;
  assert.deepEqual(
    state.today.map((item) => item.id),
    ["saturday-dentist", "weekend"],
  );
  // Both Friday events ended before the owner's today began.
  assert.deepEqual(
    state.upcoming.map((item) => item.id),
    ["sunday"],
  );
  assert.equal(state.timeZone, "Europe/Berlin");
});

test("an agenda of only finished all-day events is empty", () => {
  const state = toCalendarSummary(
    [event("friday", { allDay: true, startDate: "2026-10-09" })],
    nearMidnight,
    "Europe/Berlin",
  );
  assert.deepEqual(state, { kind: "empty" });
});

test("event times are shown in the owner's timezone", () => {
  const dentist = event("dentist", {
    allDay: false,
    startAt: "2026-10-16T13:00:00Z",
    endAt: "2026-10-16T14:00:00Z",
  });
  assert.equal(
    eventTimeLabel(dentist, "Europe/Berlin"),
    "Fri 16 Oct · 15:00–16:00",
  );
  assert.equal(eventTimeLabel(dentist, "UTC"), "Fri 16 Oct · 13:00–14:00");
  // A late event crosses midnight in Berlin, so only its start is shown.
  const late = event("late", {
    allDay: false,
    startAt: "2026-10-16T21:30:00Z",
    endAt: "2026-10-16T22:30:00Z",
  });
  assert.equal(eventTimeLabel(late, "Europe/Berlin"), "Fri 16 Oct · 23:30");
  // Dates are not instants: all-day events read the same everywhere.
  const trip = event("trip", {
    allDay: true,
    startDate: "2026-10-16",
    endDate: "2026-10-18",
  });
  assert.equal(
    eventTimeLabel(trip, "America/Los_Angeles"),
    "Fri 16 Oct – Sun 18 Oct · All day",
  );
});
