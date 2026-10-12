import { expect, type Page, type Route, test } from "./fixtures";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

// "Notifications on this device" on /notifications. A real push cannot be
// tested here: Playwright's Chromium has no push service, and the
// verification stack has no VAPID keys. So the page's Push API and
// permission prompt are stubbed (addInitScript), and so are Notifications'
// /v1/push endpoints (page.route), which keep the devices in memory. The
// service worker's push and click handling is covered by
// src/lib/service-worker.test.ts, the delivery by the Go tests against a
// fake push service.

// RFC 8291's example key: what the stub expects the page to subscribe with.
const PUBLIC_KEY =
  "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8";
const ENDPOINT = "https://fcm.googleapis.com/fcm/send/e2e-this-device";

type Device = {
  id: string;
  deviceLabel: string;
  endpoint: string;
  pushService: string;
  createdAt: string;
  lastSuccessAt: string | null;
  lastFailureAt: string | null;
  lastError: string | null;
};

type Server = {
  configured: boolean;
  devices: Device[];
  subscribed: unknown[];
  deleted: string[];
  tests: number;
  actors: Set<string>;
};

function device(overrides: Partial<Device>): Device {
  return {
    id: crypto.randomUUID(),
    deviceLabel: "Chrome on Android",
    endpoint: ENDPOINT,
    pushService: "fcm.googleapis.com",
    createdAt: new Date().toISOString(),
    lastSuccessAt: null,
    lastFailureAt: null,
    lastError: null,
    ...overrides,
  };
}

/** Notifications' web push API, in memory. */
async function stubServer(page: Page, server: Server) {
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  await page.route("**/api/notifications/v1/push/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(
      "/api/notifications",
      "",
    );
    const method = request.method();
    if (method !== "GET") {
      server.actors.add(request.headers()["x-life-os-actor"] ?? "");
    }
    if (method === "GET" && path === "/v1/push/config") {
      return json(route, {
        configured: server.configured,
        publicKey: server.configured ? PUBLIC_KEY : null,
        delivering: server.configured,
      });
    }
    if (method === "GET" && path === "/v1/push/subscriptions") {
      return json(route, { items: server.devices });
    }
    if (method === "POST" && path === "/v1/push/subscriptions") {
      const body = request.postDataJSON() as { endpoint: string };
      server.subscribed.push(body);
      const created = device({ endpoint: body.endpoint });
      server.devices.push(created);
      return json(route, created, 201);
    }
    const removed = path.match(/^\/v1\/push\/subscriptions\/(.+)$/);
    if (method === "DELETE" && removed) {
      server.deleted.push(removed[1]);
      server.devices = server.devices.filter((item) => item.id !== removed[1]);
      return route.fulfill({ status: 204 });
    }
    if (method === "POST" && path === "/v1/push/test") {
      server.tests++;
      const at = new Date().toISOString();
      server.devices = server.devices.map((item) => ({
        ...item,
        lastSuccessAt: at,
      }));
      return json(route, {
        results: server.devices.map((item) => ({
          subscriptionId: item.id,
          deviceLabel: item.deviceLabel,
          outcome: "sent",
          error: null,
        })),
      });
    }
    return json(
      route,
      { error: { code: "not_found", message: "not stubbed" } },
      404,
    );
  });
}

/**
 * The browser's side: the permission prompt answers `answer`, and the Push
 * API subscribes without a push service. window.__push records what the
 * page asked for.
 */
async function stubBrowser(
  page: Page,
  permission: {
    initial: NotificationPermission;
    answer: NotificationPermission;
  },
) {
  await page.addInitScript(
    ({ permission, endpoint }) => {
      const state = {
        permission: permission.initial,
        subscription: null as PushSubscription | null,
        keys: [] as number[][],
        unsubscribed: 0,
      };
      (window as unknown as { __push: typeof state }).__push = state;
      Object.defineProperty(Notification, "permission", {
        configurable: true,
        get: () => state.permission,
      });
      Notification.requestPermission = async () => {
        state.permission = permission.answer;
        return state.permission;
      };
      PushManager.prototype.getSubscription = async () => state.subscription;
      PushManager.prototype.subscribe = async (options) => {
        const key = options?.applicationServerKey as Uint8Array;
        state.keys.push([...new Uint8Array(key)]);
        const subscription = {
          endpoint,
          expirationTime: null,
          options: {
            applicationServerKey: new Uint8Array(key).buffer,
            userVisibleOnly: true,
          },
          toJSON: () => ({
            endpoint,
            expirationTime: null,
            keys: {
              p256dh:
                "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
              auth: "BTBZMqHH6r4Tts7J_aSIgg",
            },
          }),
          unsubscribe: async () => {
            state.unsubscribed++;
            state.subscription = null;
            return true;
          },
        } as unknown as PushSubscription;
        state.subscription = subscription;
        return subscription;
      };
    },
    { permission, endpoint: ENDPOINT },
  );
}

