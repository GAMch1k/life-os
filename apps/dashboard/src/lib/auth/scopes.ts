import { SCOPE_MODULES, type ScopeModule } from "./constants";

/**
 * Scopes of personal API tokens and MCP grants
 * (docs/architecture/multi-user.md#devices-tokens-mcp): `<module>:read`, or
 * `<module>:write`, which includes reading. The auth check refuses a token
 * a route it is not scoped for; the services check the identity token's
 * `scp` again (pkg/identity), and MCP lists only the tools they allow.
 */

export type Scope = `${ScopeModule}:${"read" | "write"}`;

const SCOPE = /^([a-z]+):(read|write)$/;

export function isScopeModule(value: unknown): value is ScopeModule {
  return (SCOPE_MODULES as readonly string[]).includes(value as string);
}

/**
 * The scopes of a request, validated and in a canonical form: one per
 * module, `write` where both are named, in SCOPE_MODULES' order. null when
 * any is not a scope.
 */
export function normalizeScopes(values: unknown): Scope[] | null {
  if (!Array.isArray(values)) return null;
  const level = new Map<ScopeModule, "read" | "write">();
  for (const value of values) {
    if (typeof value !== "string") return null;
    const match = SCOPE.exec(value.trim());
    if (!match || !isScopeModule(match[1])) return null;
    const name = match[1];
    const wanted = match[2] as "read" | "write";
    if (wanted === "write" || !level.has(name)) level.set(name, wanted);
  }
  return SCOPE_MODULES.filter((module) => level.has(module)).map(
    (module) => `${module}:${level.get(module)}` as Scope,
  );
}

/** Whether scopes allow a method on a module's routes. */
export function scopesAllow(
  scopes: readonly string[],
  module: string,
  method: string,
): boolean {
  const reading = ["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
  return (
    scopes.includes(`${module}:write`) ||
    (reading && scopes.includes(`${module}:read`))
  );
}

/**
 * What a route of the gateway is, for the check: a module's API (with the
 * module whose scopes it needs), MCP, the Android downloads, or the
 * Dashboard's own routes. The integrations' APIs belong to the module they
 * feed.
 */
export type RouteKind =
  | { kind: "module"; module: ScopeModule }
  | { kind: "mcp" }
  | { kind: "android" }
  | { kind: "other" };

const INTEGRATION_MODULES: Record<string, ScopeModule> = {
  banking: "finance",
  "google-calendar": "calendar",
  "calendar-feeds": "calendar",
  "recipe-import": "food",
};

export function routeOf(uri: string): RouteKind {
  let path: string;
  try {
    path = new URL(uri, "http://lifeos.invalid").pathname;
  } catch {
    return { kind: "other" };
  }
  if (path === "/api/mcp" || path.startsWith("/api/mcp/")) {
    return { kind: "mcp" };
  }
  if (path === "/android" || path.startsWith("/android/")) {
    return { kind: "android" };
  }
  const integration = /^\/api\/integrations\/([a-z-]+)\//.exec(path);
  if (integration) {
    const owner = INTEGRATION_MODULES[integration[1]];
    return owner ? { kind: "module", module: owner } : { kind: "other" };
  }
  const direct = /^\/api\/([a-z]+)\//.exec(path);
  if (direct && isScopeModule(direct[1])) {
    return { kind: "module", module: direct[1] };
  }
  return { kind: "other" };
}

/**
 * The module scopes an MCP grant gives the identity token: mcp:read reads
 * every module, mcp:write writes (and reads) them. MCP lists the tools
 * these allow and forwards the token to the services, which check it again.
 */
export function mcpModuleScopes(grant: readonly string[]): Scope[] {
  const level = grant.includes("mcp:write")
    ? "write"
    : grant.includes("mcp:read")
      ? "read"
      : null;
  if (!level) return [];
  return SCOPE_MODULES.map((module) => `${module}:${level}` as Scope);
}
