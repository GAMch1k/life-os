import { generateKeyPairSync, randomBytes } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { Client } from "pg";

import { backupsHandler, statusHandler } from "../../src/lib/admin-surfaces";
import { accountHandler } from "../../src/lib/auth/account";
import { adminHandler } from "../../src/lib/auth/admin";
import type { CheckRuntime } from "../../src/lib/auth/check";
import type { RuntimeStatus } from "../../src/lib/runtime-status";
import { authHandler, checkHandler } from "../../src/lib/auth/handlers";
import {
  oauthMetadataHandler,
  protectedResourceHandler,
} from "../../src/lib/auth/oauth-metadata";
import { createRuntime } from "../../src/lib/auth/runtime";

/**
 * The auth flow suite's world: a database of its own on a throwaway
 * PostgreSQL (AUTH_TEST_DATABASE_URL, a superuser's URL; tools/test-auth
 * makes one), auth_db's migrations, the Dashboard's auth runtime with
 * made-up keys, and browsers with cookie jars that call the same handlers
 * the Next.js routes mount. Nothing listens on a port.
 */

export const PUBLIC_URL = "https://lifeos.test";
export const MIGRATIONS = path.join(__dirname, "..", "..", "db", "migrations");

export function adminUrl(): string | undefined {
  return process.env.AUTH_TEST_DATABASE_URL;
}

export function databaseUrl(base: string, name: string) {
  const url = new URL(base);
  url.pathname = `/${name}`;
  return url.toString();
}

export async function sql(url: string, text: string, values: unknown[] = []) {
  const client = new Client({ connectionString: url });
  await client.connect();
  try {
    return await client.query(text, values);
  } finally {
    await client.end();
  }
}

/** A fresh database with auth_db's migrations applied, as auth-migrate does. */
const REGISTRY = path.join(
  __dirname,
  "..",
  "..",
  "..",
  "..",
  "infra",
  "postgres",
  "service-databases.conf",
);

/** Never a Life OS server: refuse one with a database from the registry. */
async function refuseLifeOs(admin: string) {
  const names = readFileSync(REGISTRY, "utf8")
    .split("\n")
    .filter((line) => line && !line.startsWith("#"))
    .map((line) => line.split("|")[1]);
  const { rows } = await sql(
    admin,
    "SELECT datname FROM pg_database WHERE datname = ANY($1)",
    [names],
  );
  if (rows.length) {
    throw new Error(
      `the server has Life OS databases (${rows.map((r) => r.datname).join(", ")}): use a throwaway PostgreSQL`,
    );
  }
}

export async function freshDatabase(admin: string) {
  await refuseLifeOs(admin);
  const name = `it_auth_${randomBytes(4).toString("hex")}`;
  await sql(admin, `CREATE DATABASE ${name}`);
  const url = databaseUrl(admin, name);
  for (const file of readdirSync(MIGRATIONS)
    .filter((f) => f.endsWith(".up.sql"))
    .sort()) {
    await sql(url, readFileSync(path.join(MIGRATIONS, file), "utf8"));
  }
  return {
    url,
    async drop() {
      await sql(admin, `DROP DATABASE IF EXISTS ${name} WITH (FORCE)`);
    },
  };
}

/** An Ed25519 key pair as setup.sh writes it: a private JWK and a JWKS. */
export function testKeys(kid = `auth-test-${randomBytes(2).toString("hex")}`) {
  const { privateKey } = generateKeyPairSync("ed25519");
  const jwk = privateKey.export({ format: "jwk" });
  const signingKey = JSON.stringify({ ...jwk, kid });
  const publicJwk = {
    kty: "OKP",
    crv: "Ed25519",
    x: jwk.x,
    kid,
    use: "sig",
    alg: "EdDSA",
  };
  return { kid, signingKey, publicJwk };
}

export type RuntimeOptions = {
  legacy?: boolean;
  insecureHosts?: string;
  keys?: ReturnType<typeof testKeys>;
  trusted?: object[];
  /** Notifications, for the push subscriptions of revoked device keys. */
  notifications?: typeof fetch;
};