function newServer(overrides: Partial<Server> = {}): Server {
  return {
    configured: true,
    devices: [],
    subscribed: [],
    deleted: [],
    tests: 0,
    actors: new Set(),
    ...overrides,
  };
}

test.describe("Notifications on this device", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("turns web push on, lists the device, sends a test, and turns it off", async ({
    page,
  }) => {
    const server = newServer();
    await stubServer(page, server);
    await stubBrowser(page, { initial: "default", answer: "granted" });
    await page.goto("/notifications");

    const card = page.getByRole("region", {
      name: "Notifications on this device",
    });
    await expect(card).toContainText(
      "Works even where ntfy is blocked, as on some work networks.",
    );
    await expect(card.locator("#push-state")).toHaveText("Off on this device.");
    await expect(card).toContainText("No device has notifications on yet.");

    await card.getByRole("button", { name: "Turn on" }).click();
    await expect(card.locator("#push-state")).toHaveText(
      "On: this device gets Life OS notifications.",
    );
    // Subscribed with the server's key, as bytes, and saved as the owner.
    const keys = await page.evaluate(
      () => (window as unknown as { __push: { keys: number[][] } }).__push.keys,
    );
    expect(keys).toHaveLength(1);
    expect(keys[0]).toHaveLength(65);
    expect(keys[0][0]).toBe(4);
    expect(server.subscribed).toEqual([
      {
        endpoint: ENDPOINT,
        keys: {
          p256dh:
            "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
          auth: "BTBZMqHH6r4Tts7J_aSIgg",
        },
      },
    ]);
    expect([...server.actors]).toEqual(["user"]);

    const list = card.getByRole("list", {
      name: "Devices with notifications on",
    });
    await expect(list.getByRole("listitem")).toHaveCount(1);
    await expect(list).toContainText("Chrome on Android");
    await expect(list).toContainText("This device");
    await expect(list).toContainText("Nothing pushed yet");

    await card.getByRole("button", { name: "Send test" }).click();
    await expect(
      card.getByRole("list", { name: "Test results" }),
    ).toContainText("Chrome on Android: sent.");
    await expect(list).toContainText("Last push");
    expect(server.tests).toBe(1);

    await card.getByRole("button", { name: "Turn off" }).click();
    await expect(card.locator("#push-state")).toHaveText("Off on this device.");
    expect(server.deleted).toHaveLength(1);
    expect(
      await page.evaluate(
        () =>
          (window as unknown as { __push: { unsubscribed: number } }).__push
            .unsubscribed,
      ),
    ).toBe(1);
    await expect(card).toContainText("No device has notifications on yet.");
  });

  test("removes another device from the list", async ({ page }) => {
    const other = device({
      deviceLabel: "Firefox on Windows",
      endpoint: "https://updates.push.services.mozilla.com/wpush/v2/e2e",
      pushService: "updates.push.services.mozilla.com",
      lastFailureAt: new Date().toISOString(),
      lastError: "push service answered 403 Forbidden",
    });
    const server = newServer({ devices: [other] });
    await stubServer(page, server);
    await stubBrowser(page, { initial: "default", answer: "granted" });
    await page.goto("/notifications");

    const card = page.getByRole("region", {
      name: "Notifications on this device",
    });
    const list = card.getByRole("list", {
      name: "Devices with notifications on",
    });
    await expect(list).toContainText("Firefox on Windows");
    await expect(list).toContainText("push service answered 403 Forbidden");
    await expect(list).not.toContainText("This device");
    await card
      .getByRole("button", { name: "Remove Firefox on Windows" })
      .click();
    await expect(card).toContainText("No device has notifications on yet.");
    expect(server.deleted).toEqual([other.id]);
  });

  test("explains how to allow notifications after they were blocked", async ({
    page,
  }) => {
    await stubServer(page, newServer());
    await stubBrowser(page, { initial: "denied", answer: "denied" });
    await page.goto("/notifications");

    const card = page.getByRole("region", {
      name: "Notifications on this device",
    });
    await expect(card.locator("#push-state")).toHaveText(
      "Notifications are blocked for Life OS on this device.",
    );
    await expect(card).toContainText("App info → Notifications");
    await expect(card.getByRole("button", { name: "Turn on" })).toHaveCount(0);
  });

  test("says when the server has no web push keys", async ({ page }) => {
    await stubServer(page, newServer({ configured: false }));
    await stubBrowser(page, { initial: "default", answer: "granted" });
    await page.goto("/notifications");

    const card = page.getByRole("region", {
      name: "Notifications on this device",
    });
    await expect(card.locator("#push-state")).toHaveText(
      "Web push is not set up on the server yet.",
    );
    await expect(card).toContainText("LIFE_OS_WEBPUSH_");
    await expect(card.getByRole("button", { name: "Turn on" })).toHaveCount(0);
  });
});
