import { type CheckRuntime, bearerToken } from "./check";
import { DEVICE_KEY_PREFIX } from "./constants";
import {
  checkKey,
  type KeyKind,
  type KeyRecord,
  keyName,
  revokeKeys,
} from "./keys";
import { anonymousWriteAllowed } from "./origin";
import {
  claimPairingCode,
  isVerifier,
  notePairedKey,
  PairingLimiter,
} from "./pairing";
import {
  clientAddress,
  recordFailedSignIn,
  recordSecurityEvent,
} from "./security-log";

/**
 * The phone and watch apps' endpoints (docs/api/devices.md), under
 * /api/auth/devices/ next to Better Auth's, behind the gateway's strict
 * limit for /api/auth/. Errors look like the services' ({"error": {"code",
 * "message"}}), since the app reads both.
 *
 *   POST /api/auth/devices/pair      a pairing code → the phone's key
 *   POST /api/auth/devices/child     the phone's key → its watch's key
 *   GET  /api/auth/devices/me        a key → who it is
 *   POST /api/auth/devices/sign-out  a key → revoked (with its watch's)
 */

const LIMITER = Symbol.for("lifeos.auth.pairing-limiter");

function limiter(): PairingLimiter {
  const holder = globalThis as { [LIMITER]?: PairingLimiter };
  holder[LIMITER] ??= new PairingLimiter();
  return holder[LIMITER];
}

function json(status: number, body: unknown, headers: HeadersInit = {}) {
  return Response.json(body, {
    status,
    headers: { "cache-control": "no-store", ...headers },
  });
}

function problem(
  status: number,
  code: string,
  message: string,
  headers: HeadersInit = {},
) {
  return json(status, { error: { code, message } }, headers);
}

const PAIRING_CODE_INVALID =
  "That pairing code is wrong, used, or expired. Make a new one in Settings → Devices.";

