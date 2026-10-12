import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import vm from "node:vm";

// public/sw.js runs here in a sandbox standing in for the service worker's
// global scope: its handlers get fake push and click events, and what they
// do to the registration, the windows, and the network is recorded.

const SOURCE = readFileSync(join(process.cwd(), "public", "sw.js"), "utf8");
const ORIGIN = "https://lifeos.example.com";

type Listener = (event: Record<string, unknown>) => void;

type FakeWindow = {
  url: string;
  focused: boolean;
  navigatedTo?: string;
  focus: () => Promise<FakeWindow>;
  navigate: (url: string) => Promise<FakeWindow | null>;
};

function fakeWindow(url: string, navigate?: FakeWindow["navigate"]) {
  const client: FakeWindow = {
    url,
    focused: false,
    async focus() {
      client.focused = true;
      return client;
    },
    navigate:
      navigate ??
      (async (target) => {
        client.navigatedTo = target;
        return client;
      }),
  };
  return client;
}

function worker(windows: FakeWindow[] = []) {
  const listeners = new Map<string, Listener>();
  const shown: { title: string; options: Record<string, unknown> }[] = [];
  const opened: string[] = [];
  const fetched: { url: string; init: RequestInit }[] = [];
  const subscribed: unknown[] = [];
  let skipped = false;
  const self = {
    location: { origin: ORIGIN },
    addEventListener(type: string, listener: Listener) {
      listeners.set(type, listener);
    },
    skipWaiting() {
      skipped = true;
      return Promise.resolve();
    },
    clients: {
      claim: () => Promise.resolve(),
      matchAll: async (options: { type: string }) => {
        assert.equal(options.type, "window");
        return windows;
      },
      openWindow: async (url: string) => {
        opened.push(url);
        return null;
      },
    },
    registration: {
      showNotification: async (
        title: string,
        options: Record<string, unknown>,
      ) => {
        shown.push({ title, options });
      },
      pushManager: {
        subscribe: async (options: unknown) => {
          subscribed.push(options);
          return {
            toJSON: () => ({
              endpoint: "https://fcm.googleapis.com/fcm/send/new",
              keys: { p256dh: "p", auth: "a" },
            }),
          };
        },
      },
    },
  };
  const context = vm.createContext({
    self,
    URL,
    fetch: async (url: string, init: RequestInit) => {
      fetched.push({ url, init });
      return new Response(null, { status: 201 });
    },
  });
  vm.runInContext(SOURCE, context, { filename: "sw.js" });

  async function dispatch(type: string, event: Record<string, unknown>) {
    const listener = listeners.get(type);
    assert.ok(listener, `no ${type} listener`);
    let waited: Promise<unknown> = Promise.resolve();
    listener({
      ...event,
      waitUntil: (promise: Promise<unknown>) => (waited = promise),
    });
    await waited;
  }
  return {
    listeners,
    shown,
    opened,
    fetched,
    subscribed,
    dispatch,
    skipped: () => skipped,
  };
}

const pushData = (payload: unknown) => ({
  json: () => (typeof payload === "string" ? JSON.parse(payload) : payload),
});

function click(link: unknown) {
  let closed = false;
  return {
    event: { notification: { data: { link }, close: () => (closed = true) } },
    closed: () => closed,
  };
}

test("the worker still installs, takes control, and caches nothing", async () => {
  const sw = worker();
  await sw.dispatch("install", {});
  await sw.dispatch("activate", {});
  assert.equal(sw.skipped(), true);
  // No fetch handler: every request goes to the network as without it.
  assert.equal(sw.listeners.has("fetch"), false);
  assert.deepEqual([...sw.listeners.keys()].sort(), [
    "activate",
    "install",
    "notificationclick",
    "push",
    "pushsubscriptionchange",
  ]);
});

