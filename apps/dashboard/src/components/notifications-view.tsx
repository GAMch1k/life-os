"use client";

import { ArrowRight, CheckCheck, Send } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";

import { PushSettings } from "@/components/push-settings";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { formatInboxTime } from "@/lib/inbox-summary";
import {
  announceNotificationsChanged,
  clockTime,
  type Delivery,
  deliveryView,
  type DeliveryView,
  emptyMessage,
  fetchNotifications,
  fetchUnreadCount,
  fullTime,
  groupByDay,
  happenedAt,
  hasFilters,
  IMPORTANCE_BADGES,
  IMPORTANCES,
  importanceLabel,
  isUnread,
  LIFE_OS_ONLY,
  listQuery,
  markAllBefore,
  markAllRead,
  markAllReadLocally,
  markRead,
  type Notification,
  type NotificationFilters,
  type NotificationImportance,
  NOTIFICATIONS_CHANGED,
  type NotificationsChangedDetail,
  type NotificationsPage,
  replaceNotification,
  safeLink,
  sendNotification,
  sourceLabel,
  sourceOptions,
  testNotification,
} from "@/lib/notifications";
import { cn } from "@/lib/utils";

const fieldClass =
  "border-input bg-background focus-visible:ring-ring/50 h-9 w-full rounded-lg border px-2.5 text-sm focus-visible:ring-3 focus-visible:outline-none";

const DELIVERY_TONES: Record<DeliveryView["tone"], string> = {
  success: "text-success",
  warning: "text-warning",
  destructive: "text-destructive",
  muted: "text-muted-foreground",
};

/**
 * /notifications: every notification, newest first and grouped by day, with
 * filters, read state, delivery state per channel, notifications on this
 * device (web push), and a test notification. The server reads the first
 * page; "Show more" reads the next ones here.
 */
