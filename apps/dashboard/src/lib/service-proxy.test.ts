import assert from "node:assert/strict";
import test from "node:test";

import { NextRequest } from "next/server";

import { proxyServiceRequest } from "./service-proxy";

async function forwardedActor(actor?: string) {
  const original = globalThis.fetch;
  let seen: string | null | undefined;
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen = new Headers(init?.headers).get("x-life-os-actor");
    return Response.json({});
  }) as typeof fetch;
  try {
    const request = new NextRequest(
      "http://dashboard.test/api/tasks/v1/tasks",
      {
        method: "POST",
        headers: actor ? { "X-Life-OS-Actor": actor } : {},
        body: "{}",
      },
    );
    await proxyServiceRequest(
      request,
      { params: Promise.resolve({ path: ["v1", "tasks"] }) },
      "http://tasks.test",
    );
  } finally {
    globalThis.fetch = original;
  }
  return seen;
}

test("the service proxy passes on only owner and development-tool actors", async () => {
  assert.equal(await forwardedActor("user"), "user");
  assert.equal(await forwardedActor("dev:claude-code"), "dev:claude-code");
  assert.equal(await forwardedActor("agent:codex-inbox-processor"), null);
  assert.equal(await forwardedActor("system:voice-worker"), null);
  assert.equal(await forwardedActor(), null);
});
