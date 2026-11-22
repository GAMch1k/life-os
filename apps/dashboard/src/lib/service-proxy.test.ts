import assert from "node:assert/strict";
import test from "node:test";

import { NextRequest } from "next/server";

import {
  developmentProxy,
  proxyServiceRequest,
  serviceTarget,
} from "./service-proxy";

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

test("the service proxy passes on an Idempotency-Key", async () => {
  const original = globalThis.fetch;
  let seen: string | null = null;
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen = new Headers(init?.headers).get("idempotency-key");
    return Response.json({});
  }) as typeof fetch;
  try {
    await proxyServiceRequest(
      new NextRequest("http://dashboard.test/api/finance/v1/transactions", {
        method: "POST",
        headers: { "Idempotency-Key": "dashboard-abc" },
        body: "{}",
      }),
      { params: Promise.resolve({ path: ["v1", "transactions"] }) },
      "http://finance.test",
    );
  } finally {
    globalThis.fetch = original;
  }
  assert.equal(seen, "dashboard-abc");
});

test("the service proxy passes on the extra headers a route asks for", async () => {
  const original = globalThis.fetch;
  let seen: Headers | undefined;
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen = new Headers(init?.headers);
    return Response.json({});
  }) as typeof fetch;
  try {
    const request = new NextRequest(
      "http://dashboard.test/api/integrations/banking/v1/connections/c/sync",
      {
        method: "POST",
        headers: {
          "X-Life-OS-Actor": "user",
          "User-Agent": "Firefox",
          "X-Real-IP": "203.0.113.7",
          Cookie: "secret=1",
        },
      },
    );
    await proxyServiceRequest(
      request,
      { params: Promise.resolve({ path: ["v1", "connections", "c", "sync"] }) },
      "http://banking.test",
      { forwardHeaders: ["user-agent", "x-real-ip"] },
    );
  } finally {
    globalThis.fetch = original;
  }
  assert.equal(seen?.get("user-agent"), "Firefox");
  assert.equal(seen?.get("x-real-ip"), "203.0.113.7");
  assert.equal(seen?.get("cookie"), null);
});

type Seen = { url: string; init?: RequestInit };

/**
 * Runs the proxy with a stand-in fetch: what the proxy fetched (nothing,
 * when it refused), and its answer.
 */
async function proxied(
  segments: string[],
  {
    serviceUrl = "http://tasks.test",
    search = "",
    answer = () => Response.json({}),
  }: {
    serviceUrl?: string;
    search?: string;
    answer?: () => Response;
  } = {},
) {
  const original = globalThis.fetch;
  const seen: Seen[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    seen.push({ url: String(input), init });
    return answer();
  }) as typeof fetch;
  try {
    const response = await proxyServiceRequest(
      new NextRequest(`http://dashboard.test/api/tasks/x${search}`),
      { params: Promise.resolve({ path: segments }) },
      serviceUrl,
    );
    return { response, seen };
  } finally {
    globalThis.fetch = original;
  }
}

// The segments as Next.js hands them to a catch-all route: it decodes each
// one, %2F, %5C, %3A, and %2E included.
const REFUSED: string[][] = [
  [],
  [""],
  ["."],
  [".."],
  ["v1", ".."],
  ["v1", "", "tasks"],
  ["..", "..", "v1"],
  ["http://192.0.2.1/x.html?", "../../../../.."],
  ["http:", "", "192.0.2.1", "x.html?", "..", "..", "..", "..", ".."],
  ["http://192.0.2.1"],
  ["https:192.0.2.1"],
  ["//192.0.2.1/x"],
  ["/v1/tasks"],
  ["v1/tasks"],
  ["v1/../../x"],
  ["http:\\\\192.0.2.1\\x"],
  ["\\\\192.0.2.1"],
  ["v1\\tasks"],
  ["192.0.2.1:80"],
  ["javascript:alert(1)"],
  ["v1", "tasks\u0000"],
  ["v1", "ta\nsks"],
  ["v1", "ta\rsks"],
  ["v1", "\ttasks"],
  ["v1", "tasks\u007f"],
];

test("encoded separators and dot segments never change the target", async () => {
  for (const segments of REFUSED) {
    const { response, seen } = await proxied(segments);
    assert.equal(response.status, 400, JSON.stringify(segments));
    assert.equal(seen.length, 0, JSON.stringify(segments));
    const body = (await response.json()) as { error: { code: string } };
    assert.equal(body.error.code, "forbidden_path");
    assert.equal(response.headers.get("content-security-policy"), "sandbox");
  }
});

