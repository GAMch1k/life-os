import { activityClient } from "@/lib/api/activity-client";
import type { components } from "@/lib/api/generated/activity";

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
];

export const SUBJECT_TYPES = [
  { value: "voice_job", label: "Voice recording" },
  { value: "inbox_item", label: "Inbox item" },
  { value: "processing_attempt", label: "Processing attempt" },
  { value: "mcp_action", label: "Agent action" },
  { value: "task", label: "Task" },
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
};

export const EVENT_TYPES = Object.keys(EVENT_LABELS);

function humanize(value: string) {
  const words = value.replace(/[._]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function eventLabel(event: Pick<ActivityEvent, "type" | "data">) {
  const label = EVENT_LABELS[event.type] ?? humanize(event.type);
  const tool = event.data?.tool;
  return event.type.startsWith("mcp.action.") && typeof tool === "string"
    ? `${label}: ${tool}`
    : label;
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
  mcp: "Agent via MCP",
  "voice-worker": "Voice worker",
};

export function actorLabel(actor: ActivityEvent["actor"]) {
  if (actor.kind === "user") return "You";
  const name = actor.name ? (ACTOR_NAMES[actor.name] ?? actor.name) : undefined;
  return name ?? humanize(actor.kind);
}

const timeFormat = new Intl.DateTimeFormat("en-GB", {
  dateStyle: "medium",
  timeStyle: "medium",
  timeZone: "UTC",
});

export function eventTime(value: string) {
  return `${timeFormat.format(new Date(value))} UTC`;
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
  /** Inclusive first day, YYYY-MM-DD (UTC). */
  fromDate?: string;
  /** Inclusive last day, YYYY-MM-DD (UTC). */
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

/** Activity API query for the list; the "to" day is inclusive for the user. */
export function listQuery(filters: ActivityFilters, limit = 50) {
  const nextDay = (date: string) => {
    const day = new Date(`${date}T00:00:00Z`);
    day.setUTCDate(day.getUTCDate() + 1);
    return day.toISOString();
  };
  return {
    source: filters.source,
    subjectType: filters.subjectType,
    subjectId: filters.subjectId,
    type: filters.type,
    outcome: filters.outcome,
    correlationId: filters.correlationId,
    from: filters.fromDate ? `${filters.fromDate}T00:00:00.000Z` : undefined,
    to: filters.toDate ? nextDay(filters.toDate) : undefined,
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
): Promise<ActivityLog> {
  const filters = parseFilters(params);
  const selectedId = selectedEventId(params);
  const [list, selection] = await Promise.all([
    activityClient
      .GET("/v1/activity-events", { params: { query: listQuery(filters) } })
      .then(
        ({ data }) => data ?? null,
        () => null,
      ),
    selectedId ? loadSelection(selectedId) : Promise.resolve(null),
  ]);
  return { filters, list, selectedId, selection };
}
