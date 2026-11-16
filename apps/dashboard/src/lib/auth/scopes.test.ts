import assert from "node:assert/strict";
import test from "node:test";

import { bearerToken, devName } from "./check";
import {
  mcpModuleScopes,
  normalizeScopes,
  routeFromGateway,
  routeOf,
  scopesAllow,
  suspiciousPath,
} from "./scopes";

test("scopes are checked and put in one canonical form", () => {
  assert.deepEqual(
    normalizeScopes(["food:read", "tasks:read", "food:write", " tasks:read "]),
    ["tasks:read", "food:write"],
  );
  assert.deepEqual(normalizeScopes([]), []);
  for (const bad of [
    ["admin:write"],
    ["tasks:delete"],
    ["tasks"],
    [1],
    "tasks:read",
    null,
    ["mcp:read"],
  ]) {
    assert.equal(normalizeScopes(bad), null, JSON.stringify(bad));
  }
});

test("write includes read; read is GET, HEAD, and OPTIONS only", () => {
  assert.equal(scopesAllow(["tasks:read"], "tasks", "GET"), true);
  assert.equal(scopesAllow(["tasks:read"], "tasks", "HEAD"), true);
  assert.equal(scopesAllow(["tasks:read"], "tasks", "POST"), false);
  assert.equal(scopesAllow(["tasks:read"], "tasks", "DELETE"), false);
  assert.equal(scopesAllow(["tasks:write"], "tasks", "GET"), true);
  assert.equal(scopesAllow(["tasks:write"], "tasks", "PATCH"), true);
  assert.equal(scopesAllow(["tasks:write"], "food", "GET"), false);
});

test("routes: modules, the integrations' modules, MCP, the downloads, the rest", () => {
  assert.deepEqual(routeOf("/api/tasks/v1/tasks?limit=1"), {
    kind: "module",
    module: "tasks",
  });
  assert.deepEqual(routeOf("/api/integrations/banking/v1/connections"), {
    kind: "module",
    module: "finance",
  });
  assert.deepEqual(routeOf("/api/integrations/google-calendar/v1/x"), {
    kind: "module",
    module: "calendar",
  });
  assert.deepEqual(routeOf("/api/integrations/recipe-import/v1/imports"), {
    kind: "module",
    module: "food",
  });
  assert.deepEqual(routeOf("/api/integrations/unknown/v1/x"), {
    kind: "other",
  });
  assert.deepEqual(routeOf("/api/mcp/mcp"), { kind: "mcp" });
  assert.deepEqual(routeOf("/android/updates"), { kind: "android" });
  assert.deepEqual(routeOf("/api/status"), { kind: "other" });
  assert.deepEqual(routeOf("/api/account/tokens"), { kind: "other" });
  // A path that only looks like a module's is not one.
  assert.deepEqual(routeOf("/api/tasksx/v1"), { kind: "other" });
  assert.deepEqual(routeOf("/api/tasks"), { kind: "other" });
});

test("the route the gateway names decides, the URI only without it", () => {
  assert.equal(routeFromGateway(null), null);
  assert.equal(routeFromGateway(""), null);
  assert.deepEqual(routeFromGateway("mcp"), { kind: "mcp" });
  assert.deepEqual(routeFromGateway("android"), { kind: "android" });
  assert.deepEqual(routeFromGateway("finance"), {
    kind: "module",
    module: "finance",
  });
  // Not a route a location names: the Dashboard's own, which no token
  // reaches.
  for (const odd of [
    "admin",
    "Finance",
    "finance ",
    "banking",
    "mcp,finance",
  ]) {
    assert.deepEqual(routeFromGateway(odd), { kind: "other" }, odd);
  }
});

test("paths nginx would resolve to another location are suspicious", () => {
  for (const uri of [
    "/api/finance/..%2Fmcp/mcp",
    "/api/finance/..%2fmcp/mcp",
    "/api/finance/%2e%2e/mcp/mcp",
    "/api/finance/.%2E/mcp",
    "/api/finance/../mcp/mcp",
    "/api/tasks/./v1/tasks",
    "/api/tasks/v1/..",
    "/api/tasks/v1/.",
    "/api/finance/v1%5Caccounts",
    "/api/finance/v1\\accounts",
    "/..",
  ]) {
    assert.equal(suspiciousPath(uri), true, uri);
  }
  for (const uri of [
    "/api/finance/v1/accounts",
    "/api/tasks/v1/tasks?q=a%2Fb&r=..",
    "/api/tasks/v1/tasks#../x",
    "/api/food/v1/recipes/my.recipe",
    "/api/food/v1/recipes/...",
    "/api/food/v1/recipes/.well",
    "/api/food/v1/recipes/a%2520b",
    "/",
  ]) {
    assert.equal(suspiciousPath(uri), false, uri);
  }
});

test("an MCP grant's scopes become module scopes", () => {
  const read = mcpModuleScopes(["mcp:read", "offline_access"]);
  assert.ok(read.includes("tasks:read"));
  assert.ok(read.every((scope) => scope.endsWith(":read")));
  const write = mcpModuleScopes(["mcp:read", "mcp:write"]);
  assert.ok(write.includes("finance:write"));
  assert.ok(write.every((scope) => scope.endsWith(":write")));
  assert.deepEqual(mcpModuleScopes(["offline_access"]), []);
});

test("only Bearer is a Life OS credential", () => {
  const h = (value?: string) =>
    new Headers(value === undefined ? {} : { authorization: value });
  assert.equal(bearerToken(h()), null);
  assert.equal(bearerToken(h("Basic dXNlcjpwYXNz")), null);
  assert.equal(bearerToken(h("Bearer lo_dev_abc")), "lo_dev_abc");
  assert.equal(bearerToken(h("bearer   lo_dev_abc ")), "lo_dev_abc");
  assert.equal(bearerToken(h("Bearer")), "");
});

test("a development tool's actor name", () => {
  assert.equal(devName("claude code"), "claude-code");
  assert.equal(devName(" Script #1 ✓ "), "Script-1");
  assert.equal(devName("✓✓"), "token");
  assert.equal(devName("x".repeat(80)).length, 64);
});
