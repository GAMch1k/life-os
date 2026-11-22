import assert from "node:assert/strict";
import { test } from "node:test";

import { isKnownDevice, knownDeviceValue } from "./known-device";
import { PairingLimiter } from "./pairing";
import { ACCOUNT_FREE_FAILURES, addressKey, LoginThrottle } from "./throttle";

test("an address counts as itself, an IPv6 address as its /64", () => {
  assert.equal(addressKey("203.0.113.9"), "203.0.113.9");
  assert.equal(addressKey("2001:db8:1:2:3:4:5:6"), "2001:db8:1:2::/64");
  assert.equal(addressKey("2001:DB8:1:2::ff"), "2001:db8:1:2::/64");
  assert.equal(addressKey("2001:db8::1"), "2001:db8:0:0::/64");
  assert.equal(addressKey("2001:db8:0:0:1::"), "2001:db8:0:0::/64");
  assert.equal(addressKey("::1"), "0:0:0:0::/64");
  assert.equal(addressKey("fe80::1%eth0"), "fe80:0:0:0::/64");
  // An IPv4 address in IPv6 form is the IPv4 address.
  assert.equal(addressKey("::ffff:192.0.2.1"), "192.0.2.1");
  assert.equal(addressKey("::ffff:c000:201"), "192.0.2.1");
  // No address: as it is.
  assert.equal(addressKey("unknown"), "unknown");
  assert.equal(addressKey(""), "");
});

test("the per-address delay counts a /64 as one address", () => {
  const throttle = new LoginThrottle();
  const t = 1_000_000;
  // Five failures, each from another address of the same /64.
  for (let i = 1; i <= 5; i += 1) {
    throttle.failed("anna", `2001:db8:1:2::${i}`, t);
  }
  assert.equal(throttle.waitMs("anna", "2001:db8:1:2::99", t), 15_000);
  assert.equal(throttle.waitMs("anna", "2001:db8:1:3::1", t), 0);
  assert.deepEqual(throttle.attempt("anna", "2001:db8:1:2:ffff::1", {}, t), {
    waitMs: 15_000,
    account: false,
  });
});

test("failures from many addresses slow the account down", () => {
  const throttle = new LoginThrottle();
  const t = 1_000_000;
  const started: boolean[] = [];
  // One failure each from many addresses: none of them waits on its own.
  for (let i = 1; i <= ACCOUNT_FREE_FAILURES; i += 1) {
    started.push(throttle.failed("Anna", `198.51.100.${i}`, t));
  }
  // The tenth starts the account's delay, once.
  assert.deepEqual(
    started,
    Array.from(
      { length: ACCOUNT_FREE_FAILURES },
      (_, i) => i === ACCOUNT_FREE_FAILURES - 1,
    ),
  );
  // A new address waits a minute, for this account only.
  assert.deepEqual(throttle.attempt("anna", "192.0.2.77", {}, t), {
    waitMs: 60_000,
    account: true,
  });
  assert.deepEqual(throttle.attempt("bob", "192.0.2.77", {}, t), {
    waitMs: 0,
    account: false,
  });
  // After the minute, one attempt goes through and takes the turn: a
  // burst of them gets one through, not all.
  assert.equal(
    throttle.attempt("anna", "192.0.2.78", {}, t + 60_000).waitMs,
    0,
  );
  assert.equal(
    throttle.attempt("anna", "192.0.2.79", {}, t + 60_000).waitMs,
    60_000,
  );
  // It failed: two minutes, then four, … up to 15.
  assert.equal(throttle.failed("anna", "192.0.2.78", t + 60_000), false);
  assert.equal(throttle.accountWaitMs("anna", t + 60_000), 120_000);
  let now = t + 60_000;
  for (let i = 0; i < 10; i += 1) {
    now += 20 * 60_000;
    throttle.failed("anna", `203.0.113.${i}`, now);
  }
  assert.equal(throttle.accountWaitMs("anna", now), 15 * 60_000);
  // An hour after the last failure, it is forgotten.
  assert.equal(throttle.accountWaitMs("anna", now + 60 * 60_000 + 1), 0);
});

test("a browser that signed in before is never held back by the account's delay", () => {
  const throttle = new LoginThrottle();
  const t = 1_000_000;
  for (let i = 1; i <= 30; i += 1) {
    throttle.failed("anna", `198.51.100.${i}`, t + i);
  }
  assert.equal(throttle.attempt("anna", "192.0.2.1", {}, t + 31).account, true);
  assert.deepEqual(
    throttle.attempt("anna", "192.0.2.1", { knownDevice: true }, t + 31),
    { waitMs: 0, account: false },
  );
  // Its own address's delay still holds, as before.
  for (let i = 0; i < 5; i += 1) throttle.failed("anna", "192.0.2.1", t + 40);
  assert.deepEqual(
    throttle.attempt("anna", "192.0.2.1", { knownDevice: true }, t + 40),
    { waitMs: 15_000, account: false },
  );
});

test("a right password or a reset link ends the account's delay", () => {
  const throttle = new LoginThrottle();
  const t = 1_000_000;
  for (let i = 1; i <= ACCOUNT_FREE_FAILURES; i += 1) {
    throttle.failed("anna", `198.51.100.${i}`, t);
  }
  assert.equal(throttle.accountWaitMs("anna", t), 60_000);
  throttle.forgetAccount("ANNA");
  assert.equal(throttle.accountWaitMs("anna", t), 0);
  for (let i = 1; i <= ACCOUNT_FREE_FAILURES; i += 1) {
    throttle.failed("anna", `198.51.100.${i}`, t);
  }
  throttle.succeeded("anna", "192.0.2.1");
  assert.equal(throttle.accountWaitMs("anna", t), 0);
});

test("the known-device cookie is this account's and this server's alone", () => {
  const secret = "s".repeat(64);
  const value = knownDeviceValue(secret, "Anna");
  assert.match(value, /^[A-Za-z0-9_-]{43}$/);
  assert.equal(isKnownDevice(secret, "anna", value), true);
  assert.equal(isKnownDevice(secret, " ANNA ", value), true);
  assert.equal(isKnownDevice(secret, "bob", value), false);
  assert.equal(isKnownDevice("t".repeat(64), "anna", value), false);
  assert.equal(isKnownDevice(secret, "anna", `${value}x`), false);
  assert.equal(isKnownDevice(secret, "anna", ""), false);
  assert.equal(isKnownDevice(secret, "anna", null), false);
  assert.equal(isKnownDevice(secret, "", knownDeviceValue(secret, "")), false);
});

test("pairing codes are limited per IPv6 /64", () => {
  const limiter = new PairingLimiter();
  const t = 1_000_000;
  for (let i = 1; i <= 5; i += 1) {
    assert.equal(limiter.attempt(`2001:db8:5:6::${i}`, t), 0);
  }
  assert.equal(limiter.attempt("2001:db8:5:6::abcd", t), 60_000);
  assert.equal(limiter.attempt("2001:db8:5:7::1", t), 0);
  for (let i = 1; i <= 10; i += 1) limiter.failed(`2001:db8:9:9::${i}`, t);
  assert.equal(limiter.attempt("2001:db8:9:9::42", t), 15 * 60_000);
});
