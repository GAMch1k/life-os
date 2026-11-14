import type { CheckRuntime, Principal } from "./check";
import {
  SESSION_COOKIE,
  TOKEN_DEFAULT_DAYS,
  TOKEN_MAX_DAYS,
  TOKEN_PREFIX,
} from "./constants";
import { devName } from "./check";
import {
  keyName,
  type KeyRecord,
  listKeys,
  revokeCredentials,
  revokeKeys,
} from "./keys";
import { listConnectedApps, revokeConnectedApp } from "./oauth";
import {
  createPairingCode,
  isChallenge,
  isPairingState,
  pairingLink,
} from "./pairing";
import { normalizeScopes } from "./scopes";
import { recordSecurityEvent } from "./security-log";

/**
 * The account's devices, API tokens, and connected apps
 * (docs/api/devices.md#for-the-dashboard-managing-devices,
 * docs/install/accounts.md): /api/account/… routes for a real sign-in only
 * (accountHandler refuses anything else first). Every write needs Life
 * OS's own origin (the check's CSRF rule).
 */

function json(status: number, body: unknown, headers: HeadersInit = {}) {
  return Response.json(body, {
    status,
    headers: { "cache-control": "no-store", ...headers },
  });
}

function problem(status: number, code: string, message: string) {
  return json(status, { error: { code, message } });
}

