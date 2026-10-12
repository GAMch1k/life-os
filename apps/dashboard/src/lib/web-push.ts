import { notificationsClient } from "@/lib/api/notifications-client";
import type { components } from "@/lib/api/generated/notifications";
import { formatInboxTime } from "@/lib/inbox-summary";
import { SERVICE_WORKER_PATH } from "@/lib/web-app";

// Web push on this device: "Notifications on this device" on /notifications.
// The browser subscribes with the server's VAPID key, and Notifications
// stores the subscription and pushes every notification to it through the
// browser's push service (docs/services/notifications.md#web-push). The
// service worker (public/sw.js) shows them.

export type PushConfig = components["schemas"]["PushConfig"];
export type PushDevice = components["schemas"]["PushSubscription"];
export type PushSubscriptionRequest =
  components["schemas"]["PushSubscriptionRequest"];
export type PushTestDeviceResult =
  components["schemas"]["PushTestDeviceResult"];

/** Shown with the setting: why to turn it on as well as ntfy. */
export const PUSH_EXPLAINER =
  "Works even where ntfy is blocked, as on some work networks.";

// ---- Keys ------------------------------------------------------------------

/**
 * The bytes of a base64url string, such as the server's VAPID public key the
 * browser subscribes with (applicationServerKey). Padding and the standard
 * alphabet (+ and /) are accepted too.
 */