test("the target is the service, whatever a segment holds", () => {
  const shapes = [
    ...REFUSED.flat(),
    "%2e%2e",
    "%2E%2E%2F",
    "%2f%2f192.0.2.1",
    "..%2F..%2F192.0.2.1",
    "%5c%5c192.0.2.1",
    "@192.0.2.1",
    "?x=1",
    "#frag",
    "...",
    ".well-known",
    "a b",
    "Zwiebel & Käse",
    "100%",
  ];
  for (const serviceUrl of ["http://tasks.test", "http://svc.test:8080/base"]) {
    const base = new URL(`${serviceUrl}/`);
    for (const one of shapes) {
      for (const segments of [[one], ["v1", one], [one, "x"]]) {
        const target = serviceTarget(serviceUrl, segments, "?q=1");
        if (!target) continue;
        const what = JSON.stringify(segments);
        assert.equal(target.origin, base.origin, what);
        assert.ok(target.pathname.startsWith(base.pathname), what);
        assert.equal(target.search, "?q=1", what);
        assert.equal(target.hash, "", what);
        // Exactly the segments: none splits in two, none goes up.
        assert.equal(
          target.pathname.slice(base.pathname.length).split("/").length,
          segments.length,
          what,
        );
      }
    }
  }
});

test("plain segments reach the service, encoded, with the query", async () => {
  const { response, seen } = await proxied(["v1", "tasks"], {
    search: "?completed=false&q=a%2Fb",
  });
  assert.equal(response.status, 200);
  assert.equal(seen.length, 1);
  assert.equal(
    seen[0].url,
    "http://tasks.test/v1/tasks?completed=false&q=a%2Fb",
  );
  assert.equal(seen[0].init?.redirect, "manual");

  const based = await proxied(["v1", "%2e%2e", "a b"], {
    serviceUrl: "http://svc.test:8080/base",
  });
  assert.equal(
    based.seen[0].url,
    "http://svc.test:8080/base/v1/%252e%252e/a%20b",
  );
  assert.equal(
    serviceTarget("http://tasks.test", ["v1", "@192.0.2.1"], "")?.href,
    "http://tasks.test/v1/%40192.0.2.1",
  );
});

test("a redirect is not followed, and another origin's Location not passed on", async () => {
  const redirect = (location: string) => () =>
    new Response(null, { status: 302, headers: { location } });
  for (const location of [
    "http://192.0.2.1/x.html",
    "//192.0.2.1/x",
    "https://tasks.test/x",
    "http://tasks.test:8080/x",
  ]) {
    const { response, seen } = await proxied(["v1", "x"], {
      answer: redirect(location),
    });
    assert.equal(seen.length, 1, location);
    assert.equal(seen[0].init?.redirect, "manual", location);
    assert.equal(response.status, 302, location);
    assert.equal(response.headers.get("location"), null, location);
  }
  const home = await proxied(["v1", "x"], { answer: redirect("/v1/y") });
  assert.equal(home.response.headers.get("location"), "/v1/y");
});

test("every answer is sandboxed, and only JSON and media show inline", async () => {
  const answer = (type: string | null) => () =>
    new Response("<p>x</p>", {
      status: 200,
      headers: type ? { "content-type": type } : {},
    });
  for (const [type, inline] of [
    ["application/json", true],
    ["application/json; charset=utf-8", true],
    ["application/problem+json", true],
    ["image/jpeg", true],
    ["image/png", true],
    ["image/webp", true],
    ["text/html; charset=utf-8", false],
    ["TEXT/HTML", false],
    ["image/svg+xml", false],
    ["application/xhtml+xml", false],
    ["text/plain", false],
    ["application/octet-stream", false],
    [null, false],
  ] as const) {
    const { response } = await proxied(["v1", "x"], { answer: answer(type) });
    assert.equal(response.headers.get("content-security-policy"), "sandbox");
    assert.equal(response.headers.get("x-content-type-options"), "nosniff");
    assert.equal(
      response.headers.get("content-disposition"),
      inline ? null : "attachment",
      String(type),
    );
  }
  const empty = await proxied(["v1", "x"], {
    answer: () => new Response(null, { status: 204 }),
  });
  assert.equal(empty.response.status, 204);
  assert.equal(
    empty.response.headers.get("content-security-policy"),
    "sandbox",
  );
});

function setNodeEnv(value: string | undefined) {
  const env = process.env as Record<string, string | undefined>;
  if (value === undefined) delete env.NODE_ENV;
  else env.NODE_ENV = value;
}

test("a gateway-routed service's proxy answers only in development", async () => {
  const original = globalThis.fetch;
  const environment = process.env.NODE_ENV;
  let calls = 0;
  globalThis.fetch = (async () => {
    calls += 1;
    return Response.json({});
  }) as typeof fetch;
  const call = (path: string[]) =>
    developmentProxy(
      new NextRequest("http://dashboard.test/api/tasks/v1/tasks"),
      { params: Promise.resolve({ path }) },
      "http://tasks.test",
    );
  try {
    for (const value of ["production", "test", undefined]) {
      setNodeEnv(value);
      const response = await call(["v1", "tasks"]);
      assert.equal(response.status, 404, String(value));
    }
    assert.equal(calls, 0);
    setNodeEnv("development");
    assert.equal((await call(["v1", "tasks"])).status, 200);
    assert.equal(calls, 1);
    assert.equal((await call(["http://192.0.2.1/", ".."])).status, 400);
    assert.equal(calls, 1);
  } finally {
    globalThis.fetch = original;
    setNodeEnv(environment);
  }
});