export function NotificationsView({ page }: { page: NotificationsPage }) {
  const { filters, renderedAt, timeZone } = page;
  const router = useRouter();
  const [items, setItems] = useState(page.list?.items ?? []);
  const [cursor, setCursor] = useState(page.list?.nextCursor ?? undefined);
  const [unread, setUnread] = useState(page.unread);
  const [moreState, setMoreState] = useState<"idle" | "loading" | "failed">(
    "idle",
  );
  const [error, setError] = useState<string>();
  // A router.refresh() (after a test notification, or a read in the bell)
  // renders the page again with the current list; it replaces this one.
  const [loaded, setLoaded] = useState(page);
  if (loaded !== page) {
    setLoaded(page);
    setItems(page.list?.items ?? []);
    setCursor(page.list?.nextCursor ?? undefined);
    setUnread(page.unread);
    setMoreState("idle");
  }
  const now = new Date(renderedAt);

  useEffect(() => {
    function onChanged(event: Event) {
      const { detail } = event as CustomEvent<NotificationsChangedDetail>;
      if (detail?.origin === "bell") router.refresh();
    }
    window.addEventListener(NOTIFICATIONS_CHANGED, onChanged);
    return () => window.removeEventListener(NOTIFICATIONS_CHANGED, onChanged);
  }, [router]);

  function changed() {
    announceNotificationsChanged("page");
    void fetchUnreadCount().then((next) => {
      if (next) setUnread(next);
    });
  }

  async function read(item: Notification) {
    if (!isUnread(item)) return;
    const updated = await markRead(item.id);
    if (!updated) {
      setError("Couldn't mark the notification read. Try again.");
      return;
    }
    setError(undefined);
    setItems((current) => replaceNotification(current, updated));
    changed();
  }

  async function readAll() {
    // Up to what this page has shown, so a newer arrival stays unread.
    const before = markAllBefore(items, renderedAt);
    if ((await markAllRead(before)) === null) {
      setError("Couldn't mark notifications read. Try again.");
      return;
    }
    setError(undefined);
    const readAt = new Date().toISOString();
    setItems((current) => markAllReadLocally(current, before, readAt));
    changed();
  }

  async function showMore() {
    if (!cursor) return;
    setMoreState("loading");
    const next = await fetchNotifications(listQuery(filters, cursor));
    if (!next) {
      setMoreState("failed");
      return;
    }
    setMoreState("idle");
    setItems((current) => {
      const shown = new Set(current.map((item) => item.id));
      return [...current, ...next.items.filter((item) => !shown.has(item.id))];
    });
    setCursor(next.nextCursor ?? undefined);
  }

  const unavailable = page.list === null;
  const anyUnread = (unread?.count ?? 0) > 0 || items.some(isUnread);

  return (
    <div className="mx-auto max-w-3xl space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 className="text-xl font-semibold tracking-tight">Notifications</h1>
        {unread ? (
          <span className="text-muted-foreground text-sm">
            {unread.count ? `${unread.count} unread` : "All read"}
          </span>
        ) : null}
        <Button
          className="ml-auto"
          disabled={unavailable || !anyUnread}
          onClick={() => void readAll()}
          size="sm"
          variant="outline"
        >
          <CheckCheck aria-hidden="true" />
          Mark all read
        </Button>
      </div>
      <PushSettings timeZone={timeZone} />
      <TestNotification onSent={() => router.refresh()} />
      <Filters
        filters={filters}
        sources={sourceOptions(
          items.map((item) => item.source),
          filters.source,
        )}
      />
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <Card>
        <CardContent>
          {unavailable ? (
            <p className="text-muted-foreground py-6 text-center">
              Notifications are unavailable right now.
            </p>
          ) : items.length === 0 ? (
            <div className="space-y-2 py-6 text-center">
              <p className="text-muted-foreground">{emptyMessage(filters)}</p>
              {hasFilters(filters) ? (
                <Link
                  className="text-primary underline-offset-4 hover:underline"
                  href="/notifications"
                >
                  Show all notifications
                </Link>
              ) : null}
            </div>
          ) : (
            <div className="space-y-4">
              {groupByDay(items, timeZone, now).map((day) => (
                <section aria-labelledby={`day-${day.day}`} key={day.day}>
                  <h2
                    className="text-muted-foreground text-xs font-semibold tracking-wide uppercase"
                    id={`day-${day.day}`}
                  >
                    {day.label}
                  </h2>
                  <ol className="divide-y">
                    {day.items.map((item) => (
                      <li key={item.id}>
                        <NotificationRow
                          item={item}
                          now={now}
                          onRead={(entry) => void read(entry)}
                          timeZone={timeZone}
                        />
                      </li>
                    ))}
                  </ol>
                </section>
              ))}
            </div>
          )}
          {cursor ? (
            <div className="mt-4 flex flex-col items-center gap-1">
              <Button
                disabled={moreState === "loading"}
                onClick={() => void showMore()}
                variant="outline"
              >
                {moreState === "loading" ? "Loading…" : "Show more"}
              </Button>
              {moreState === "failed" ? (
                <p className="text-destructive text-xs" role="alert">
                  Couldn&apos;t load more notifications. Try again.
                </p>
              ) : null}
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}

function NotificationRow({
  item,
  now,
  onRead,
  timeZone,
}: {
  item: Notification;
  now: Date;
  onRead: (item: Notification) => void;
  timeZone: string;
}) {
  const unread = isUnread(item);
  const link = safeLink(item.link);
  const happened = happenedAt(item);
  const titleId = `notification-${item.id}`;
  return (
    <article
      aria-labelledby={titleId}
      className={cn("-mx-2 rounded-lg px-2 py-3", unread && "bg-muted/40")}
    >
      <div className="flex items-start gap-2">
        <Badge className={IMPORTANCE_BADGES[item.importance]} variant="outline">
          {importanceLabel(item.importance)}
        </Badge>
        <h3
          className={cn(
            "min-w-0 flex-1 wrap-anywhere",
            unread ? "font-semibold" : "text-muted-foreground font-medium",
          )}
          id={titleId}
        >
          {item.title}
        </h3>
        {unread ? (
          <span className="text-primary shrink-0 text-xs font-medium">
            Unread
          </span>
        ) : null}
      </div>
      {item.body ? (
        <p className="mt-1 wrap-anywhere whitespace-pre-line">{item.body}</p>
      ) : null}
      <p className="text-muted-foreground mt-1 text-xs">
        <time
          dateTime={item.createdAt}
          title={fullTime(item.createdAt, timeZone)}
        >
          {clockTime(item.createdAt, timeZone)}
        </time>
        {happened ? (
          <>
            {" · happened "}
            <time dateTime={happened} title={fullTime(happened, timeZone)}>
              {formatInboxTime(happened, timeZone, now)}
            </time>
          </>
        ) : null}
        {" · "}
        {sourceLabel(item.source)} · {item.category}
      </p>
      <Deliveries deliveries={item.deliveries} now={now} timeZone={timeZone} />
      {link || unread ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          {link ? (
            <Link
              aria-label={`Open ${item.title}`}
              className={buttonVariants({ size: "xs", variant: "outline" })}
              href={link}
              onClick={() => onRead(item)}
            >
              Open <ArrowRight aria-hidden="true" />
            </Link>
          ) : null}
          {unread ? (
            <Button
              aria-label={`Mark ${item.title} read`}
              onClick={() => onRead(item)}
              size="xs"
              variant="ghost"
            >
              Mark read
            </Button>
          ) : null}
        </div>
      ) : null}
    </article>
  );
}

/** Where the notification went: each channel's state, or Life OS only. */
function Deliveries({
  deliveries,
  now,
  timeZone,
}: {
  deliveries: Delivery[];
  now: Date;
  timeZone: string;
}) {
  if (!deliveries.length) {
    return <p className="text-muted-foreground mt-1 text-xs">{LIFE_OS_ONLY}</p>;
  }
  return (
    <ul aria-label="Deliveries" className="mt-1 space-y-0.5 text-xs">
      {deliveries.map((delivery) => {
        const view = deliveryView(delivery, timeZone, now);
        return (
          <li className="wrap-anywhere" key={delivery.channel}>
            <span className={DELIVERY_TONES[view.tone]}>{view.text}</span>
            {view.error ? (
              <span className="text-muted-foreground"> — {view.error}</span>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}

function Filters({
  filters,
  sources,
}: {
  filters: NotificationFilters;
  sources: { value: string; label: string }[];
}) {
  return (
    <form
      action="/notifications"
      aria-label="Filter notifications"
      className="bg-background grid grid-cols-2 items-end gap-3 rounded-xl border p-3 sm:grid-cols-[repeat(2,minmax(0,1fr))_auto_auto] sm:p-4"
    >
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">Importance</span>
        <select
          className={fieldClass}
          defaultValue={filters.importance ?? ""}
          name="importance"
        >
          <option value="">Any importance</option>
          {IMPORTANCES.map((importance) => (
            <option key={importance} value={importance}>
              {importanceLabel(importance)}
            </option>
          ))}
        </select>
      </label>
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">Source</span>
        <select
          className={fieldClass}
          defaultValue={filters.source ?? ""}
          name="source"
        >
          <option value="">All sources</option>
          {sources.map((source) => (
            <option key={source.value} value={source.value}>
              {source.label}
            </option>
          ))}
        </select>
      </label>
      <label className="flex h-9 items-center gap-2 text-sm">
        <input
          className="accent-primary size-4"
          defaultChecked={filters.unread}
          name="unread"
          type="checkbox"
          value="true"
        />
        Unread only
      </label>
      <div className="flex items-center gap-2">
        <Button size="lg" type="submit">
          Apply
        </Button>
        {hasFilters(filters) ? (
          <Link
            className="text-muted-foreground hover:text-foreground h-9 px-1 text-sm leading-9 underline-offset-4 hover:underline"
            href="/notifications"
          >
            Clear
          </Link>
        ) : null}
      </div>
    </form>
  );
}

/**
 * Sends a test notification of the chosen importance through the same path
 * as every other: it is listed here and goes to every configured channel.
 */
function TestNotification({ onSent }: { onSent: () => void }) {
  const [importance, setImportance] =
    useState<NotificationImportance>("medium");
  const [sending, setSending] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string }>();

  async function send() {
    setSending(true);
    const created = await sendNotification(testNotification(importance));
    setSending(false);
    if (!created) {
      setResult({
        ok: false,
        text: "Couldn't send the test notification. Notifications may be unavailable.",
      });
      return;
    }
    setResult({ ok: true, text: `Sent “${created.title}”.` });
    announceNotificationsChanged("page");
    onSent();
  }

  return (
    <Card aria-labelledby="test-notification-title" role="region" size="sm">
      <CardContent className="space-y-2">
        <div>
          <h2 className="text-sm font-semibold" id="test-notification-title">
            Try notifications
          </h2>
          <p className="text-muted-foreground text-xs">
            Sends a test notification the way Life OS sends any other: it
            appears here and on every channel configured on the server.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-sm">
            <span className="font-medium">Test importance</span>
            <select
              className={cn(fieldClass, "w-auto")}
              onChange={(event) =>
                setImportance(event.target.value as NotificationImportance)
              }
              value={importance}
            >
              {IMPORTANCES.map((value) => (
                <option key={value} value={value}>
                  {importanceLabel(value)}
                </option>
              ))}
            </select>
          </label>
          <Button disabled={sending} onClick={() => void send()} size="lg">
            <Send aria-hidden="true" />
            Send test notification
          </Button>
        </div>
        <p
          className={cn(
            "text-sm empty:hidden",
            result?.ok ? "text-success" : "text-destructive",
          )}
          role="status"
        >
          {result?.text}
        </p>
      </CardContent>
    </Card>
  );
}
