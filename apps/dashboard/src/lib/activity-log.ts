import { activityClient } from "@/lib/api/activity-client";
import type { components } from "@/lib/api/generated/activity";
import { addDays, DATE_LOCALE, dayKey, startOfDay } from "@/lib/time-zone";

// The Event Log is a read-only view over the Activity API. Everything here is
// presentation: filters map to API query parameters, and a selected event's
// trace is rebuilt from its correlation (one workflow), causation (what caused
// what), and subject (the same entity in other workflows).

export type ActivityEvent = components["schemas"]["ActivityEvent"];
export type ActivityOutcome = components["schemas"]["ActivityOutcome"];

export const OUTCOMES: ActivityOutcome[] = ["succeeded", "pending", "failed"];

/** What the agent's changes are, wherever the "mcp" domain is explained. */
export const AGENT_CHANGES_DESCRIPTION =
  "Changes the AI made through MCP, such as creating or completing Tasks. Its decisions, such as Human Review requests, appear under Inbox.";

/** What the "calendar" domain records. */
export const CALENDAR_DESCRIPTION =
  "Calendar events created, changed, cancelled, or deleted (by you, the AI, or a tool), and the Google Calendar connection and syncs.";

/** What the "calendar-feeds" domain records. */
export const CALENDAR_FEEDS_DESCRIPTION =
  "Each refresh of a subscribed calendar feed (ICS/iCal, such as ZAW): what it added, changed, or cancelled, or why it failed.";

export const SOURCES: readonly {
  value: string;
  label: string;
  description?: string;
}[] = [
  { value: "voice", label: "Voice" },
  { value: "inbox", label: "Inbox" },
  {
    value: "mcp",
    label: "Agent changes",
    description: AGENT_CHANGES_DESCRIPTION,
  },
  { value: "tasks", label: "Tasks" },
  { value: "calendar", label: "Calendar", description: CALENDAR_DESCRIPTION },
  {
    value: "calendar-feeds",
    label: "Calendar feeds",
    description: CALENDAR_FEEDS_DESCRIPTION,
  },
  { value: "notifications", label: "Notifications" },
];

export const SUBJECT_TYPES = [
  { value: "voice_job", label: "Voice recording" },
  { value: "inbox_item", label: "Inbox item" },
  { value: "processing_attempt", label: "Processing attempt" },
  { value: "mcp_action", label: "Agent action" },
  { value: "task", label: "Task" },
  { value: "calendar_event", label: "Calendar event" },
  { value: "calendar_source", label: "Calendar source" },
  { value: "google_connection", label: "Google connection" },
  { value: "notification", label: "Notification" },
] as const;

const EVENT_LABELS: Record<string, string> = {
  "voice.job.accepted": "Voice recording received",
  "voice.job.transcribed": "Voice recording transcribed",
  "voice.job.retry_scheduled": "Transcription retry scheduled",
  "voice.job.failed": "Transcription failed",
  "inbox.item.captured": "Inbox capture",
  "inbox.processing.started": "Processing started",
  "inbox.processing.completed": "Processing completed",
  "inbox.processing.retry_scheduled": "Processing retry scheduled",
  "inbox.processing.failed": "Processing failed",
  "inbox.human_review.requested": "Human Review requested",
  "inbox.human_review.resolved": "Human Review resolved",
  "inbox.item.resolved": "Inbox item resolved",
  "inbox.item.discarded": "Inbox item discarded",
  "mcp.action.requested": "Agent action requested",
  "mcp.action.completed": "Agent action completed",
  "mcp.action.failed": "Agent action failed",
  "task.created": "Task created",
  "task.updated": "Task updated",
  "task.completed": "Task completed",
  "task.reopened": "Task reopened",
  "calendar.event.created": "Event created",
  "calendar.event.updated": "Event updated",
  "calendar.event.rescheduled": "Event rescheduled",
  "calendar.event.cancelled": "Event cancelled",
  "calendar.event.restored": "Event restored",
  "calendar.event.deleted": "Event deleted",
  "calendar.source.refreshed": "Source refreshed",
  "calendar.source.refresh_failed": "Source refresh failed",
  "calendar.source.removed": "Source removed",
  "calendar.google.connected": "Google Calendar connected",
  "calendar.google.disconnected": "Google Calendar disconnected",
  "calendar.google.expired": "Google Calendar access expired",
  "calendar.google.conflict": "Changed in Google meanwhile",
  "calendar.google.calendar_created": "Life OS calendar created in Google",
  "calendar.google.export_failed": "Export to Google failed",
  "notification.created": "Notification created",
  "notification.delivered": "Notification delivered",
  "notification.delivery_failed": "Notification delivery failed",
};