test("a push shows the notification Life OS sent", async () => {
  const sw = worker();
  await sw.dispatch("push", {
    data: pushData({
      id: "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10",
      title: "Dentist in 30 minutes",
      body: "Dr. Weber, Darmstadt",
      link: "/calendar?date=2026-10-12",
      tag: "lifeos-7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10",
      importance: "medium",
      category: "calendar.reminder",
      timestamp: 1791797400000,
    }),
  });
  assert.equal(sw.shown.length, 1);
  const [{ title, options }] = sw.shown;
  assert.equal(title, "Dentist in 30 minutes");
  assert.deepEqual(JSON.parse(JSON.stringify(options)), {
    body: "Dr. Weber, Darmstadt",
    icon: "/icons/icon-192.png",
    badge: "/icons/badge-96.png",
    data: { link: "/calendar?date=2026-10-12" },
    lang: "en",
    silent: false,
    tag: "lifeos-7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10",
    timestamp: 1791797400000,
  });
});

test("a low push is silent, and only Life OS paths are kept as links", async () => {
  const sw = worker();
  for (const link of [
    "https://evil.example/x",
    "//evil.example/x",
    "/\\evil",
    42,
    undefined,
  ]) {
    await sw.dispatch("push", {
      data: pushData({ title: "Budget 80%", importance: "low", link }),
    });
  }
  for (const { options } of sw.shown) {
    assert.equal(options.silent, true);
    assert.deepEqual(JSON.parse(JSON.stringify(options.data)), {
      link: "/notifications",
    });
  }
});

test("an unreadable push still shows a notification", async () => {
  const sw = worker();
  await sw.dispatch("push", {
    data: {
      json: () => {
        throw new SyntaxError("not JSON");
      },
    },
  });
  await sw.dispatch("push", { data: null });
  await sw.dispatch("push", { data: pushData({ title: "   " }) });
  assert.deepEqual(
    sw.shown.map((entry) => entry.title),
    ["Life OS", "Life OS", "Life OS"],
  );
});

test("a tap focuses an open Life OS window and opens the link there", async () => {
  const open = fakeWindow(`${ORIGIN}/tasks`);
  const sw = worker([open]);
  const tap = click("/calendar?date=2026-10-12");
  await sw.dispatch("notificationclick", tap.event);
  assert.equal(tap.closed(), true);
  assert.equal(open.focused, true);
  assert.equal(open.navigatedTo, `${ORIGIN}/calendar?date=2026-10-12`);
  assert.deepEqual(sw.opened, []);
});

test("a tap opens Life OS when no window is open, or the open one cannot go there", async () => {
  const sw = worker();
  await sw.dispatch("notificationclick", click("/inbox#human-review").event);
  await sw.dispatch("notificationclick", click("https://evil.example/").event);
  assert.deepEqual(sw.opened, [
    `${ORIGIN}/inbox#human-review`,
    `${ORIGIN}/notifications`,
  ]);

  const stuck = fakeWindow(`${ORIGIN}/`, async () => {
    throw new TypeError("not controlled");
  });
  const other = worker([stuck]);
  await other.dispatch("notificationclick", click("/tasks").event);
  assert.deepEqual(other.opened, [`${ORIGIN}/tasks`]);
});

test("a replaced subscription is subscribed again and saved in Life OS", async () => {
  const sw = worker();
  const key = new Uint8Array([4, 1, 2]).buffer;
  await sw.dispatch("pushsubscriptionchange", {
    oldSubscription: { options: { applicationServerKey: key } },
  });
  assert.equal(sw.subscribed.length, 1);
  // Made in the sandbox's realm: compared field by field.
  const options = sw.subscribed[0] as PushSubscriptionOptionsInit;
  assert.equal(options.userVisibleOnly, true);
  assert.equal(options.applicationServerKey, key);
  assert.equal(sw.fetched.length, 1);
  const [{ url, init }] = sw.fetched;
  assert.equal(url, "/api/notifications/v1/push/subscriptions");
  assert.equal(init.method, "POST");
  assert.equal(
    (init.headers as Record<string, string>)["X-Life-OS-Actor"],
    "user",
  );
  assert.deepEqual(JSON.parse(String(init.body)), {
    endpoint: "https://fcm.googleapis.com/fcm/send/new",
    keys: { p256dh: "p", auth: "a" },
  });
});