async function readJson(
  request: Request,
): Promise<Record<string, unknown> | null> {
  const text = await request.text().catch(() => "");
  if (text.length > 4096) return null;
  try {
    const value = JSON.parse(text);
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

/** The key a request presents, verified; or the answer refusing it. */
async function presentedKey(
  rt: CheckRuntime,
  request: Request,
): Promise<{ key: KeyRecord } | { refused: Response }> {
  const bearer = bearerToken(request.headers);
  const refused = problem(
    401,
    "unauthenticated",
    "Send this device's key as Authorization: Bearer.",
  );
  if (!bearer) return { refused };
  const result = await checkKey(rt.pool, bearer);
  if (!result.ok) return { refused };
  return { key: result.key };
}

async function userSummary(rt: CheckRuntime, userId: string) {
  const { rows } = await rt.pool.query<{
    id: string;
    username: string | null;
    name: string;
  }>(
    'SELECT id, "displayUsername" AS username, name FROM "user" WHERE id = $1',
    [userId],
  );
  return rows[0] ?? { id: userId, username: null, name: "" };
}

type CreatedKey = { id: string; key: string; createdAt: Date | string };

/** A device key, through the api-key plugin (stored hashed, shown once). */
export async function createDeviceKey(
  rt: CheckRuntime,
  userId: string,
  name: string,
  kind: Exclude<KeyKind, "token">,
  parentId?: string,
): Promise<CreatedKey> {
  const created = (await rt.auth.api.createApiKey({
    body: {
      userId,
      name,
      prefix: DEVICE_KEY_PREFIX,
      metadata: parentId ? { kind, parentId } : { kind },
      rateLimitEnabled: false,
    },
  })) as unknown as CreatedKey;
  return created;
}

function iso(value: Date | string) {
  return new Date(value).toISOString();
}

async function pair(rt: CheckRuntime, request: Request): Promise<Response> {
  const address = clientAddress(request.headers) ?? "unknown";
  const wait = limiter().attempt(address);
  if (wait > 0) {
    const seconds = Math.ceil(wait / 1000);
    return problem(
      429,
      "too_many_attempts",
      `Too many tries. Try again in ${seconds} s.`,
      { "retry-after": String(seconds) },
    );
  }
  const body = await readJson(request);
  const name = keyName(body?.name);
  const code = typeof body?.code === "string" ? body.code : "";
  const verifier = body?.verifier;
  if (
    !body ||
    !name ||
    !code ||
    (verifier !== undefined && !isVerifier(verifier))
  ) {
    return problem(
      400,
      "bad_request",
      'Send {"code": "…", "name": "…"}, and "verifier" for a code from the sign-in page.',
    );
  }
  const claimed = await claimPairingCode(
    rt.pool,
    code,
    typeof verifier === "string" ? verifier : undefined,
  );
  if (!claimed) {
    limiter().failed(address);
    await recordFailedSignIn(rt.pool, {
      kind: "pairing_refused",
      headers: request.headers,
      detail: "pairing_code_invalid",
    });
    return problem(400, "pairing_code_invalid", PAIRING_CODE_INVALID);
  }
  const created = await createDeviceKey(rt, claimed.userId, name, "phone");
  await notePairedKey(rt.pool, claimed.codeHash, created.id);
  await recordSecurityEvent(rt.pool, {
    kind: "device_paired",
    actorUserId: claimed.userId,
    subjectUserId: claimed.userId,
    via: "device",
    headers: request.headers,
    detail: typeof verifier === "string" ? "phone:sign_in" : "phone:qr",
  });
  return json(200, {
    key: created.key,
    keyId: created.id,
    name,
    kind: "phone",
    createdAt: iso(created.createdAt),
    user: await userSummary(rt, claimed.userId),
    publicUrl: rt.env.publicUrl,
  });
}

async function child(rt: CheckRuntime, request: Request): Promise<Response> {
  const presented = await presentedKey(rt, request);
  if ("refused" in presented) return presented.refused;
  const phone = presented.key;
  if (phone.kind !== "phone") {
    return problem(
      403,
      "not_a_phone_key",
      "Only a phone's key makes a key for its watch.",
    );
  }
  const body = await readJson(request);
  const name = keyName(body?.name);
  if (!name) {
    return problem(
      400,
      "bad_request",
      'Send {"name": "…"}: the watch\'s name.',
    );
  }
  // One watch per phone: the new key replaces the one before (and its push
  // subscriptions go with it).
  const before = (
    await rt.pool.query<{ id: string }>(
      `SELECT id FROM apikey
      WHERE "referenceId" = $1 AND prefix = $2 AND metadata IS NOT NULL
        AND (metadata::jsonb ->> 'parentId') = $3`,
      [phone.userId, DEVICE_KEY_PREFIX, phone.id],
    )
  ).rows.map((row) => row.id);
  const created = await createDeviceKey(
    rt,
    phone.userId,
    name,
    "watch",
    phone.id,
  );
  if (before.length) {
    await revokeKeys(rt.pool, phone.userId, before, { kinds: ["watch"] });
    void rt.keyCleanup?.();
    await recordSecurityEvent(rt.pool, {
      kind: "device_revoked",
      actorUserId: phone.userId,
      subjectUserId: phone.userId,
      via: "device",
      headers: request.headers,
      detail: "watch:replaced",
    });
  }
  await recordSecurityEvent(rt.pool, {
    kind: "device_paired",
    actorUserId: phone.userId,
    subjectUserId: phone.userId,
    via: "device",
    headers: request.headers,
    detail: "watch",
  });
  return json(200, {
    key: created.key,
    keyId: created.id,
    name,
    kind: "watch",
    parentKeyId: phone.id,
    createdAt: iso(created.createdAt),
  });
}

async function me(rt: CheckRuntime, request: Request): Promise<Response> {
  const presented = await presentedKey(rt, request);
  if ("refused" in presented) return presented.refused;
  const key = presented.key;
  rt.keyUses?.touch(key.id);
  return json(200, {
    keyId: key.id,
    name: key.name,
    kind: key.kind,
    parentKeyId: key.parentId,
    createdAt: key.createdAt.toISOString(),
    ...(key.expiresAt ? { expiresAt: key.expiresAt.toISOString() } : {}),
    user: await userSummary(rt, key.userId),
  });
}

async function signOut(rt: CheckRuntime, request: Request): Promise<Response> {
  const presented = await presentedKey(rt, request);
  if ("refused" in presented) return presented.refused;
  const key = presented.key;
  await revokeKeys(rt.pool, key.userId, [key.id]);
  void rt.keyCleanup?.();
  await recordSecurityEvent(rt.pool, {
    kind: key.kind === "token" ? "token_revoked" : "device_revoked",
    actorUserId: key.userId,
    subjectUserId: key.userId,
    via: key.kind === "token" ? "token" : "device",
    headers: request.headers,
    detail: `${key.kind}:self`,
  });
  return new Response(null, {
    status: 204,
    headers: { "cache-control": "no-store" },
  });
}

/** /api/auth/devices/… */
export async function devicesHandler(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const { pathname } = new URL(request.url);
  const route = `${request.method} ${pathname.slice("/api/auth/devices".length)}`;
  // A browser on another site cannot post here; the apps send no Origin.
  if (!anonymousWriteAllowed(request.method, request.headers)) {
    return problem(
      403,
      "forbidden_origin",
      "This request did not come from Life OS.",
    );
  }
  try {
    switch (route) {
      case "POST /pair":
        return await pair(rt, request);
      case "POST /child":
        return await child(rt, request);
      case "GET /me":
        return await me(rt, request);
      case "POST /sign-out":
        return await signOut(rt, request);
      default:
        return problem(404, "not_found", "Not found");
    }
  } catch (error) {
    console.error(
      "devices: request failed",
      route,
      error instanceof Error ? error.message : error,
    );
    return problem(500, "internal", "Something went wrong. Try again.");
  }
}
