// The Life OS service worker. It makes the Dashboard install as an app (home
// screen, Share → Life OS) and shows the notifications Life OS pushes to this
// device (web push, docs/services/notifications.md#web-push). It has no fetch
// handler and no cache: every page, API response, and shared item goes to the
// network exactly as without it, so nothing private is ever stored on the
// phone. See docs/frontend/dashboard.md ("Install on your phone").

/** Where a notification without a usable link opens. */
const FALLBACK_LINK = "/notifications";
const ICON = "/icons/icon-192.png";
/** Android shows the badge in the status bar, as a silhouette. */
const BADGE = "/icons/badge-96.png";

self.addEventListener("install", () => {
  // A new version takes over at once; there is nothing to migrate.
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(self.clients.claim());
});

/** A Life OS path such as /calendar?date=…; anything else is not followed. */
function safeLink(link) {
  return typeof link === "string" && /^\/(?:[^/\\]|$)/.test(link)
    ? link
    : FALLBACK_LINK;
}

/**
 * The notification for a push: the payload the notifications service sends
 * (title, body, link, tag, importance, timestamp), or a generic one when it
 * cannot be read. Chrome requires every push to show a notification.
 */
function notificationFor(data) {
  let payload = {};
  try {
    payload = data ? data.json() : {};
  } catch {
    payload = {};
  }
  if (!payload || typeof payload !== "object") payload = {};
  const title =
    typeof payload.title === "string" && payload.title.trim()
      ? payload.title
      : "Life OS";
  const options = {
    body: typeof payload.body === "string" ? payload.body : "",
    icon: ICON,
    badge: BADGE,
    data: { link: safeLink(payload.link) },
    lang: "en",
    // Low importance arrives without sound or vibration, as on ntfy.
    silent: payload.importance === "low",
  };
  if (typeof payload.tag === "string" && payload.tag) {
    // A notification with the tag of one already shown replaces it.
    options.tag = payload.tag;
  }
  if (Number.isFinite(payload.timestamp)) {
    options.timestamp = payload.timestamp;
  }
  return { title, options };
}

self.addEventListener("push", (event) => {
  const { title, options } = notificationFor(event.data);
  event.waitUntil(self.registration.showNotification(title, options));
});

/**
 * Opens the link: an open Life OS window is focused and navigated to it,
 * otherwise a new one opens (on Android, in the installed app when it is
 * installed). Both are ordinary navigations, with the Cloudflare Access
 * cookie; when the session has expired, Access asks for the code first.
 */
async function openLink(link) {
  const target = new URL(link, self.location.origin).href;
  // The windows this worker controls, the most recently focused first.
  const windows = await self.clients.matchAll({ type: "window" });
  const open = windows[0];
  if (open) {
    try {
      // Focus first: the click allows it only right away.
      const focused = (await open.focus()) ?? open;
      const navigated = await focused.navigate(target);
      if (navigated) return navigated;
    } catch {
      // It could not be focused or navigated: open a new window instead.
    }
  }
  return self.clients.openWindow(target);
}

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data;
  event.waitUntil(openLink(safeLink(data && data.link)));
});

/**
 * The browser replaced the subscription (rare; a push service rotating it):
 * subscribe again with the same key and tell Life OS, so pushes keep
 * arriving. The old one is removed when its push service reports it gone.
 */
async function resubscribe(event) {
  const old = event.oldSubscription;
  const key = old && old.options ? old.options.applicationServerKey : null;
  const subscription =
    event.newSubscription ||
    (key
      ? await self.registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: key,
        })
      : null);
  if (!subscription) return;
  await fetch("/api/notifications/v1/push/subscriptions", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "X-Life-OS-Actor": "user",
    },
    body: JSON.stringify(subscription.toJSON()),
  });
}

self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil(resubscribe(event).catch(() => {}));
});