export function base64UrlToBytes(value: string): Uint8Array<ArrayBuffer> {
  const base64 = value
    .trim()
    .replace(/=+$/, "")
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  const binary = atob(base64 + "=".repeat((4 - (base64.length % 4)) % 4));
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

/** Bytes as base64url without padding. */
export function bytesToBase64Url(bytes: ArrayBuffer | ArrayBufferView) {
  const view =
    bytes instanceof ArrayBuffer
      ? new Uint8Array(bytes)
      : new Uint8Array(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let binary = "";
  for (const byte of view) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/**
 * Whether a subscription was made with the server's current key. A
 * subscription made with another key (the server's keys were replaced) gets
 * every push refused, so it is replaced.
 */
export function sameKey(
  applicationServerKey: ArrayBuffer | null | undefined,
  publicKey: string,
) {
  if (!applicationServerKey) return false;
  try {
    return (
      bytesToBase64Url(applicationServerKey) ===
      bytesToBase64Url(base64UrlToBytes(publicKey))
    );
  } catch {
    return false;
  }
}

/** A browser subscription as the API takes it. */
export function subscriptionRequest(
  json: PushSubscriptionJSON,
): PushSubscriptionRequest {
  return {
    endpoint: json.endpoint ?? "",
    keys: { p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "" },
  };
}

// ---- State -----------------------------------------------------------------

/**
 * What this browser can do: push needs a secure context (https or
 * localhost), a service worker, the Push API, and notifications. On an
 * iPhone or iPad, only a Home Screen app has the Push API.
 */
export type PushSupport =
  "supported" | "insecure" | "ios-browser" | "unsupported";

export type PushEnvironment = {
  secure: boolean;
  serviceWorker: boolean;
  pushManager: boolean;
  notification: boolean;
  ios: boolean;
  standalone: boolean;
};

export function pushSupport(env: PushEnvironment): PushSupport {
  if (!env.secure) return "insecure";
  if (env.serviceWorker && env.pushManager && env.notification) {
    return "supported";
  }
  if (env.ios && !env.standalone) return "ios-browser";
  return "unsupported";
}

/** This browser's support (browser only). */
export function readPushSupport(): PushSupport {
  const ua = navigator.userAgent;
  return pushSupport({
    secure: window.isSecureContext,
    serviceWorker: "serviceWorker" in navigator,
    pushManager: "PushManager" in window,
    notification: "Notification" in window,
    // iPadOS reports itself as a Mac with touch.
    ios:
      /iPhone|iPad|iPod/.test(ua) ||
      (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1),
    standalone: window.matchMedia("(display-mode: standalone)").matches,
  });
}

export type PushState =
  | "loading"
  | "unavailable"
  | "unsupported"
  | "unconfigured"
  | "denied"
  | "off"
  | "on";

export type PushStateInput = {
  /** undefined until known (in the browser). */
  support: PushSupport | undefined;
  /** undefined while loading; null when Notifications could not be read. */
  config: PushConfig | null | undefined;
  permission: NotificationPermission | undefined;
  /** This browser's subscription endpoint, null without one. */
  endpoint: string | null | undefined;
  /** The subscribed devices, null when they could not be read. */
  devices: readonly PushDevice[] | null | undefined;
};

/**
 * The setting's state on this device: on only when the browser has a
 * subscription, may show notifications, and the server has it (a
 * subscription the server pruned, or removed in the list, is off and is
 * sent again by turning it on).
 */
export function pushState(input: PushStateInput): PushState {
  const { support, config, permission, endpoint, devices } = input;
  if (support === undefined) return "loading";
  if (support !== "supported") return "unsupported";
  if (config === undefined || endpoint === undefined) return "loading";
  if (config === null) return "unavailable";
  if (!config.configured) return "unconfigured";
  if (permission === "denied") return "denied";
  if (
    permission === "granted" &&
    endpoint &&
    devices?.some((device) => device.endpoint === endpoint)
  ) {
    return "on";
  }
  return "off";
}

export type PushStateView = {
  summary: string;
  /** More about it, such as how to allow notifications again. */
  hint?: string;
  /** The button the state offers. */
  action?: "enable" | "disable";
};

const DENIED_HINT =
  "To allow them on Android: long-press the Life OS app icon, tap App info → Notifications, and allow them (in Chrome: tap the icon left of the address → Permissions → Notifications). Then reload this page.";

const NOT_DELIVERING =
  "The server does not deliver notifications by web push yet (webpush is not in LIFE_OS_NOTIFICATIONS_CHANNELS): only test notifications arrive.";

/** What the setting says and offers in a state. */
export function pushStateView(
  state: PushState,
  support: PushSupport | undefined,
  config: PushConfig | null | undefined,
): PushStateView {
  const notDelivering =
    config?.configured && !config.delivering ? NOT_DELIVERING : undefined;
  switch (state) {
    case "loading":
      return { summary: "Checking this device…" };
    case "unavailable":
      return { summary: "Notifications are unavailable right now." };
    case "unsupported":
      if (support === "insecure") {
        return {
          summary: "This address can't receive notifications.",
          hint: "Open Life OS at its https address (such as https://lifeos.gamch1k.com), not the WireGuard address, to turn them on.",
        };
      }
      if (support === "ios-browser") {
        return {
          summary: "Safari gets notifications only in the Home Screen app.",
          hint: "Tap Share → Add to Home Screen, open Life OS from there, and turn notifications on in it.",
        };
      }
      return {
        summary: "This browser can't receive notifications from Life OS.",
        hint: "On Android, use Chrome.",
      };
    case "unconfigured":
      return {
        summary: "Web push is not set up on the server yet.",
        hint: "It needs its VAPID keys (LIFE_OS_WEBPUSH_* in .env; docs/services/notifications.md).",
      };
    case "denied":
      return {
        summary: "Notifications are blocked for Life OS on this device.",
        hint: DENIED_HINT,
      };
    case "off":
      return {
        summary: "Off on this device.",
        hint: notDelivering,
        action: "enable",
      };
    case "on":
      return {
        summary: "On: this device gets Life OS notifications.",
        hint: notDelivering,
        action: "disable",
      };
  }
}

// ---- Devices ---------------------------------------------------------------

/** One device's latest outcome, times in the timezone. */
export function deviceStatus(
  device: Pick<PushDevice, "lastSuccessAt" | "lastFailureAt" | "lastError">,
  timeZone: string,
  now: Date,
): { text: string; tone: "success" | "warning" | "muted"; error?: string } {
  const at = (instant: string) => formatInboxTime(instant, timeZone, now);
  const success = device.lastSuccessAt ? Date.parse(device.lastSuccessAt) : 0;
  const failure = device.lastFailureAt ? Date.parse(device.lastFailureAt) : 0;
  if (device.lastFailureAt && failure > success) {
    return {
      text: `Last push failed ${at(device.lastFailureAt)}`,
      tone: "warning",
      error: device.lastError?.trim() || undefined,
    };
  }
  if (device.lastSuccessAt) {
    return { text: `Last push ${at(device.lastSuccessAt)}`, tone: "success" };
  }
  return { text: "Nothing pushed yet", tone: "muted" };
}

/** A test push's outcome for one device, in a sentence. */
export function testResultText(result: PushTestDeviceResult) {
  switch (result.outcome) {
    case "sent":
      return `${result.deviceLabel}: sent.`;
    case "removed":
      return `${result.deviceLabel}: removed, it no longer accepts notifications (turn them on again there).`;
    case "failed":
      return `${result.deviceLabel}: failed${result.error ? ` (${result.error})` : ""}.`;
  }
}

// ---- Requests --------------------------------------------------------------
// Each resolves to null (or false) when Notifications could not be reached
// or refused the request.

export async function fetchPushConfig(): Promise<PushConfig | null> {
  try {
    const { data } = await notificationsClient.GET("/v1/push/config");
    return data ?? null;
  } catch {
    return null;
  }
}

export async function fetchPushDevices(): Promise<PushDevice[] | null> {
  try {
    const { data } = await notificationsClient.GET("/v1/push/subscriptions");
    return data?.items ?? null;
  } catch {
    return null;
  }
}

export async function saveSubscription(
  body: PushSubscriptionRequest,
): Promise<PushDevice | null> {
  try {
    const { data } = await notificationsClient.POST("/v1/push/subscriptions", {
      body,
    });
    return data ?? null;
  } catch {
    return null;
  }
}

export async function removeDevice(id: string): Promise<boolean> {
  try {
    const { response } = await notificationsClient.DELETE(
      "/v1/push/subscriptions/{subscriptionId}",
      { params: { path: { subscriptionId: id } } },
    );
    return response.ok || response.status === 404;
  } catch {
    return false;
  }
}

export async function sendPushTest(
  subscriptionId?: string,
): Promise<PushTestDeviceResult[] | null> {
  try {
    const { data } = await notificationsClient.POST("/v1/push/test", {
      body: subscriptionId ? { subscriptionId } : {},
    });
    return data?.results ?? null;
  } catch {
    return null;
  }
}

// ---- This browser (browser only) -------------------------------------------

/** This browser's push subscription, or null. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  const registration = await navigator.serviceWorker.getRegistration("/");
  return registration ? registration.pushManager.getSubscription() : null;
}

export type EnableResult =
  | { ok: true; device: PushDevice; subscription: PushSubscription }
  | { ok: false; reason: "denied" | "dismissed" | "failed"; message: string };

/**
 * Turns notifications on here: asks for permission (call it from the click,
 * which allows the prompt), subscribes with the server's key (again when
 * the key changed), and saves the subscription in Life OS.
 */
export async function enablePush(publicKey: string): Promise<EnableResult> {
  const permission = await Notification.requestPermission();
  if (permission === "denied") {
    return {
      ok: false,
      reason: "denied",
      message: "Notifications are blocked for Life OS on this device.",
    };
  }
  if (permission !== "granted") {
    return {
      ok: false,
      reason: "dismissed",
      message: "Allow notifications when the browser asks to turn them on.",
    };
  }
  let subscription: PushSubscription;
  try {
    await navigator.serviceWorker.register(SERVICE_WORKER_PATH, {
      scope: "/",
      updateViaCache: "none",
    });
    const registration = await navigator.serviceWorker.ready;
    let existing = await registration.pushManager.getSubscription();
    if (
      existing &&
      !sameKey(existing.options.applicationServerKey, publicKey)
    ) {
      await existing.unsubscribe();
      existing = null;
    }
    subscription =
      existing ??
      (await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: base64UrlToBytes(publicKey),
      }));
  } catch {
    return {
      ok: false,
      reason: "failed",
      message: "This browser couldn't subscribe to notifications. Try again.",
    };
  }
  const device = await saveSubscription(
    subscriptionRequest(subscription.toJSON()),
  );
  if (!device) {
    return {
      ok: false,
      reason: "failed",
      message: "Life OS couldn't save this device. Try again.",
    };
  }
  return { ok: true, device, subscription };
}

/**
 * Turns notifications off here: removes this device from Life OS, then
 * unsubscribes the browser. False when Life OS could not remove it (the
 * browser stays subscribed, so the state stays on).
 */
export async function disablePush(
  devices: readonly PushDevice[],
): Promise<boolean> {
  const subscription = await currentSubscription();
  if (!subscription) return true;
  const device = devices.find(
    (item) => item.endpoint === subscription.endpoint,
  );
  if (device && !(await removeDevice(device.id))) return false;
  await subscription.unsubscribe();
  return true;
}
