import assert from "node:assert/strict";
import test from "node:test";

import {
  createActivityClient,
  createBankingClient,
  createCalendarClient,
  createCalendarFeedsClient,
  createFinanceClient,
  createInboxClient,
  createNotificationsClient,
  createTasksClient,
  createVoiceClient,
} from "./index";
import { ACTOR_HEADER, PUBLIC_ACTOR } from "./actor";

test("each generated service client accepts an explicit base URL", () => {
  const url = "http://example.test";

  assert.ok(createFinanceClient(url));
  assert.ok(createTasksClient(url));
  assert.ok(createInboxClient(url));
  assert.ok(createCalendarClient(url));
  assert.ok(createCalendarFeedsClient(url));
  assert.ok(createVoiceClient(url));
  assert.ok(createActivityClient(url));
  assert.ok(createNotificationsClient(url));
  assert.ok(createBankingClient(url));
});

test("service clients act for the owner on every request", async () => {
  const original = globalThis.fetch;
  const actors: (string | null)[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    actors.push(new Request(input, init).headers.get(ACTOR_HEADER));
    return Response.json({});
  }) as typeof fetch;
  try {
    const url = "http://example.test";
    await createTasksClient(url).POST("/v1/tasks", { body: { title: "x" } });
    await createInboxClient(url).POST("/v1/inbox-items", {
      body: { content: "x", source: "web" },
    });
    await createCalendarClient(url).DELETE("/v1/events/{eventId}", {
      params: { path: { eventId: "e" } },
    });
    await createFinanceClient(url).GET("/v1/accounts");
    await createVoiceClient(url).POST("/v1/audio-jobs", {
      body: { audio: "x" },
      bodySerializer: () => new FormData(),
    });
    await createActivityClient(url).GET("/v1/activity-events");
    await createNotificationsClient(url).POST(
      "/v1/notifications/{notificationId}/read",
      { params: { path: { notificationId: "n" } } },
    );
    await createBankingClient(url).POST("/v1/connections/{connectionId}/sync", {
      params: { path: { connectionId: "c" } },
    });
    await createCalendarFeedsClient(url).POST("/v1/feeds/{feedId}/refresh", {
      params: { path: { feedId: "f" } },
    });
  } finally {
    globalThis.fetch = original;
  }
  assert.deepEqual(actors, Array(9).fill("user"));
});

test("only owner and development-tool actors count as public", () => {
  for (const actor of ["user", "dev:claude-code", "dev:e2e"]) {
    assert.ok(PUBLIC_ACTOR.test(actor), actor);
  }
  for (const actor of [
    "",
    "agent:codex-inbox-processor",
    "system:voice-worker",
    "dev:",
    "dev:bad name",
    "user:x",
  ]) {
    assert.ok(!PUBLIC_ACTOR.test(actor), actor);
  }
});
