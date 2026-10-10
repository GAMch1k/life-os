import { notificationsClient } from "@/lib/api/notifications-client";
import type { components } from "@/lib/api/generated/notifications";
import { formatInboxTime } from "@/lib/inbox-summary";
import { relativeDayLabel } from "@/lib/tasks-workspace";
import { addDays, dateTimeFormat, dayKey } from "@/lib/time-zone";

// Notifications as the Dashboard shows them: the bell in the shell (a dot
// while anything is unread, the latest few in a popover) and /notifications
// (every notification, newest first, grouped by day). Everything here is
// presentation over the Notifications API; read state lives in the service.

export type Notification = components["schemas"]["Notification"];
export type NotificationImportance =
  components["schemas"]["NotificationImportance"];
export type Delivery = components["schemas"]["Delivery"];
export type NotificationList = components["schemas"]["NotificationList"];
export type UnreadCount = components["schemas"]["UnreadCount"];
export type CreateNotificationRequest =
  components["schemas"]["CreateNotificationRequest"];

export const IMPORTANCES: NotificationImportance[] = ["low", "medium", "high"];

/** Notifications in the bell's popover. */
export const POPOVER_LIMIT = 6;
/** Notifications per page on /notifications. */
export const PAGE_LIMIT = 50;
/** How often the bell reads the unread count while the tab is visible. */
export const UNREAD_REFRESH_MS = 60_000;
/** Focus and visibility both fire on a tab switch; one read is enough. */
export const MIN_REFRESH_GAP_MS = 2_000;

export function importanceLabel(importance: NotificationImportance) {
  return importance.charAt(0).toUpperCase() + importance.slice(1);
}

/** The importance badge, in the status colors (readable in both themes). */
export const IMPORTANCE_BADGES: Record<NotificationImportance, string> = {
  high: "bg-destructive-subtle text-destructive border-destructive/30",
  medium: "bg-warning-subtle text-warning border-warning/30",
  low: "bg-muted text-muted-foreground border-border",
};

/** The small importance marker beside a notification in the popover. */
export const IMPORTANCE_DOTS: Record<NotificationImportance, string> = {
  high: "bg-destructive",
  medium: "bg-warning",
  low: "bg-muted-foreground/40",
};

export function isUnread(notification: Pick<Notification, "readAt">) {
  return !notification.readAt;
}

export type BellState = {
  /** No dot, a dot, or a dot tinted because something important is unread. */
  dot: "none" | "unread" | "high";
  label: string;
};

/**
 * The bell shows a dot, never a number, while anything is unread; the count
 * is in its accessible name. An unknown count (the service could not be
 * read) shows no dot.
 */
export function bellState(count: UnreadCount | null | undefined): BellState {
  if (!count || count.count <= 0)
    return { dot: "none", label: "Notifications" };
  return {
    dot: count.highCount > 0 ? "high" : "unread",
    label: `Notifications, ${count.count} unread`,
  };
}

// ---- Text ------------------------------------------------------------------

