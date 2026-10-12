import assert from "node:assert/strict";
import test from "node:test";

import {
  base64UrlToBytes,
  bytesToBase64Url,
  deviceStatus,
  pushState,
  pushStateView,
  pushSupport,
  sameKey,
  subscriptionRequest,
  testResultText,
  type PushConfig,
  type PushDevice,
  type PushEnvironment,
  type PushStateInput,
} from "./web-push";

const BERLIN = "Europe/Berlin";
const now = new Date("2026-10-12T12:00:00Z"); // 14:00 CEST

// The VAPID public key of RFC 8291's example: an uncompressed P-256 point.
const PUBLIC_KEY =
  "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8";

test("a base64url VAPID key becomes the bytes the browser subscribes with", () => {
  const bytes = base64UrlToBytes(PUBLIC_KEY);
  assert.equal(bytes.length, 65);
  assert.equal(bytes[0], 0x04); // uncompressed point
  assert.equal(bytes[1], 0xfe);
  assert.equal(bytes[64], 0x0f);
  assert.equal(bytesToBase64Url(bytes), PUBLIC_KEY);
  assert.equal(bytesToBase64Url(bytes.buffer), PUBLIC_KEY);
  // Padding, the standard alphabet, and surrounding space are accepted.
  assert.deepEqual(
    base64UrlToBytes(
      " " + PUBLIC_KEY.replace(/-/g, "+").replace(/_/g, "/") + "= ",
    ),
    bytes,
  );
  assert.deepEqual([...base64UrlToBytes("BTBZMqHH6r4Tts7J_aSIgg")].length, 16);
  assert.deepEqual([...base64UrlToBytes("")], []);
  assert.throws(() => base64UrlToBytes("not base64!"));
});

test("a subscription made with another key is not the server's", () => {
  const key = base64UrlToBytes(PUBLIC_KEY).buffer;
  assert.equal(sameKey(key, PUBLIC_KEY), true);
  assert.equal(sameKey(key, PUBLIC_KEY.slice(0, -2) + "AA"), false);
  assert.equal(sameKey(null, PUBLIC_KEY), false);
  assert.equal(sameKey(key, "not base64!"), false);
});

test("the browser's subscription JSON becomes the API request", () => {
  assert.deepEqual(
    subscriptionRequest({
      endpoint: "https://fcm.googleapis.com/fcm/send/abc",
      expirationTime: null,
      keys: { p256dh: "BCVx", auth: "BTBZ" },
    }),
    {
      endpoint: "https://fcm.googleapis.com/fcm/send/abc",
      keys: { p256dh: "BCVx", auth: "BTBZ" },
    },
  );
  assert.deepEqual(subscriptionRequest({}), {
    endpoint: "",
    keys: { p256dh: "", auth: "" },
  });
});

const android: PushEnvironment = {
  secure: true,
  serviceWorker: true,
  pushManager: true,
  notification: true,
  ios: false,
  standalone: true,
};

test("push needs https, a service worker, the Push API, and notifications", () => {
  assert.equal(pushSupport(android), "supported");
  assert.equal(pushSupport({ ...android, standalone: false }), "supported");
  // The WireGuard address is plain http.
  assert.equal(pushSupport({ ...android, secure: false }), "insecure");
  assert.equal(
    pushSupport({
      ...android,
      pushManager: false,
      notification: false,
      ios: true,
      standalone: false,
    }),
    "ios-browser",
  );
  assert.equal(
    pushSupport({
      ...android,
      pushManager: false,
      ios: true,
      standalone: true,
    }),
    "unsupported",
  );
  assert.equal(
    pushSupport({ ...android, serviceWorker: false }),
    "unsupported",
  );
});

const config: PushConfig = {
  configured: true,
  publicKey: PUBLIC_KEY,
  delivering: true,
};
const ENDPOINT = "https://fcm.googleapis.com/fcm/send/this-phone";

function device(overrides: Partial<PushDevice> = {}): PushDevice {
  return {
    id: "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10",
    deviceLabel: "Chrome on Android",
    endpoint: ENDPOINT,
    pushService: "fcm.googleapis.com",
    createdAt: "2026-10-12T08:00:00Z",
    lastSuccessAt: null,
    lastFailureAt: null,
    lastError: null,
    ...overrides,
  };
}

const ready: PushStateInput = {
  support: "supported",
  config,
  permission: "granted",
  endpoint: ENDPOINT,
  devices: [device()],
};

