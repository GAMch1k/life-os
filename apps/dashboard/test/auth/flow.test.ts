import assert from "node:assert/strict";
import { after, before, describe, test } from "node:test";

import { FIRST_SPACE_ID, FIRST_USER_ID } from "../../src/lib/auth/constants";
import { parseTrustedKeys, verifyIdentityToken } from "../../src/lib/auth/identity";
import { createSetupCode } from "../../src/lib/auth/store";
import { totp } from "./authenticators";
import {
  adminUrl,
  app,
  Browser,
  freshDatabase,
  runtime,
  testKeys,
} from "./harness";

const admin = adminUrl();
const password = "correct horse battery staple 42";

describe("auth flow", { skip: admin ? false : "AUTH_TEST_DATABASE_URL is not set" }, () => {
  let db: Awaited<ReturnType<typeof freshDatabase>>;
  let rt: ReturnType<typeof runtime>;
  const keys = testKeys();
  let fetcher: ReturnType<typeof app>;

  before(async () => {
    db = await freshDatabase(admin!);
    rt = runtime(db.url, { keys, legacy: false });
    fetcher = app(rt);
  });
  after(async () => {
    await rt?.pool.end();
    await db?.drop();
  });

  test("the first account needs the setup code and gets the fixed ids", async () => {
    const owner = new Browser(fetcher);
    let r = await owner.req("/api/auth/sign-up/email", {
      method: "POST",
      body: { username: "owner", password, name: "Owner" },
    });
    assert.equal(r.status, 403, JSON.stringify(r.data));
    const code = await createSetupCode(rt.pool);
    assert.ok(code);
    r = await owner.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code! },
      body: { username: "owner", password, name: "Owner" },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.user.id, FIRST_USER_ID);
    assert.equal(r.data.user.role, "owner");
    assert.match(r.data.user.email, /^u-[0-9a-f]+@users\.invalid$/);
    assert.ok(owner.jar.has("__Host-lifeos.session"), [...owner.jar.keys()].join());
    const check = await owner.check();
    assert.equal(check.status, 204);
    const token = check.headers.get("x-life-os-identity")!;
    const claims = verifyIdentityToken(token, parseTrustedKeys(JSON.stringify({ keys: [keys.publicJwk] })));
    assert.ok(claims);
    assert.equal(claims.sub, FIRST_USER_ID);
    assert.equal(claims.via, "session");
    assert.equal(claims.role, "owner");
    assert.deepEqual(claims.sp, { [FIRST_SPACE_ID]: "o" });

    const again = new Browser(fetcher);
    r = await again.req("/api/auth/sign-up/email", {
      method: "POST",
      headers: { "x-lifeos-setup-code": code! },
      body: { username: "second", password },
    });
    assert.equal(r.status, 403);
  });

  test("password sign-in by username, then TOTP", async () => {
    const b = new Browser(fetcher);
    let r = await b.req("/api/auth/sign-in/username", {
      method: "POST",
      body: { username: "OWNER", password },
    });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    r = await b.req("/api/auth/two-factor/enable", { method: "POST", body: { password } });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal(r.data.backupCodes.length, 10);
    const uri = r.data.totpURI as string;
    r = await b.req("/api/auth/two-factor/verify-totp", { method: "POST", body: { code: totp(uri) } });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    const c = new Browser(fetcher);
    r = await c.req("/api/auth/sign-in/username", { method: "POST", body: { username: "owner", password } });
    assert.equal(r.data.twoFactorRedirect, true, JSON.stringify(r.data));
    assert.equal((await c.check()).status, 401);
    r = await c.req("/api/auth/two-factor/verify-totp", { method: "POST", body: { code: totp(uri) } });
    assert.equal(r.status, 200, JSON.stringify(r.data));
    assert.equal((await c.check()).status, 204);
  });
});
