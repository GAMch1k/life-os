import assert from "node:assert/strict";
import { test } from "node:test";

import {
  addDays,
  dateTimeFormat,
  dayKey,
  resolveTimeZone,
  startOfDay,
  storedTimeZone,
  timeZoneCookie,
  timeZoneSync,
} from "./time-zone";

test("the browser's zone wins, then LIFE_OS_TIMEZONE, then UTC", () => {
  assert.equal(
    resolveTimeZone("America/New_York", "Europe/Berlin"),
    "America/New_York",
  );
  // A missing, empty, or unknown cookie falls back to the configured zone.
  assert.equal(resolveTimeZone(undefined, "Europe/Berlin"), "Europe/Berlin");
  assert.equal(resolveTimeZone("", "Europe/Berlin"), "Europe/Berlin");
  assert.equal(
    resolveTimeZone("Mars/Olympus", " Europe/Berlin "),
    "Europe/Berlin",
  );
  assert.equal(resolveTimeZone(undefined, undefined), "UTC");
  assert.equal(resolveTimeZone("Mars/Olympus", "Europe/Berlinn"), "UTC");
  // A browser that really is in UTC is shown in UTC.
  assert.equal(resolveTimeZone("UTC", "Europe/Berlin"), "UTC");
});

test("the timezone cookie round-trips through document.cookie syntax", () => {
  assert.equal(
    timeZoneCookie("America/Argentina/Buenos_Aires"),
    "lifeos-tz=America%2FArgentina%2FBuenos_Aires; Path=/; Max-Age=31536000; SameSite=Lax",
  );
  assert.equal(
    storedTimeZone("theme=dark; lifeos-tz=Etc%2FGMT%2B5; other=1"),
    "Etc/GMT+5",
  );
  assert.equal(storedTimeZone("lifeos-tz=Europe/Berlin"), "Europe/Berlin");
  assert.equal(storedTimeZone("theme=dark"), undefined);
  assert.equal(storedTimeZone(""), undefined);
  assert.equal(storedTimeZone("lifeos-tz=%E0%A4%A"), undefined);
});

test("time zone sync stores a new browser zone and refreshes only once", () => {
  // First visit from New York: the page was rendered in Berlin.
  assert.deepEqual(
    timeZoneSync({
      browser: "America/New_York",
      rendered: "Europe/Berlin",
      stored: undefined,
    }),
    { store: true, refresh: true },
  );
  // After the refresh the server rendered the cookie's zone: nothing to do.
  assert.deepEqual(
    timeZoneSync({
      browser: "America/New_York",
      rendered: "America/New_York",
      stored: "America/New_York",
    }),
    { store: false, refresh: false },
  );
  // A Berlin browser on a Berlin Dashboard stores its zone without a refresh.
  assert.deepEqual(
    timeZoneSync({
      browser: "Europe/Berlin",
      rendered: "Europe/Berlin",
      stored: undefined,
    }),
    { store: true, refresh: false },
  );
  // Back home after a trip: the stale cookie is replaced and the page redone.
  assert.deepEqual(
    timeZoneSync({
      browser: "Europe/Berlin",
      rendered: "America/New_York",
      stored: "America/New_York",
    }),
    { store: true, refresh: true },
  );
  // The cookie already holds the browser's zone but the server rendered
  // another (it cannot use that zone): refreshing again would loop.
  assert.deepEqual(
    timeZoneSync({
      browser: "America/Ciudad_Juarez",
      rendered: "Europe/Berlin",
      stored: "America/Ciudad_Juarez",
    }),
    { store: false, refresh: false },
  );
  // An unknown browser zone changes nothing.
  assert.deepEqual(
    timeZoneSync({
      browser: undefined,
      rendered: "Europe/Berlin",
      stored: undefined,
    }),
    { store: false, refresh: false },
  );
});

test("dayKey uses the configured timezone, not UTC", () => {
  // 23:41 UTC on Friday is already Saturday in Berlin (summer time).
  assert.equal(dayKey("2026-10-09T23:41:00Z", "UTC"), "2026-10-09");
  assert.equal(dayKey("2026-10-09T23:41:00Z", "Europe/Berlin"), "2026-10-10");
});

test("startOfDay is local midnight, across DST changes", () => {
  assert.equal(
    startOfDay("2026-10-10", "Europe/Berlin").toISOString(),
    "2026-10-09T22:00:00.000Z",
  );
  // Berlin leaves summer time on 2026-10-25.
  assert.equal(
    startOfDay("2026-10-25", "Europe/Berlin").toISOString(),
    "2026-10-24T22:00:00.000Z",
  );
  assert.equal(
    startOfDay("2026-10-26", "Europe/Berlin").toISOString(),
    "2026-10-25T23:00:00.000Z",
  );
  assert.equal(
    startOfDay("2026-10-10", "UTC").toISOString(),
    "2026-10-10T00:00:00.000Z",
  );
});

test("addDays crosses month boundaries", () => {
  assert.equal(addDays("2026-10-31", 1), "2026-11-01");
  assert.equal(addDays("2026-03-01", -1), "2026-02-28");
});

test("dateTimeFormat renders in the timezone with the fixed locale", () => {
  const options = { hour: "2-digit", minute: "2-digit" } as const;
  const at = new Date("2026-10-09T23:41:00Z");
  assert.equal(dateTimeFormat("Europe/Berlin", options).format(at), "01:41");
  assert.equal(dateTimeFormat("UTC", options).format(at), "23:41");
  assert.equal(
    dateTimeFormat("Europe/Berlin", options),
    dateTimeFormat("Europe/Berlin", { ...options }),
  );
});