export const EVENT_TYPES = Object.keys(EVENT_LABELS);

function humanize(value: string) {
  const words = value.replace(/[._]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function text(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

/** What a feed refresh changed, as recorded in its `counts`. */
type RefreshCounts = {
  inserted?: number;
  updated?: number;
  removed?: number;
  restored?: number;
};

/**
 * What a source refresh did, such as "3 added, 1 cancelled", "no changes",
 * or "unchanged" when the feed itself had not changed. Removed events are
 * cancelled by their source, so they read as cancelled.
 */
export function refreshChanges(data: ActivityEvent["data"] | undefined) {
  if (data?.outcome === "not_modified") return "unchanged";
  const counts = (data?.counts ?? {}) as RefreshCounts;
  const parts = (
    [
      ["inserted", "added"],
      ["updated", "changed"],
      ["removed", "cancelled"],
      ["restored", "restored"],
    ] as const
  )
    .map(([key, verb]) => {
      const count = counts[key];
      return typeof count === "number" && count > 0
        ? `${count} ${verb}`
        : undefined;
    })
    .filter(Boolean);
  return parts.length ? parts.join(", ") : "no changes";
}

export function eventLabel(event: Pick<ActivityEvent, "type" | "data">) {
  const label = EVENT_LABELS[event.type] ?? humanize(event.type);
  const tool = text(event.data?.tool);
  if (event.type.startsWith("mcp.action.") && tool) return `${label}: ${tool}`;
  const title = text(event.data?.title);
  if (
    (event.type.startsWith("calendar.event.") ||
      event.type === "calendar.google.conflict" ||
      event.type === "calendar.google.export_failed") &&
    title
  ) {
    return `${label}: ${title}`;
  }
  const source = text(event.data?.sourceName);
  if (event.type === "calendar.source.refreshed" && source) {
    return `${label}: ${source} — ${refreshChanges(event.data)}`;
  }
  if (event.type === "calendar.source.refresh_failed" && source) {
    return `${label}: ${source}`;
  }
  if (event.type === "calendar.source.removed" && source) {
    const removed = event.data?.removedEvents;
    return typeof removed === "number"
      ? `${label}: ${source} — ${removed} event${removed === 1 ? "" : "s"} removed`
      : `${label}: ${source}`;
  }
  const account = text(event.data?.accountEmail);
  if (event.type.startsWith("calendar.google.") && account) {
    return `${label}: ${account}`;
  }
  // A notification by its title, and a delivery by the channel it used.
  if (event.type.startsWith("notification.") && title) {
    const channel = text(event.data?.channel);
    return channel ? `${label}: ${title} (${channel})` : `${label}: ${title}`;
  }
  return label;
}

/**
 * Where a Calendar entry's subject is in the Calendar: an event's first day
 * (in `timeZone`), or the Calendar itself for a source refresh. Undefined for
 * other entries and for a deleted event, which is no longer there.
 */
export function calendarHref(
  event: Pick<ActivityEvent, "type" | "subject" | "data">,
  timeZone: string,
) {
  if (
    event.subject.type === "calendar_source" ||
    event.subject.type === "google_connection"
  ) {
    return "/calendar";
  }
  if (
    event.subject.type !== "calendar_event" ||
    event.type === "calendar.event.deleted"
  ) {
    return undefined;
  }
  const startDate = text(event.data?.startDate);
  const startAt = text(event.data?.startAt);
  let day: string | undefined;
  if (startDate && /^\d{4}-\d{2}-\d{2}$/.test(startDate)) {
    day = startDate;
  } else if (startAt && !Number.isNaN(Date.parse(startAt))) {
    day = dayKey(startAt, timeZone);
  }
  return day ? `/calendar?date=${day}` : "/calendar";
}

export function subjectLabel(type: string) {
  return (
    SUBJECT_TYPES.find((entry) => entry.value === type)?.label ?? humanize(type)
  );
}

export function sourceLabel(source: string) {
  return (
    SOURCES.find((entry) => entry.value === source)?.label ?? humanize(source)
  );
}

export function sourceDescription(source: string | undefined) {
  return SOURCES.find((entry) => entry.value === source)?.description;
}

const ACTOR_NAMES: Record<string, string> = {
  "codex-inbox-processor": "Inbox processor (Codex)",
  "google-calendar": "Google Calendar sync",
  mcp: "Agent via MCP",
  "voice-worker": "Voice worker",
};

export type ActivityActor = ActivityEvent["actor"];

/**
 * A development tool or agent working on Life OS, such as Claude Code. It is
 * neither the owner nor the Life OS AI, and the Event Log keeps it visibly
 * apart from both.
 */
export function isDevActor(actor: ActivityActor) {
  return actor.kind === "dev";
}

export function actorLabel(actor: ActivityActor) {
  if (actor.kind === "user") return "You";
  if (isDevActor(actor)) {
    return actor.name ? `Developer tool (${actor.name})` : "Developer tool";
  }
  // A subscribed calendar feed, as system:ics-<its name> (see Calendar).
  if (actor.kind === "system" && actor.name?.startsWith("ics-")) {
    const feed = actor.name.slice(4);
    return feed && feed !== "source"
      ? `Calendar feed (${feed})`
      : "Calendar feed";
  }
  const name = actor.name ? (ACTOR_NAMES[actor.name] ?? actor.name) : undefined;
  return name ?? humanize(actor.kind);
}

const timeFormats = new Map<string, Intl.DateTimeFormat>();

/**
 * An event time in the owner's timezone, with the zone's abbreviation, for
 * example "9 Oct 2026, 23:21:30 CEST". The locale and timezone are fixed, so
 * the text does not depend on where it is rendered.
 */
export function eventTime(value: string, timeZone: string) {
  let format = timeFormats.get(timeZone);
  if (!format) {
    format = new Intl.DateTimeFormat(DATE_LOCALE, {
      dateStyle: "medium",
      timeStyle: "long",
      timeZone,
    });
    timeFormats.set(timeZone, format);
  }
  return format.format(new Date(value));
}

/** Shortest piece of shortened text that may be matched against the data. */
const MIN_OVERLAP = 16;

/**
 * The event's summary with its shortened part restored, when the event's data
 * holds more of it, for example a retry's full `errorSummary` behind
 * "Processing will be retried: planning failed: …". Services shorten text in
 * summaries with a trailing "…" after collapsing whitespace; the data keeps a
 * longer copy. Undefined when there is nothing more to show.
 */
export function fullSummary(event: Pick<ActivityEvent, "summary" | "data">) {
  const cut = event.summary.lastIndexOf("…");
  if (cut < 0) return undefined;
  const before = event.summary.slice(0, cut);
  const after = event.summary.slice(cut + 1);
  for (const value of Object.values(event.data ?? {})) {
    if (typeof value !== "string") continue;
    const text = value.trim();
    const flat = text.replace(/\s+/g, " ");
    // Text the summary already shows in full is not what was shortened.
    if (event.summary.includes(flat)) continue;
    // The shortened part is the start of the text; the earliest match is the
    // longest overlap.
    for (let start = 0; start <= before.length - MIN_OVERLAP; start++) {
      if (flat.startsWith(before.slice(start))) {
        return before.slice(0, start) + text + after;
      }
    }
  }
  return undefined;
}

// ---- Filters ---------------------------------------------------------------

export type SearchParams = Record<string, string | string[] | undefined>;

export type ActivityFilters = {
  source?: string;
  subjectType?: string;
  subjectId?: string;
  type?: string;
  outcome?: ActivityOutcome;
  correlationId?: string;
  /** Inclusive first day, YYYY-MM-DD in the owner's timezone. */
  fromDate?: string;
  /** Inclusive last day, YYYY-MM-DD in the owner's timezone. */
  toDate?: string;
  cursor?: string;
};

const FILTER_KEYS = [
  "source",
  "subjectType",
  "subjectId",
  "type",
  "outcome",
  "correlationId",
  "fromDate",
  "toDate",
  "cursor",
] as const;

function single(value: string | string[] | undefined) {
  const text = (Array.isArray(value) ? value[0] : value)?.trim();
  return text ? text.slice(0, 200) : undefined;
}

const DATE = /^\d{4}-\d{2}-\d{2}$/;

function validDate(value: string | undefined) {
  return value &&
    DATE.test(value) &&
    !Number.isNaN(Date.parse(`${value}T00:00:00Z`))
    ? value
    : undefined;
}

export function parseFilters(params: SearchParams): ActivityFilters {
  const outcome = single(params.outcome);
  const filters: ActivityFilters = {
    source: single(params.source),
    subjectType: single(params.subjectType),
    subjectId: single(params.subjectId),
    type: single(params.type),
    outcome: OUTCOMES.find((value) => value === outcome),
    correlationId: single(params.correlationId),
    fromDate: validDate(single(params.fromDate)),
    toDate: validDate(single(params.toDate)),
    cursor: single(params.cursor),
  };
  return Object.fromEntries(
    Object.entries(filters).filter(([, value]) => value !== undefined),
  ) as ActivityFilters;
}

export function selectedEventId(params: SearchParams) {
  const id = single(params.event);
  return id && /^[0-9a-f-]{36}$/i.test(id) ? id.toLowerCase() : undefined;
}

/**
 * Activity API query for the list. Days are the owner's calendar days, and the
 * "to" day is inclusive for the user.
 */
export function listQuery(
  filters: ActivityFilters,
  timeZone: string,
  limit = 50,
) {
  const dayStart = (day: string) => startOfDay(day, timeZone).toISOString();
  return {
    source: filters.source,
    subjectType: filters.subjectType,
    subjectId: filters.subjectId,
    type: filters.type,
    outcome: filters.outcome,
    correlationId: filters.correlationId,
    from: filters.fromDate ? dayStart(filters.fromDate) : undefined,
    to: filters.toDate ? dayStart(addDays(filters.toDate, 1)) : undefined,
    cursor: filters.cursor,
    limit,
  };
}

/** A /activity URL with the given filters and selection. */
export function activityHref(
  filters: ActivityFilters,
  changes: Partial<ActivityFilters> & { event?: string } = {},
) {
  const merged: Record<string, string | undefined> = { ...filters, ...changes };
  const params = new URLSearchParams();
  for (const key of [...FILTER_KEYS, "event"]) {
    const value = merged[key];
    if (value) params.set(key, value);
  }
  const query = params.toString();
  return query ? `/activity?${query}` : "/activity";
}

export function hasFilters(filters: ActivityFilters) {
  return FILTER_KEYS.some((key) => key !== "cursor" && filters[key]);
}

export type EmptyState = {
  message: string;
  /** A related view that explains or contains what the user looked for. */
  link?: { label: string; href: string };
};

/** Why the event list is empty, specific to the active filters. */
export function emptyState(filters: ActivityFilters): EmptyState {
  if (!hasFilters(filters)) return { message: "No activity recorded yet." };
  const narrowed = (
    ["subjectType", "subjectId", "type", "outcome", "correlationId"] as const
  ).some((key) => filters[key]);
  if (filters.source === "mcp" && !narrowed) {
    // Usually not a fault: the agent only shows up here when it changed
    // something; its decisions are recorded by Inbox.
    return {
      message:
        filters.fromDate || filters.toDate
          ? "The agent hasn't changed anything in this range."
          : "The agent hasn't changed anything yet.",
      link: {
        label: "Show Inbox activity",
        href: activityHref(filters, { source: "inbox", cursor: undefined }),
      },
    };
  }
  return { message: "No activity matches these filters." };
}

// ---- Trace -----------------------------------------------------------------

export type Relation = "selected" | "upstream" | "downstream" | "other";

export type TraceNode = {
  event: ActivityEvent;
  relation: Relation;
  children: TraceNode[];
};

export type Trace = {
  selected: ActivityEvent;
  /** The workflow as a causation tree, oldest root first. */
  roots: TraceNode[];
  /** Root-first chain of events that led to the selection. */
  upstream: ActivityEvent[];
  /** Everything the selection led to, oldest first. */
  downstream: ActivityEvent[];
  /** The selection's cause when it was recorded outside this workflow. */
  externalCauseId?: string;
  /** The same subject in other workflows, oldest first. */
  related: ActivityEvent[];
};

export function chronological(a: ActivityEvent, b: ActivityEvent) {
  return (
    Date.parse(a.occurredAt) - Date.parse(b.occurredAt) ||
    Date.parse(a.recordedAt) - Date.parse(b.recordedAt) ||
    a.id.localeCompare(b.id)
  );
}

export function buildTrace(
  selected: ActivityEvent,
  workflow: ActivityEvent[],
  subjectHistory: ActivityEvent[],
): Trace {
  const byId = new Map<string, ActivityEvent>();
  for (const event of [...workflow, selected]) byId.set(event.id, event);
  const events = [...byId.values()].sort(chronological);

  const childrenOf = new Map<string, ActivityEvent[]>();
  const roots: ActivityEvent[] = [];
  for (const event of events) {
    const cause = event.causationId;
    if (cause && cause !== event.id && byId.has(cause)) {
      childrenOf.set(cause, [...(childrenOf.get(cause) ?? []), event]);
    } else {
      roots.push(event);
    }
  }

  const upstream: ActivityEvent[] = [];
  const seen = new Set([selected.id]);
  for (
    let cause = selected.causationId
      ? byId.get(selected.causationId)
      : undefined;
    cause && !seen.has(cause.id);
    cause = cause.causationId ? byId.get(cause.causationId) : undefined
  ) {
    seen.add(cause.id);
    upstream.unshift(cause);
  }

  const downstream: ActivityEvent[] = [];
  const queue = [...(childrenOf.get(selected.id) ?? [])];
  while (queue.length) {
    const event = queue.shift()!;
    if (seen.has(event.id)) continue;
    seen.add(event.id);
    downstream.push(event);
    queue.push(...(childrenOf.get(event.id) ?? []));
  }
  downstream.sort(chronological);

  const upstreamIds = new Set(upstream.map((event) => event.id));
  const downstreamIds = new Set(downstream.map((event) => event.id));
  const relation = (event: ActivityEvent): Relation =>
    event.id === selected.id
      ? "selected"
      : upstreamIds.has(event.id)
        ? "upstream"
        : downstreamIds.has(event.id)
          ? "downstream"
          : "other";
  const visited = new Set<string>();
  const node = (event: ActivityEvent): TraceNode => {
    visited.add(event.id);
    return {
      event,
      relation: relation(event),
      children: (childrenOf.get(event.id) ?? [])
        .filter((child) => !visited.has(child.id))
        .map(node),
    };
  };

  return {
    selected,
    roots: roots.map(node),
    upstream,
    downstream,
    externalCauseId:
      selected.causationId && !byId.has(selected.causationId)
        ? selected.causationId
        : undefined,
    related: subjectHistory
      .filter((event) => event.correlationId !== selected.correlationId)
      .sort(chronological),
  };
}

// ---- Loading ---------------------------------------------------------------

export type ActivityList = {
  items: ActivityEvent[];
  nextCursor?: string | null;
};

export type Selection =
  | { kind: "trace"; trace: Trace }
  | { kind: "missing"; id: string }
  | { kind: "unavailable" };

export type ActivityLog = {
  filters: ActivityFilters;
  /** The owner's timezone, for filter days and displayed times. */
  timeZone: string;
  /** `null` when the Activity service could not be read. */
  list: ActivityList | null;
  selectedId?: string;
  selection: Selection | null;
};

/** A workflow or subject history is read in pages of 100, up to this many. */
const TRACE_PAGES = 5;

async function listAll(query: {
  correlationId?: string;
  subjectType?: string;
  subjectId?: string;
}) {
  const items: ActivityEvent[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < TRACE_PAGES; page++) {
    const { data } = await activityClient.GET("/v1/activity-events", {
      params: { query: { ...query, cursor, limit: 100 } },
    });
    if (!data) throw new Error("Activity is unavailable");
    items.push(...data.items);
    if (!data.nextCursor) break;
    cursor = data.nextCursor;
  }
  return items;
}

async function loadSelection(id: string): Promise<Selection> {
  try {
    const { data, response } = await activityClient.GET(
      "/v1/activity-events/{eventId}",
      { params: { path: { eventId: id } } },
    );
    if (!data) {
      return response.status === 404
        ? { kind: "missing", id }
        : { kind: "unavailable" };
    }
    const [workflow, history] = await Promise.all([
      listAll({ correlationId: data.correlationId }),
      listAll({ subjectType: data.subject.type, subjectId: data.subject.id }),
    ]);
    return { kind: "trace", trace: buildTrace(data, workflow, history) };
  } catch {
    return { kind: "unavailable" };
  }
}

export async function getActivityLog(
  params: SearchParams,
  timeZone: string,
): Promise<ActivityLog> {
  const filters = parseFilters(params);
  const selectedId = selectedEventId(params);
  const [list, selection] = await Promise.all([
    activityClient
      .GET("/v1/activity-events", {
        params: { query: listQuery(filters, timeZone) },
      })
      .then(
        ({ data }) => data ?? null,
        () => null,
      ),
    selectedId ? loadSelection(selectedId) : Promise.resolve(null),
  ]);
  return { filters, timeZone, list, selectedId, selection };
}
