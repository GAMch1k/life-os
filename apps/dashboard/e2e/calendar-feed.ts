import { chmodSync, writeFileSync } from "node:fs";
import path from "node:path";

import { type APIRequestContext, expect } from "./fixtures";

// Calendar feeds (ICS) for the browser tests, through the calendar-feeds
// integration (/api/integrations/calendar-feeds). A feed is one Calendar
// source: `id` is the feed's, `sourceId` its Calendar source's (its events'
// source.sourceId, its Activity subject, and /api/calendar/v1/sources for
// its name, enabled, and defaults). A feed whose host does not exist fails
// on every verification stack. Feeds that import need the verification
// overlay tools/fixtures/calendar-ics.compose.yaml: it serves the files a
// test writes to LIFE_OS_E2E_ICS_DIR at http://ics-fixture/ and lets that
// stack's calendar-feeds fetch them.

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

const FEEDS = "/api/integrations/calendar-feeds/v1/feeds";

export type CalendarFeed = {
  id: string;
  sourceId: string;
  name: string;
  enabled: boolean;
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

/**
 * Writes another fixture file, such as a recipe page for recipe-import, and
 * returns its URL on the fixture server (the overlay lets this stack's
 * recipe-import fetch the ics-fixture host).
 */
export function writeFixture(file: string, contents: string) {
  if (!feedDir) throw new Error(FEED_OVERLAY_HINT);
  chmodSync(feedDir, 0o755);
  const target = path.join(feedDir, file);
  writeFileSync(target, contents);
  chmodSync(target, 0o644);
  return `${feedBase}/${file}`;
}

/**
 * Subscribes to a feed; `enabled: false` switches its Calendar source off
 * right away (a disabled feed is not refreshed).
 */
export async function createFeed(
  request: APIRequestContext,
  name: string,
  url: string,
  { enabled = true }: { enabled?: boolean } = {},
): Promise<CalendarFeed> {
  const response = await request.post(FEEDS, { data: { name, url } });
  expect(response.status(), await response.text()).toBe(201);
  const feed = (await response.json()) as CalendarFeed;
  if (!enabled) {
    const off = await request.patch(
      `/api/calendar/v1/sources/${feed.sourceId}`,
      { data: { enabled: false } },
    );
    expect(off.status(), await off.text()).toBe(200);
  }
  return feed;
}

async function getFeed(
  request: APIRequestContext,
  id: string,
): Promise<CalendarFeed> {
  const response = await request.get(`${FEEDS}/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

/**
 * Waits for the feed's first refresh, which calendar-feeds' scheduler starts
 * as soon as the feed is added.
 */
export async function waitForRefresh(
  request: APIRequestContext,
  id: string,
  outcome: "imported" | "failed",
) {
  let feed: CalendarFeed | undefined;
  await expect
    .poll(
      async () => {
        feed = await getFeed(request, id);
        return feed.refresh.lastOutcome;
      },
      { timeout: 90_000 },
    )
    .toBe(outcome);
  return feed!;
}

/** "Refresh now", retried while another refresh of the feed runs. */
export async function refreshFeed(
  request: APIRequestContext,
  id: string,
): Promise<CalendarFeed> {
  let feed: CalendarFeed | undefined;
  await expect
    .poll(
      async () => {
        const response = await request.post(`${FEEDS}/${id}/refresh`);
        if (response.status() === 409) return "running";
        expect(response.status(), await response.text()).toBe(200);
        feed = await response.json();
        return "done";
      },
      { timeout: 60_000 },
    )
    .toBe("done");
  return feed!;
}

/** Unsubscribes: the feed, its Calendar source, and its events go. */
export async function deleteFeed(request: APIRequestContext, id: string) {
  await request.delete(`${FEEDS}/${id}`);
}
