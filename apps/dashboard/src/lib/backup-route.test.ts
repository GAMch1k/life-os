import assert from "node:assert/strict";
import test from "node:test";

import { NextRequest } from "next/server";

import { GET, POST } from "@/app/api/backups/[...path]/route";

/** Calls the Dashboard's backup route and returns what reached the service. */
async function call(method: "GET" | "POST", path: string[]) {
  const original = globalThis.fetch;
  const seen: { url: string; actor: string | null }[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    seen.push({
      url: String(input),
      actor: new Headers(init?.headers).get("x-life-os-actor"),
    });
    return Response.json({ id: "20261011T120000Z" }, { status: 202 });
  }) as typeof fetch;
  try {
    const handler = method === "GET" ? GET : POST;
    const response = await handler(
      new NextRequest(`http://dashboard.test/api/backups/${path.join("/")}`, {
        method,
        headers: { "X-Life-OS-Actor": "user" },
      }),
      { params: Promise.resolve({ path }) },
    );
    return { status: response.status, seen };
  } finally {
    globalThis.fetch = original;
  }
}

test("the backup route passes on reading the status and starting a backup", async () => {
  const status = await call("GET", ["v1", "status"]);
  assert.equal(status.seen.length, 1);
  assert.match(status.seen[0].url, /\/v1\/status$/);
  const started = await call("POST", ["v1", "backups"]);
  assert.equal(started.status, 202);
  assert.match(started.seen[0].url, /\/v1\/backups$/);
  assert.equal(started.seen[0].actor, "user");
});

test("anything else stays on the private network", async () => {
  for (const [method, path] of [
    ["POST", ["v1", "restore-checks"]],
    ["GET", ["v1", "backups"]],
    ["POST", ["v1", "status"]],
    ["GET", ["health"]],
  ] as const) {
    const result = await call(method, [...path]);
    assert.equal(result.status, 404, `${method} ${path.join("/")}`);
    assert.equal(result.seen.length, 0, `${method} ${path.join("/")}`);
  }
});
