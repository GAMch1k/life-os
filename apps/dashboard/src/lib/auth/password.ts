import { randomBytes, scrypt, timingSafeEqual } from "node:crypto";

import { PASSWORD_HASH_CONCURRENCY } from "./constants";

/**
 * Password hashing for Better Auth on node:crypto's asynchronous scrypt,
 * which runs on libuv's thread pool instead of the event loop (the login
 * trial measured Better Auth's pure-JS fallback blocking it for about
 * 110 ms per sign-in). Better Auth's parameters and format, so hashes stay
 * compatible both ways: "<16-byte salt as hex>:<64-byte key as hex>",
 * N=16384, r=16, p=1, the password NFKC-normalised, the salt's hex text as
 * the salt. Each run needs 32 MiB, so at most two run at once.
 */
const N = 16384;
const r = 16;
const p = 1;
const KEY_LENGTH = 64;
const MAX_MEMORY = 64 * 1024 * 1024;

let running = 0;
const waiting: Array<() => void> = [];

async function limited<T>(work: () => Promise<T>): Promise<T> {
  if (running >= PASSWORD_HASH_CONCURRENCY) {
    await new Promise<void>((resolve) => waiting.push(resolve));
  }
  running += 1;
  try {
    return await work();
  } finally {
    running -= 1;
    waiting.shift()?.();
  }
}

function derive(password: string, salt: string): Promise<Buffer> {
  return limited(
    () =>
      new Promise<Buffer>((resolve, reject) => {
        scrypt(
          password.normalize("NFKC"),
          salt,
          KEY_LENGTH,
          { N, r, p, maxmem: MAX_MEMORY },
          (error, key) => (error ? reject(error) : resolve(key)),
        );
      }),
  );
}

export async function hashPassword(password: string): Promise<string> {
  const salt = randomBytes(16).toString("hex");
  const key = await derive(password, salt);
  return `${salt}:${key.toString("hex")}`;
}

export async function verifyPassword({
  hash,
  password,
}: {
  hash: string;
  password: string;
}): Promise<boolean> {
  const [salt, key] = hash.split(":");
  if (!salt || !key || !/^[0-9a-f]+$/i.test(key)) return false;
  const expected = Buffer.from(key, "hex");
  if (expected.length !== KEY_LENGTH) return false;
  const actual = await derive(password, salt);
  return timingSafeEqual(actual, expected);
}