async function body(request: Request): Promise<Record<string, unknown>> {
  try {
    const value = (await request.json()) as unknown;
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

export type DeviceInfo = {
  id: string;
  name: string;
  kind: "phone" | "watch";
  parentId: string | null;
  createdAt: string;
  lastUsedAt: string | null;
};

export type TokenInfo = {
  id: string;
  name: string;
  start: string | null;
  scopes: string[];
  devTool: boolean;
  /** The activity actor of a development tool: dev:<name>. */
  actor: string | null;
  createdAt: string;
  expiresAt: string | null;
  /** Expired: it no longer works, and is deleted 30 days later. */
  expired: boolean;
  lastUsedAt: string | null;
};

function deviceInfo(key: KeyRecord): DeviceInfo {
  return {
    id: key.id,
    name: key.name,
    kind: key.kind === "watch" ? "watch" : "phone",
    parentId: key.parentId,
    createdAt: key.createdAt.toISOString(),
    lastUsedAt: key.lastUsedAt?.toISOString() ?? null,
  };
}

function tokenInfo(key: KeyRecord): TokenInfo {
  return {
    id: key.id,
    name: key.name,
    start: key.start,
    scopes: key.scopes ?? [],
    devTool: key.devTool,
    actor: key.devTool ? `dev:${devName(key.name)}` : null,
    createdAt: key.createdAt.toISOString(),
    expiresAt: key.expiresAt?.toISOString() ?? null,
    expired: key.expiresAt !== null && key.expiresAt.getTime() <= Date.now(),
    lastUsedAt: key.lastUsedAt?.toISOString() ?? null,
  };
}

export async function listDevices(rt: CheckRuntime, userId: string) {
  return (await listKeys(rt.pool, userId, ["phone", "watch"])).map(deviceInfo);
}

export async function listTokens(rt: CheckRuntime, userId: string) {
  return (await listKeys(rt.pool, userId, ["token"])).map(tokenInfo);
}

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * When a new token expires: the time asked for (the Dashboard sends the
 * end of the chosen day), at least an hour and at most a year from now;
 * 90 days without one.
 */
export function tokenExpiry(value: unknown, now = Date.now()): Date | null {
  if (value === undefined || value === null || value === "") {
    return new Date(now + TOKEN_DEFAULT_DAYS * DAY_MS);
  }
  if (typeof value !== "string") return null;
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return null;
  // A day's slack, for the end of a day in any timezone.
  if (at.getTime() < now + 60 * 60 * 1000) return null;
  if (at.getTime() > now + (TOKEN_MAX_DAYS + 1) * DAY_MS) return null;
  return at;
}

async function createToken(
  rt: CheckRuntime,
  principal: Principal,
  request: Request,
): Promise<Response> {
  const input = await body(request);
  const name = keyName(input.name);
  const scopes = normalizeScopes(input.scopes);
  const expiresAt = tokenExpiry(input.expiresAt);
  const devTool = input.devTool === true;
  if (!name) {
    return problem(
      400,
      "bad_request",
      "Give the token a name (1 to 64 characters).",
    );
  }
  if (!scopes || scopes.length === 0) {
    return problem(
      400,
      "bad_request",
      'Choose what the token may do: scopes such as "tasks:read" or "food:write".',
    );
  }
  if (!expiresAt) {
    return problem(
      400,
      "bad_request",
      `The token must expire between an hour and ${TOKEN_MAX_DAYS} days from now.`,
    );
  }
  const created = (await rt.auth.api.createApiKey({
    body: {
      userId: principal.userId,
      name,
      prefix: TOKEN_PREFIX,
      expiresIn: Math.floor((expiresAt.getTime() - Date.now()) / 1000),
      metadata: { kind: "token", scopes, devTool },
      rateLimitEnabled: false,
    },
  })) as unknown as {
    id: string;
    key: string;
    start: string | null;
    createdAt: Date | string;
    expiresAt: Date | string | null;
  };
  await recordSecurityEvent(rt.pool, {
    kind: "token_created",
    actorUserId: principal.userId,
    subjectUserId: principal.userId,
    via: "session",
    headers: request.headers,
    detail: devTool ? "dev_tool" : null,
  });
  return json(201, {
    token: created.key,
    id: created.id,
    name,
    start: created.start,
    scopes,
    devTool,
    actor: devTool ? `dev:${devName(name)}` : null,
    createdAt: new Date(created.createdAt).toISOString(),
    expiresAt: created.expiresAt
      ? new Date(created.expiresAt).toISOString()
      : null,
  });
}

const UUID = "[0-9a-fA-F-]{36}";

/**
 * /api/account/devices…, /tokens…, /connected-apps…, and
 * /sign-out-everywhere for a principal signed in with a session; null for
 * any other path.
 */
export async function credentialsRoute(
  rt: CheckRuntime,
  request: Request,
  principal: Principal,
  pathname: string,
): Promise<Response | null> {
  const method = request.method;
  const userId = principal.userId;

  if (pathname === "/api/account/devices/pairing-codes" && method === "POST") {
    const input = await body(request);
    const challenge = input.challenge;
    const state = input.state;
    if (challenge !== undefined && !isChallenge(challenge)) {
      return problem(
        400,
        "bad_request",
        "challenge must be BASE64URL(SHA-256(verifier)).",
      );
    }
    if (state !== undefined && !isPairingState(state)) {
      return problem(
        400,
        "bad_request",
        "state must be 8 to 128 letters, digits, - or _.",
      );
    }
    const { code, expiresAt } = await createPairingCode(rt.pool, userId, {
      challenge: challenge as string | undefined,
    });
    return json(201, {
      code,
      expiresAt: expiresAt.toISOString(),
      link: pairingLink(
        rt.env.publicUrl,
        { code },
        state as string | undefined,
      ),
    });
  }

  if (pathname === "/api/account/devices" && method === "GET") {
    return json(200, { devices: await listDevices(rt, userId) });
  }

  const device = new RegExp(`^/api/account/devices/(${UUID})$`).exec(pathname);
  if (device && method === "DELETE") {
    const revoked = await revokeKeys(rt.pool, userId, [device[1]], {
      kinds: ["phone", "watch"],
    });
    if (!revoked.some((key) => key.id === device[1])) {
      return problem(404, "not_found", "There is no such device.");
    }
    void rt.keyCleanup?.();
    for (const key of revoked) {
      await recordSecurityEvent(rt.pool, {
        kind: "device_revoked",
        actorUserId: userId,
        subjectUserId: userId,
        via: "session",
        headers: request.headers,
        detail: key.kind,
      });
    }
    return new Response(null, { status: 204 });
  }

  if (pathname === "/api/account/tokens" && method === "GET") {
    return json(200, { tokens: await listTokens(rt, userId) });
  }
  if (pathname === "/api/account/tokens" && method === "POST") {
    return createToken(rt, principal, request);
  }
  const token = new RegExp(`^/api/account/tokens/(${UUID})$`).exec(pathname);
  if (token && method === "DELETE") {
    const revoked = await revokeKeys(rt.pool, userId, [token[1]], {
      kinds: ["token"],
    });
    if (!revoked.length) {
      return problem(404, "not_found", "There is no such token.");
    }
    await recordSecurityEvent(rt.pool, {
      kind: "token_revoked",
      actorUserId: userId,
      subjectUserId: userId,
      via: "session",
      headers: request.headers,
    });
    return new Response(null, { status: 204 });
  }

  if (pathname === "/api/account/connected-apps" && method === "GET") {
    return json(200, { apps: await listConnectedApps(rt.pool, userId) });
  }
  const app = /^\/api\/account\/connected-apps\/([A-Za-z0-9._~-]{1,255})$/.exec(
    pathname,
  );
  if (app && method === "DELETE") {
    if (!(await revokeConnectedApp(rt.pool, userId, app[1]))) {
      return problem(404, "not_found", "There is no such connected app.");
    }
    await recordSecurityEvent(rt.pool, {
      kind: "oauth_consent_revoked",
      actorUserId: userId,
      subjectUserId: userId,
      via: "session",
      headers: request.headers,
    });
    return new Response(null, { status: 204 });
  }

  if (pathname === "/api/account/sign-out-everywhere" && method === "POST") {
    const revoked = await revokeCredentials(rt.pool, userId);
    rt.tokens.clear();
    void rt.keyCleanup?.();
    await recordSecurityEvent(rt.pool, {
      kind: "signed_out_everywhere",
      actorUserId: userId,
      subjectUserId: userId,
      via: "session",
      headers: request.headers,
      detail:
        revoked.devices || revoked.tokens || revoked.grants
          ? "with_devices"
          : null,
    });
    // This session went too: the browser forgets its cookie.
    return new Response(null, {
      status: 204,
      headers: {
        "cache-control": "no-store",
        "set-cookie": `${SESSION_COOKIE}=; Max-Age=0; Path=/; HttpOnly; Secure; SameSite=Lax`,
      },
    });
  }

  return null;
}