test("the setting is on only when the browser and the server both have the subscription", () => {
  assert.equal(pushState(ready), "on");
  // The server pruned it, or it was removed in the list: off, and turning
  // it on sends it again.
  assert.equal(pushState({ ...ready, devices: [] }), "off");
  assert.equal(pushState({ ...ready, devices: null }), "off");
  assert.equal(pushState({ ...ready, endpoint: null }), "off");
  // Permission withdrawn in Android's settings.
  assert.equal(pushState({ ...ready, permission: "default" }), "off");
  assert.equal(pushState({ ...ready, permission: "denied" }), "denied");
});

test("the setting says why it cannot be turned on", () => {
  assert.equal(pushState({ ...ready, support: undefined }), "loading");
  assert.equal(pushState({ ...ready, config: undefined }), "loading");
  assert.equal(pushState({ ...ready, endpoint: undefined }), "loading");
  assert.equal(pushState({ ...ready, support: "insecure" }), "unsupported");
  assert.equal(pushState({ ...ready, support: "ios-browser" }), "unsupported");
  assert.equal(pushState({ ...ready, config: null }), "unavailable");
  assert.equal(
    pushState({
      ...ready,
      config: { configured: false, publicKey: null, delivering: false },
    }),
    "unconfigured",
  );
  // Unconfigured wins over a denied permission: there is nothing to allow.
  assert.equal(
    pushState({
      ...ready,
      permission: "denied",
      config: { configured: false, publicKey: null, delivering: false },
    }),
    "unconfigured",
  );
});

test("each state offers what can be done", () => {
  assert.equal(pushStateView("off", "supported", config).action, "enable");
  assert.equal(pushStateView("on", "supported", config).action, "disable");
  assert.equal(pushStateView("on", "supported", config).hint, undefined);
  for (const state of [
    "loading",
    "unavailable",
    "unsupported",
    "unconfigured",
    "denied",
  ] as const) {
    assert.equal(
      pushStateView(state, "supported", config).action,
      undefined,
      state,
    );
  }
  const denied = pushStateView("denied", "supported", config);
  assert.match(denied.summary, /blocked/);
  assert.match(denied.hint ?? "", /App info → Notifications/);
  assert.match(
    pushStateView("unsupported", "insecure", config).hint ?? "",
    /https/,
  );
  assert.match(
    pushStateView("unsupported", "ios-browser", config).hint ?? "",
    /Add to Home Screen/,
  );
  assert.match(
    pushStateView("unconfigured", "supported", config).hint ?? "",
    /LIFE_OS_WEBPUSH_/,
  );
  // Keys set, but webpush is not a delivery channel: only tests arrive.
  const notDelivering = { ...config, delivering: false };
  assert.match(
    pushStateView("on", "supported", notDelivering).hint ?? "",
    /LIFE_OS_NOTIFICATIONS_CHANNELS/,
  );
  assert.match(
    pushStateView("off", "supported", notDelivering).hint ?? "",
    /only test notifications/,
  );
});

test("a device shows its latest push", () => {
  assert.deepEqual(deviceStatus(device(), BERLIN, now), {
    text: "Nothing pushed yet",
    tone: "muted",
  });
  assert.deepEqual(
    deviceStatus(
      device({ lastSuccessAt: "2026-10-12T11:05:00Z" }),
      BERLIN,
      now,
    ),
    { text: "Last push 13:05", tone: "success" },
  );
  assert.deepEqual(
    deviceStatus(
      device({
        lastSuccessAt: "2026-10-12T09:00:00Z",
        lastFailureAt: "2026-10-12T11:10:00Z",
        lastError: "push service answered 503 Service Unavailable",
      }),
      BERLIN,
      now,
    ),
    {
      text: "Last push failed 13:10",
      tone: "warning",
      error: "push service answered 503 Service Unavailable",
    },
  );
  // A success after the failure: fine again.
  assert.equal(
    deviceStatus(
      device({
        lastSuccessAt: "2026-10-12T11:20:00Z",
        lastFailureAt: "2026-10-12T11:10:00Z",
        lastError: "push service answered 503 Service Unavailable",
      }),
      BERLIN,
      now,
    ).tone,
    "success",
  );
});

test("a test push's outcome reads as a sentence per device", () => {
  const base = {
    subscriptionId: "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10",
    deviceLabel: "Chrome on Android",
  };
  assert.equal(
    testResultText({ ...base, outcome: "sent", error: null }),
    "Chrome on Android: sent.",
  );
  assert.match(
    testResultText({
      ...base,
      outcome: "removed",
      error: "push service answered 410 Gone",
    }),
    /^Chrome on Android: removed/,
  );
  assert.equal(
    testResultText({
      ...base,
      outcome: "failed",
      error: "push service answered 403 Forbidden",
    }),
    "Chrome on Android: failed (push service answered 403 Forbidden).",
  );
});
