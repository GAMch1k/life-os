import { chmodSync, writeFileSync } from "node:fs";
import path from "node:path";

import { type APIRequestContext, expect } from "./fixtures";

// Calendar sources (ICS feeds) for the browser tests. A source whose host
// does not exist fails on every verification stack. Feeds that import need
// the verification overlay tools/fixtures/calendar-ics.compose.yaml: it
// serves the files a test writes to LIFE_OS_E2E_ICS_DIR at
// http://ics-fixture/ and lets that stack's Calendar fetch them.

export const feedDir = process.env.LIFE_OS_E2E_ICS_DIR;
const feedBase = (
  process.env.LIFE_OS_E2E_ICS_URL ?? "http://ics-fixture"
).replace(/\/+$/, "");
export const FEED_OVERLAY_HINT =
  "set LIFE_OS_E2E_ICS_DIR and start the stack with LIFE_OS_VERIFY_OVERLAYS=tools/fixtures/calendar-ics.compose.yaml";

/** A feed URL whose host never resolves (.invalid is reserved). */
export const failingFeedUrl = (stamp: number) =>
  `https://feed.invalid/e2e-${stamp}.ics`;

export type FeedEvent = { uid: string; title: string } & (
  { start: Date; end: Date } | { day: string }
);

export type CalendarSource = {
  id: string;
  name: string;
  refresh: {
    lastOutcome?: string;
    lastError?: string | null;
    consecutiveFailures: number;
    lastCounts?: {
      inserted: number;
      updated: number;
      removed: number;
      restored: number;
    };
  };
};

/** A UTC instant as ICS writes it, such as 20261010T120000Z. */
function icsInstant(date: Date) {
  return date
    .toISOString()
    .replace(/\.\d{3}Z$/, "Z")
    .replace(/[-:]/g, "");
}

export function addDays(day: string, days: number) {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** The Berlin day (the browser's zone in these tests) of an instant. */
export function berlinDay(instant: Date | string) {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: "Europe/Berlin",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date(instant));
}

/** Writes a feed for the overlay to serve and returns its URL. */
export function writeFeed(file: string, events: FeedEvent[]) {
  if (!feedDir) throw new Error(FEED_OVERLAY_HINT);
  // The fixture server reads the files as another user.
  chmodSync(feedDir, 0o755);
  const stamp = icsInstant(new Date());
  const lines = [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//Life OS//Browser tests//EN",
  ];
  for (const event of events) {
    lines.push("BEGIN:VEVENT", `UID:${event.uid}`, `DTSTAMP:${stamp}`);
    if ("day" in event) {
      lines.push(
        `DTSTART;VALUE=DATE:${event.day.replace(/-/g, "")}`,
        `DTEND;VALUE=DATE:${addDays(event.day, 1).replace(/-/g, "")}`,
      );
    } else {
      lines.push(
        `DTSTART:${icsInstant(event.start)}`,
        `DTEND:${icsInstant(event.end)}`,
      );
    }
    lines.push(`SUMMARY:${event.title}`, "END:VEVENT");
  }
  lines.push("END:VCALENDAR");
  const target = path.join(feedDir, file);
  writeFileSync(target, `${lines.join("\r\n")}\r\n`);
  chmodSync(target, 0o644);
  return `${feedBase}/${file}`;
}

export async function createSource(
  request: APIRequestContext,
  name: string,
  url: string,
): Promise<CalendarSource> {
  const response = await request.post("/api/calendar/v1/sources", {
    data: { name, url },
  });
  expect(response.status(), await response.text()).toBe(201);
  return response.json();
}

async function getSource(
  request: APIRequestContext,
  id: string,
): Promise<CalendarSource> {
  const response = await request.get(`/api/calendar/v1/sources/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

/**
 * Waits for the source's first refresh, which Calendar's scheduler starts as
 * soon as the source is added.
 */
export async function waitForRefresh(
  request: APIRequestContext,
  id: string,
  outcome: "imported" | "failed",
) {
  let source: CalendarSource | undefined;
  await expect
    .poll(
      async () => {
        source = await getSource(request, id);
        return source.refresh.lastOutcome;
      },
      { timeout: 90_000 },
    )
    .toBe(outcome);
  return source!;
}

/** "Refresh now", retried while another refresh of the source runs. */
export async function refreshSource(
  request: APIRequestContext,
  id: string,
): Promise<CalendarSource> {
  let source: CalendarSource | undefined;
  await expect
    .poll(
      async () => {
        const response = await request.post(
          `/api/calendar/v1/sources/${id}/refresh`,
        );
        if (response.status() === 409) return "running";
        expect(response.status(), await response.text()).toBe(200);
        source = await response.json();
        return "done";
      },
      { timeout: 60_000 },
    )
    .toBe("done");
  return source!;
}

/** Removes the source and the events it imported. */
export async function deleteSource(request: APIRequestContext, id: string) {
  await request.delete(`/api/calendar/v1/sources/${id}`);
}