/** The body on one line, shortened to `max` characters for a preview. */
export function preview(body: string, max = 140) {
  const flat = body.replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1).trimEnd()}…` : flat;
}

const SOURCE_LABELS: Record<string, string> = {
  calendar: "Calendar",
  dashboard: "Dashboard",
  dev: "Developer tool",
  finance: "Finance",
  inbox: "Inbox",
  status: "Status",
  tasks: "Tasks",
  voice: "Voice",
};

export function sourceLabel(source: string) {
  const known = SOURCE_LABELS[source];
  if (known) return known;
  const words = source.replace(/[._-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * Sources offered by the filter: the known producers, those on the page, and
 * the one filtered by, alphabetically by label.
 */
export function sourceOptions(seen: readonly string[], current?: string) {
  const values = new Set([
    ...Object.keys(SOURCE_LABELS),
    ...seen,
    ...(current ? [current] : []),
  ]);
  return [...values]
    .map((value) => ({ value, label: sourceLabel(value) }))
    .sort((a, b) => a.label.localeCompare(b.label));
}

/**
 * The notification's link when it is a Life OS path. The service accepts
 * only such paths; anything else is not followed.
 */
export function safeLink(link: string | null | undefined) {
  return link && /^\/(?:[^/\\]|$)/.test(link) ? link : undefined;
}

// ---- Time ------------------------------------------------------------------

const CLOCK: Intl.DateTimeFormatOptions = {
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
};

/** The 24-hour wall-clock time, such as "14:05", in the timezone. */
export function clockTime(instant: string, timeZone: string) {
  return dateTimeFormat(timeZone, CLOCK).format(new Date(instant));
}

/** For example "9 Oct 2026, 20:22 CEST" in the timezone. */
export function fullTime(instant: string, timeZone: string) {
  return dateTimeFormat(timeZone, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  }).format(new Date(instant));
}

/**
 * How long ago, for the popover: "Just now", "5 min ago", "3 h ago" earlier
 * today, "Yesterday, 21:30", then the date and time ("8 Oct, 14:05").
 */
export function relativeTime(instant: string, timeZone: string, now: Date) {
  const minutes = Math.floor((now.getTime() - Date.parse(instant)) / 60_000);
  if (minutes < 1) return "Just now";
  if (minutes < 60) return `${minutes} min ago`;
  const day = dayKey(instant, timeZone);
  const today = dayKey(now, timeZone);
  if (day === today) return `${Math.floor(minutes / 60)} h ago`;
  if (day === addDays(today, -1)) {
    return `Yesterday, ${clockTime(instant, timeZone)}`;
  }
  return formatInboxTime(instant, timeZone, now);
}

export type NotificationDay = {
  /** YYYY-MM-DD in the timezone. */
  day: string;
  /** "Today", "Yesterday", or a date such as "Mon 12 Oct". */
  label: string;
  items: Notification[];
};

/**
 * Notifications grouped by the day (in the timezone) Notifications received
 * them, keeping the list's newest-first order.
 */
export function groupByDay(
  items: readonly Notification[],
  timeZone: string,
  now: Date,
): NotificationDay[] {
  const today = dayKey(now, timeZone);
  const days = new Map<string, NotificationDay>();
  for (const item of items) {
    const day = dayKey(item.createdAt, timeZone);
    let group = days.get(day);
    if (!group) {
      group = { day, label: relativeDayLabel(day, today), items: [] };
      days.set(day, group);
    }
    group.items.push(item);
  }
  return [...days.values()];
}

/**
 * When the notified thing happened, if that is not (within a minute) when
 * Notifications received it, such as a reminder sent late.
 */
export function happenedAt(
  notification: Pick<Notification, "occurredAt" | "createdAt">,
) {
  const gap =
    Date.parse(notification.createdAt) - Date.parse(notification.occurredAt);
  return Math.abs(gap) >= 60_000 ? notification.occurredAt : undefined;
}

// ---- Delivery --------------------------------------------------------------

/** With no channel configured, a notification is only listed here. */
export const LIFE_OS_ONLY = "Shown in Life OS only";

export type DeliveryView = {
  /** For example "ntfy: sent 14:05" or "ntfy: retrying, next try 14:10". */
  text: string;
  tone: "success" | "warning" | "destructive" | "muted";
  /** The latest error, for retrying and failed deliveries. */
  error?: string;
};

function attempts(count: number) {
  return `${count} attempt${count === 1 ? "" : "s"}`;
}

/** One channel's delivery state, times in the timezone. */
export function deliveryView(
  delivery: Delivery,
  timeZone: string,
  now: Date,
): DeliveryView {
  const at = (instant: string) => formatInboxTime(instant, timeZone, now);
  const channel = delivery.channel;
  const error = delivery.lastError?.trim() || undefined;
  switch (delivery.status) {
    case "sent":
      return {
        text: `${channel}: sent${delivery.sentAt ? ` ${at(delivery.sentAt)}` : ""}`,
        tone: "success",
      };
    case "pending":
      return { text: `${channel}: sending`, tone: "muted" };
    case "retrying":
      return {
        text: `${channel}: retrying after ${attempts(delivery.attempts)}${
          delivery.nextAttemptAt
            ? `, next try ${at(delivery.nextAttemptAt)}`
            : ""
        }`,
        tone: "warning",
        error,
      };
    case "failed":
      return {
        text: `${channel}: failed after ${attempts(delivery.attempts)}`,
        tone: "destructive",
        error,
      };
    case "skipped":
      return { text: `${channel}: skipped`, tone: "muted" };
  }
}

// ---- Filters ---------------------------------------------------------------

export type SearchParams = Record<string, string | string[] | undefined>;

export type NotificationFilters = {
  importance?: NotificationImportance;
  source?: string;
  /** Only unread notifications. */
  unread?: true;
};

function single(value: string | string[] | undefined) {
  return (Array.isArray(value) ? value[0] : value)?.trim() || undefined;
}

/** A source as the API accepts it. */
const SOURCE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

/** Filters from /notifications search params; invalid values are dropped. */
export function parseFilters(params: SearchParams): NotificationFilters {
  const importance = single(params.importance);
  const source = single(params.source);
  const filters: NotificationFilters = {
    importance: IMPORTANCES.find((value) => value === importance),
    source: source && SOURCE.test(source) ? source : undefined,
    unread: single(params.unread) === "true" ? true : undefined,
  };
  return Object.fromEntries(
    Object.entries(filters).filter(([, value]) => value !== undefined),
  ) as NotificationFilters;
}

export function hasFilters(filters: NotificationFilters) {
  return Boolean(filters.importance || filters.source || filters.unread);
}

/** A /notifications URL with the given filters. */
export function notificationsHref(filters: NotificationFilters) {
  const params = new URLSearchParams();
  if (filters.importance) params.set("importance", filters.importance);
  if (filters.source) params.set("source", filters.source);
  if (filters.unread) params.set("unread", "true");
  const query = params.toString();
  return query ? `/notifications?${query}` : "/notifications";
}

/** The Notifications API list query for the filters and page. */
export function listQuery(
  filters: NotificationFilters,
  cursor?: string,
  limit = PAGE_LIMIT,
) {
  return {
    importance: filters.importance,
    source: filters.source,
    unread: filters.unread,
    cursor,
    limit,
  };
}

/** Why the list is empty. */
export function emptyMessage(filters: NotificationFilters) {
  return hasFilters(filters)
    ? "No notifications match these filters."
    : "No notifications yet.";
}

// ---- Read state ------------------------------------------------------------

/** The list with one notification replaced by its updated copy. */
export function replaceNotification(
  items: readonly Notification[],
  updated: Notification,
) {
  return items.map((item) => (item.id === updated.id ? updated : item));
}

/**
 * The list as it is once "Mark all read" with `before` succeeded: unread
 * notifications created at or before it are read at `readAt`.
 */
export function markAllReadLocally(
  items: readonly Notification[],
  before: string | undefined,
  readAt: string,
) {
  const cutoff = before ? Date.parse(before) : Infinity;
  return items.map((item) =>
    isUnread(item) && Date.parse(item.createdAt) <= cutoff
      ? { ...item, readAt }
      : item,
  );
}

/**
 * The `before` for "Mark all read": the latest of the moment the list was
 * read and its newest notification, so one that arrives while the owner
 * looks at the list stays unread.
 */
export function markAllBefore(
  items: readonly Pick<Notification, "createdAt">[],
  readAt?: string,
) {
  const instants = [
    ...items.map((item) => item.createdAt),
    ...(readAt ? [readAt] : []),
  ].filter((value) => !Number.isNaN(Date.parse(value)));
  if (!instants.length) return undefined;
  return instants.reduce((latest, value) =>
    Date.parse(value) > Date.parse(latest) ? value : latest,
  );
}

// ---- Test notification -----------------------------------------------------

export const TEST_CATEGORY = "test.manual";

function uniqueKey() {
  // crypto.randomUUID needs a secure context; the Dashboard may run on http.
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/**
 * A test notification as the owner sends it from /notifications. The source
 * is left to the service, which records the owner's as "dashboard"; the
 * dedupe key is unique, so every press sends a new one.
 */
export function testNotification(
  importance: NotificationImportance,
  key = uniqueKey(),
): CreateNotificationRequest {
  return {
    title: `Test notification (${importance})`,
    body: "Sent from the Dashboard to check that notifications reach you.",
    importance,
    category: TEST_CATEGORY,
    link: "/notifications",
    dedupeKey: `${TEST_CATEGORY}:${key}`,
  };
}

// ---- Changes between the bell and the page ---------------------------------

/** Window event after the bell or the page changed notifications. */
export const NOTIFICATIONS_CHANGED = "lifeos:notifications-changed";

export type NotificationsChangedDetail = { origin: "bell" | "page" };

/** Browser only: tells the bell (and the page) to read their state again. */
export function announceNotificationsChanged(
  origin: NotificationsChangedDetail["origin"],
) {
  window.dispatchEvent(
    new CustomEvent<NotificationsChangedDetail>(NOTIFICATIONS_CHANGED, {
      detail: { origin },
    }),
  );
}

// ---- Requests --------------------------------------------------------------
// Each resolves to null when Notifications could not be reached or refused
// the request, so a missing service never throws into the shell.

export async function fetchUnreadCount(): Promise<UnreadCount | null> {
  try {
    const { data } = await notificationsClient.GET(
      "/v1/notifications/unread-count",
    );
    return data ?? null;
  } catch {
    return null;
  }
}

export async function fetchNotifications(
  query: ReturnType<typeof listQuery>,
): Promise<NotificationList | null> {
  try {
    const { data } = await notificationsClient.GET("/v1/notifications", {
      params: { query },
    });
    return data ?? null;
  } catch {
    return null;
  }
}

export async function markRead(id: string): Promise<Notification | null> {
  try {
    const { data } = await notificationsClient.POST(
      "/v1/notifications/{notificationId}/read",
      { params: { path: { notificationId: id } } },
    );
    return data ?? null;
  } catch {
    return null;
  }
}

export async function markAllRead(before?: string): Promise<number | null> {
  try {
    const { data } = await notificationsClient.POST(
      "/v1/notifications/read-all",
      { body: before ? { before } : {} },
    );
    return data?.marked ?? null;
  } catch {
    return null;
  }
}

export async function sendNotification(
  body: CreateNotificationRequest,
): Promise<Notification | null> {
  try {
    const { data } = await notificationsClient.POST("/v1/notifications", {
      body,
    });
    return data ?? null;
  } catch {
    return null;
  }
}

// ---- Loading ---------------------------------------------------------------

export type NotificationsPage = {
  filters: NotificationFilters;
  /** The owner's timezone, for day groups and displayed times. */
  timeZone: string;
  /** When the server read the list (ISO), the page's "now". */
  renderedAt: string;
  /** `null` when Notifications could not be read. */
  list: NotificationList | null;
  unread: UnreadCount | null;
};

export async function getNotificationsPage(
  params: SearchParams,
  timeZone: string,
): Promise<NotificationsPage> {
  const filters = parseFilters(params);
  const renderedAt = new Date().toISOString();
  const [list, unread] = await Promise.all([
    fetchNotifications(listQuery(filters)),
    fetchUnreadCount(),
  ]);
  return { filters, timeZone, renderedAt, list, unread };
}
