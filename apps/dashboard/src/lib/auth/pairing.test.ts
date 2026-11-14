import assert from "node:assert/strict";
import { createHash, generateKeyPairSync, sign } from "node:crypto";
import test from "node:test";

import { mcpChallenge, McpTokenVerifier } from "./oauth";
import {
  challengeOf,
  isChallenge,
  isPairingState,
  isVerifier,
  newPairingCode,
  pairingLink,
  PairingLimiter,
} from "./pairing";

test("pairing codes: 16 characters a person can type", () => {
  const code = newPairingCode();
  assert.match(code, /^[A-HJ-NP-Z2-9]{4}(-[A-HJ-NP-Z2-9]{4}){3}$/);
  assert.notEqual(newPairingCode(), code);
});

test("the pairing link is always lifeos://pair", () => {
  const link = new URL(
    pairingLink(
      "https://lifeos.example.com",
      { code: "AAAA-BBBB" },
      "st4te-ok",
    ),
  );
  assert.equal(link.protocol, "lifeos:");
  assert.equal(link.searchParams.get("host"), "https://lifeos.example.com");
  assert.equal(link.searchParams.get("code"), "AAAA-BBBB");
  assert.equal(link.searchParams.get("state"), "st4te-ok");
  const denied = new URL(
    pairingLink("https://x.test", { error: "access_denied" }, "bad state!"),
  );
  assert.equal(denied.searchParams.get("error"), "access_denied");
  assert.equal(
    denied.searchParams.get("state"),
    null,
    "an invalid state is dropped",
  );
  assert.ok(
    pairingLink("https://x.test", { code: "A" }).startsWith("lifeos://pair?"),
  );
});

test("challenge, verifier, and state are checked", () => {
  const verifier = "v".repeat(43);
  const challenge = challengeOf(verifier);
  assert.equal(
    challenge,
    createHash("sha256").update(verifier).digest("base64url"),
  );
  assert.equal(isChallenge(challenge), true);
  assert.equal(isChallenge(`${challenge}=`), false);
  assert.equal(isVerifier(verifier), true);
  assert.equal(isVerifier("short"), false);
  assert.equal(isVerifier("v".repeat(129)), false);
  assert.equal(isPairingState("abcdefgh"), true);
  assert.equal(isPairingState("abc"), false);
  assert.equal(isPairingState("a b c d e f"), false);
});

test("redeeming is limited: 5 a minute, and 15 minutes off after 10 wrong codes", () => {
  const limiter = new PairingLimiter();
  let now = 1_000_000;
  for (let i = 0; i < 5; i += 1) assert.equal(limiter.attempt("a", now), 0);
  assert.ok(limiter.attempt("a", now) > 0);
  assert.equal(limiter.attempt("b", now), 0, "another address");
  now += 61_000;
  assert.equal(limiter.attempt("a", now), 0);
  for (let i = 0; i < 10; i += 1) {
    now += 13_000;
    limiter.attempt("c", now);
    limiter.failed("c", now);
  }
  const wait = limiter.attempt("c", now);
  assert.ok(wait > 14 * 60_000 && wait <= 15 * 60_000, `${wait}`);
  assert.equal(limiter.attempt("c", now + 15 * 60_000 + 1), 0);
});

test("MCP's challenge points to the resource metadata", () => {
  assert.equal(
    mcpChallenge("https://lifeos.test"),
    'Bearer resource_metadata="https://lifeos.test/.well-known/oauth-protected-resource/api/mcp", scope="mcp:read mcp:write offline_access"',
  );
  assert.match(
    mcpChallenge("https://lifeos.test", "invalid_token"),
    /error="invalid_token"$/,
  );
});

test("MCP access tokens: the jwt plugin's key, issuer, audience, lifetime, and type", async () => {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const jwk = publicKey.export({ format: "jwk" });
  const db = {
    query: async () => ({
      rows: [{ id: "k1", publicKey: JSON.stringify(jwk), alg: "EdDSA" }],
    }),
  };
  const verifier = new McpTokenVerifier(
    db as never,
    "https://lifeos.test",
    "https://lifeos.test/api/mcp",
  );
  const now = 1_800_000_000;
  const make = (
    payload: Record<string, unknown>,
    header: Record<string, unknown> = {},
  ) => {
    const h = Buffer.from(
      JSON.stringify({ alg: "EdDSA", typ: "at+jwt", kid: "k1", ...header }),
    ).toString("base64url");
    const p = Buffer.from(
      JSON.stringify({
        iss: "https://lifeos.test",
        aud: "https://lifeos.test/api/mcp",
        sub: "00000000-0000-4000-8000-000000000001",
        client_id: "client-1",
        scope: "mcp:read offline_access",
        iat: now,
        exp: now + 900,
        ...payload,
      }),
    ).toString("base64url");
    const s = sign(null, Buffer.from(`${h}.${p}`), privateKey).toString(
      "base64url",
    );
    return `${h}.${p}.${s}`;
  };
  const ok = await verifier.verify(make({}), now + 10);
  assert.deepEqual(ok, {
    userId: "00000000-0000-4000-8000-000000000001",
    clientId: "client-1",
    scopes: ["mcp:read", "offline_access"],
    issuedAt: now,
    expiresAt: now + 900,
  });
  for (const [what, token] of [
    ["expired", make({ exp: now - 100 })],
    ["another issuer", make({ iss: "https://evil.example" })],
    ["another audience", make({ aud: "https://lifeos.test/api/tasks" })],
    ["no subject", make({ sub: undefined })],
    ["an id token", make({}, { typ: "JWT" })],
    ["another algorithm", make({}, { alg: "HS256" })],
    ["an unknown key", make({}, { kid: "k2" })],
    ["issued in the future", make({ iat: now + 3600 })],
  ] as const) {
    assert.equal(await verifier.verify(token, now + 10), null, what);
  }
  const [h, p] = make({}).split(".");
  assert.equal(await verifier.verify(`${h}.${p}.AAAA`, now + 10), null);
});
