import assert from "node:assert/strict";
import test from "node:test";

import { backupsHandler } from "@/lib/admin-surfaces";
import type { CheckRuntime } from "@/lib/auth/check";
import { TokenCache } from "@/lib/auth/identity";

/**
 * A runtime under legacy access, without a database: the owner, as before
 * accounts. The auth flow suite (test/auth) covers signed-in admins and
 * members against a real auth_db.
 */
function legacyRuntime() {
  const entries: unknown[][] = [];
  const rt = {
    env: { insecureLoginHosts: [] },
    pool: {
      query: async (_text: string, values: unknown[]) => {
        entries.push(values);
        return { rows: [], rowCount: 1 };
      },
    },
    signer: { kid: "test", sign: () => "identity-token" },
    tokens: new TokenCache(),
    legacy: { get: async () => true },
  } as unknown as CheckRuntime;
  return { rt, entries };
}

/** Calls the backup route's handler and returns what reached the service. */
async function call(method: "GET" | "POST", path: string[]) {
  const { rt, entries } = legacyRuntime();
  const seen: string[] = [];
  const request = new Request(
    `http://dashboard.test/api/backups/${path.join("/")}`,
    { method, headers: { "X-Life-OS-Actor": "user" } },
  );
  const response = await backupsHandler(
    rt,
    request,
    path.join("/"),
    async () => {
      seen.push(`${method} ${path.join("/")}`);
      return Response.json({ id: "20261011T120000Z" }, { status: 202 });
    },
  );
  return { status: response.status, seen, entries };
}

test("the backup route passes on reading the status and starting a backup", async () => {
  const status = await call("GET", ["v1", "status"]);
  assert.deepEqual(status.seen, ["GET v1/status"]);
  assert.equal(status.entries.length, 0, "reading the status is not logged");
  const started = await call("POST", ["v1", "backups"]);
  assert.equal(started.status, 202);
  assert.deepEqual(started.seen, ["POST v1/backups"]);
  // Starting a backup is an admin action: the security log has it.
  assert.equal(started.entries.length, 1);
  assert.ok(started.entries[0].includes("backup_started"));
  assert.ok(started.entries[0].includes("legacy"));
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