export function runtime(url: string, options: RuntimeOptions = {}) {
  const keys = options.keys ?? testKeys();
  return createRuntime(
    {
      NODE_ENV: "test",
      AUTH_DATABASE_URL: url,
      LIFE_OS_AUTH_SECRET: randomBytes(32).toString("hex"),
      LIFE_OS_IDENTITY_SIGNING_KEY: keys.signingKey,
      LIFE_OS_TRUSTED_KEYS: JSON.stringify({
        keys: options.trusted ?? [keys.publicJwk],
      }),
      LIFE_OS_PUBLIC_URL: PUBLIC_URL,
      LIFE_OS_INSECURE_LOGIN_HOSTS: options.insecureHosts ?? "",
      LIFE_OS_LEGACY_ACCESS: options.legacy ? "on" : "off",
      ...(options.notifications
        ? { NOTIFICATIONS_SERVICE_URL: "http://notifications.test" }
        : {}),
    },
    { fetch: options.notifications },
  );
}

export type AppOptions = {
  /** The runtime status /api/status reads (the Status page's checks). */
  status?: () => Promise<RuntimeStatus>;
  /** The backup service behind /api/backups/. */
  backup?: (request: Request, path: string) => Promise<Response>;
};

/** The routes a browser reaches on the Dashboard, as the Next.js routes mount them. */
export function app(rt: CheckRuntime, options: AppOptions = {}) {
  return async (request: Request): Promise<Response> => {
    const { pathname } = new URL(request.url);
    if (pathname.startsWith("/api/auth/")) return authHandler(rt, request);
    if (pathname === "/internal/auth/check") return checkHandler(rt, request);
    if (pathname.startsWith("/api/account")) return accountHandler(rt, request);
    if (pathname.startsWith("/api/admin")) return adminHandler(rt, request);
    if (pathname === "/.well-known/oauth-authorization-server") {
      return oauthMetadataHandler(rt, request);
    }
    if (pathname.startsWith("/.well-known/oauth-protected-resource")) {
      return protectedResourceHandler(rt, request, true);
    }
    if (pathname === "/api/status" && options.status) {
      return statusHandler(rt, request, options.status);
    }
    const backup = pathname.match(/^\/api\/backups\/(.+)$/);
    if (backup && options.backup) {
      const forward = options.backup;
      return backupsHandler(rt, request, backup[1], () =>
        forward(request, backup[1]),
      );
    }
    return new Response("not found", { status: 404 });
  };
}

type Fetcher = (request: Request) => Promise<Response>;

export type Reply = {
  status: number;
  data: any; // eslint-disable-line @typescript-eslint/no-explicit-any
  headers: Headers;
};

/**
 * A browser: a cookie jar, an origin (the page it runs on), and a client
 * address, as the gateway passes it on (X-Real-IP).
 */
export class Browser {
  readonly jar = new Map<string, string>();

  constructor(
    private readonly fetcher: Fetcher,
    readonly origin = PUBLIC_URL,
    readonly address = `198.51.100.${1 + Math.floor(Math.random() * 250)}`,
  ) {}

  cookieHeader() {
    return [...this.jar].map(([k, v]) => `${k}=${v}`).join("; ");
  }

  async req(
    pathname: string,
    {
      method = "GET",
      body,
      headers = {},
    }: {
      method?: string;
      body?: unknown;
      headers?: Record<string, string>;
    } = {},
  ): Promise<Reply> {
    const url = new URL(pathname, this.origin);
    const h = new Headers({
      host: url.host,
      "x-real-ip": this.address,
      ...(method !== "GET" ? { origin: this.origin } : {}),
      ...headers,
    });
    if (this.jar.size) h.set("cookie", this.cookieHeader());
    let payload: string | undefined;
    if (body !== undefined) {
      h.set("content-type", "application/json");
      payload = JSON.stringify(body);
    }
    const response = await this.fetcher(
      new Request(url, {
        method,
        headers: h,
        body: payload,
        redirect: "manual",
      }),
    );
    for (const c of response.headers.getSetCookie()) {
      const [kv] = c.split(";");
      const i = kv.indexOf("=");
      const k = kv.slice(0, i);
      const v = kv.slice(i + 1);
      if (/max-age=0/i.test(c) || v === "") this.jar.delete(k);
      else this.jar.set(k, v);
    }
    const text = await response.text();
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
    return { status: response.status, data, headers: response.headers };
  }

  /**
   * What the gateway's auth_request asks for a request of this browser to
   * a service route: the check, with the original method and URI, and the
   * browser's own headers.
   */
  async check(
    method = "GET",
    uri = "/api/tasks/v1/tasks",
    headers: Record<string, string> = {},
  ): Promise<Reply> {
    return this.req("/internal/auth/check", {
      headers: {
        "x-original-method": method,
        "x-original-uri": uri,
        ...(method !== "GET" ? { origin: this.origin } : {}),
        ...headers,
      },
    });
  }
}
