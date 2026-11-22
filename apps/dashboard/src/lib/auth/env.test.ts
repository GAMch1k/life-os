import assert from "node:assert/strict";
import { test } from "node:test";

import { AUTH_SECRET_MIN_BYTES, isPlaceholder, readAuthEnv } from "./env";

const base = {
  LIFE_OS_PUBLIC_URL: "https://lifeos.example.com",
  AUTH_DATABASE_URL: "postgres://auth_user:pw@postgres:5432/auth_db",
  LIFE_OS_AUTH_SECRET: "a".repeat(64),
  LIFE_OS_IDENTITY_SIGNING_KEY: '{"kty":"OKP"}',
  LIFE_OS_TRUSTED_KEYS: '{"keys":[]}',
};

test("a generated secret is accepted", () => {
  assert.equal(readAuthEnv(base).secret, "a".repeat(64));
  // Exactly the minimum is enough.
  const secret = "b".repeat(AUTH_SECRET_MIN_BYTES);
  assert.equal(
    readAuthEnv({ ...base, LIFE_OS_AUTH_SECRET: secret }).secret,
    secret,
  );
});

test(".env.example's placeholder secret is refused at start", () => {
  for (const placeholder of [
    "replace-with-a-generated-secret",
    "replace-with-openssl-rand-hex-32",
    "  Replace-With-something-long-enough-to-pass-the-length-check ",
  ]) {
    assert.throws(
      () => readAuthEnv({ ...base, LIFE_OS_AUTH_SECRET: placeholder }),
      /LIFE_OS_AUTH_SECRET is still \.env\.example's placeholder/,
    );
  }
});

test("a secret shorter than 32 bytes is refused at start", () => {
  assert.throws(
    () => readAuthEnv({ ...base, LIFE_OS_AUTH_SECRET: "x".repeat(31) }),
    /LIFE_OS_AUTH_SECRET is shorter than 32 bytes/,
  );
  assert.throws(
    () => readAuthEnv({ ...base, LIFE_OS_AUTH_SECRET: "hunter2" }),
    /shorter than 32 bytes/,
  );
  // Bytes, not characters: 16 two-byte characters are 32 bytes.
  assert.equal(
    readAuthEnv({ ...base, LIFE_OS_AUTH_SECRET: "é".repeat(16) }).secret,
    "é".repeat(16),
  );
});

test("the keys' placeholders are refused at start too", () => {
  assert.throws(
    () =>
      readAuthEnv({
        ...base,
        LIFE_OS_IDENTITY_SIGNING_KEY: "replace-with-a-generated-key",
      }),
    /LIFE_OS_IDENTITY_SIGNING_KEY is still \.env\.example's placeholder/,
  );
  assert.throws(
    () =>
      readAuthEnv({
        ...base,
        LIFE_OS_TRUSTED_KEYS: "replace-with-generated-keys",
      }),
    /LIFE_OS_TRUSTED_KEYS is still \.env\.example's placeholder/,
  );
});

test("placeholders are told apart from real values", () => {
  assert.equal(isPlaceholder("replace-with-a-local-password"), true);
  assert.equal(isPlaceholder("0123abcd"), false);
  assert.equal(isPlaceholder("my-replace-with-value"), false);
});
