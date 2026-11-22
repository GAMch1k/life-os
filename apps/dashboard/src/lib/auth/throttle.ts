import { isIPv4, isIPv6 } from "node:net";

/**
 * The delays after failed password sign-ins
 * (docs/architecture/multi-user.md#browser-security,
 * docs/install/accounts.md#failed-sign-ins), two of them, in memory (a
 * restart of the Dashboard forgets both):
 *
 *  - Per username and address: after FREE_FAILURES failures for one
 *    username from one address, the next attempt from there waits, 15 s at
 *    first, doubling with each further failure, up to 15 minutes. An address
 *    is an IPv4 address or an IPv6 /64 (addressKey), the block one
 *    connection usually holds.
 *  - Per username, from every address together: after
 *    ACCOUNT_FREE_FAILURES failures within an hour, each further attempt
 *    waits, 1 minute at first, doubling to 15 minutes, so that guessing
 *    from many addresses is slow too. An attempt let through while it
 *    holds takes the account's turn at once, so a burst of them gets one
 *    through, not all. It never stops a browser that signed in to the
 *    account before (its known-device cookie, known-device.ts): a stranger
 *    who keeps failing slows the account down for everyone else, but cannot
 *    lock its person out. A password set with a reset link
 *    (./setup.sh recover-owner, an admin's) ends it, as a successful
 *    sign-in does, and passkeys never wait for it.
 *
 * Better Auth's own limit per address and the gateway's zone bound the rest.
 */
const FREE_FAILURES = 5;
const FIRST_DELAY_MS = 15_000;
const MAX_DELAY_MS = 15 * 60_000;
const FORGET_AFTER_MS = 60 * 60_000;
const MAX_ENTRIES = 50_000;

export const ACCOUNT_FREE_FAILURES = 10;
const ACCOUNT_FIRST_DELAY_MS = 60_000;
const ACCOUNT_MAX_DELAY_MS = 15 * 60_000;
const ACCOUNT_FORGET_AFTER_MS = 60 * 60_000;
const MAX_ACCOUNTS = 10_000;

type Entry = { failures: number; last: number };

/**
 * The key an address counts under: an IPv4 address as it is (also one an
 * IPv6 address maps, ::ffff:a.b.c.d), and an IPv6 address's /64, since one
 * connection usually has a whole /64 to pick addresses from. Anything else
 * (no address) as it is.
 */
export function addressKey(address: string): string {
  const value = address.trim().toLowerCase().replace(/%.*$/, "");
  if (isIPv4(value) || !isIPv6(value)) return value;
  const groups = ipv6Groups(value);
  const mapped =
    groups.slice(0, 5).every((group) => group === 0) && groups[5] === 0xffff;
  if (mapped) {
    return [groups[6] >> 8, groups[6] & 255, groups[7] >> 8, groups[7] & 255]
      .map(String)
      .join(".");
  }
  return `${groups
    .slice(0, 4)
    .map((group) => group.toString(16))
    .join(":")}::/64`;
}

/** The eight 16-bit groups of a valid IPv6 address. */
function ipv6Groups(value: string): number[] {
  let text = value;
  // A dotted IPv4 tail (::ffff:192.0.2.1) is the last two groups.
  const dotted = /(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(text);
  if (dotted) {
    const [a, b, c, d] = dotted.slice(1).map(Number);
    text = `${text.slice(0, dotted.index)}${((a << 8) | b).toString(16)}:${((c << 8) | d).toString(16)}`;
  }
  const [head, tail] = text.split("::");
  const left = head ? head.split(":") : [];
  const right = tail ? tail.split(":") : [];
  const zeros =
    tail === undefined ? [] : Array(8 - left.length - right.length).fill("0");
  return [...left, ...zeros, ...right].map((group) => parseInt(group, 16));
}

function usernameKey(username: string) {
  return username.trim().toLowerCase();
}

function delay(entry: Entry, free: number, first: number, max: number) {
  return Math.min(max, first * 2 ** (entry.failures - free));
}

export class LoginThrottle {
  private readonly entries = new Map<string, Entry>();
  private readonly accounts = new Map<string, Entry>();

  private key(username: string, address: string) {
    return `${usernameKey(username)}\n${addressKey(address)}`;
  }

  /** Milliseconds until the next attempt from this address is allowed; 0: now. */
  waitMs(username: string, address: string, now = Date.now()): number {
    const entry = this.entries.get(this.key(username, address));
    if (!entry || entry.failures < FREE_FAILURES) return 0;
    if (now - entry.last > FORGET_AFTER_MS) return 0;
    const wait = delay(entry, FREE_FAILURES, FIRST_DELAY_MS, MAX_DELAY_MS);
    return Math.max(0, entry.last + wait - now);
  }

  /** Milliseconds until the account takes another attempt from anywhere; 0: now. */
  accountWaitMs(username: string, now = Date.now()): number {
    const entry = this.accounts.get(usernameKey(username));
    if (!entry || entry.failures < ACCOUNT_FREE_FAILURES) return 0;
    if (now - entry.last > ACCOUNT_FORGET_AFTER_MS) return 0;
    const wait = delay(
      entry,
      ACCOUNT_FREE_FAILURES,
      ACCOUNT_FIRST_DELAY_MS,
      ACCOUNT_MAX_DELAY_MS,
    );
    return Math.max(0, entry.last + wait - now);
  }

  /**
   * Whether an attempt may go on: milliseconds to wait (0: go on), and
   * whether the account's delay is what holds it. knownDevice: the request
   * carries this account's known-device cookie, which the account's delay
   * never holds back. An attempt let through while the account's delay
   * holds takes its turn: the next one waits again.
   */
  attempt(
    username: string,
    address: string,
    options: { knownDevice?: boolean } = {},
    now = Date.now(),
  ): { waitMs: number; account: boolean } {
    const own = this.waitMs(username, address, now);
    if (own > 0) return { waitMs: own, account: false };
    if (options.knownDevice) return { waitMs: 0, account: false };
    const account = this.accountWaitMs(username, now);
    if (account > 0) return { waitMs: account, account: true };
    const entry = this.accounts.get(usernameKey(username));
    if (
      entry &&
      entry.failures >= ACCOUNT_FREE_FAILURES &&
      now - entry.last <= ACCOUNT_FORGET_AFTER_MS
    ) {
      entry.last = now;
    }
    return { waitMs: 0, account: false };
  }

  /**
   * A wrong password. Returns true when this failure starts the account's
   * delay (its ACCOUNT_FREE_FAILURES-th within the hour): the moment the
   * security log notes.
   */
  failed(username: string, address: string, now = Date.now()): boolean {
    this.count(this.entries, this.key(username, address), now, {
      forgetAfter: FORGET_AFTER_MS,
      max: MAX_ENTRIES,
    });
    const failures = this.count(this.accounts, usernameKey(username), now, {
      forgetAfter: ACCOUNT_FORGET_AFTER_MS,
      max: MAX_ACCOUNTS,
    });
    return failures === ACCOUNT_FREE_FAILURES;
  }

  /** A right password: both delays for this username end. */
  succeeded(username: string, address: string) {
    this.entries.delete(this.key(username, address));
    this.accounts.delete(usernameKey(username));
  }

  /** The account's delay ends (a password set with a reset link). */
  forgetAccount(username: string) {
    this.accounts.delete(usernameKey(username));
  }

  clear() {
    this.entries.clear();
    this.accounts.clear();
  }

  private count(
    map: Map<string, Entry>,
    key: string,
    now: number,
    limits: { forgetAfter: number; max: number },
  ): number {
    const entry = map.get(key);
    if (entry && now - entry.last <= limits.forgetAfter) {
      entry.failures += 1;
      entry.last = now;
      return entry.failures;
    }
    if (map.size >= limits.max) {
      for (const [old, value] of map) {
        if (now - value.last > limits.forgetAfter) map.delete(old);
      }
      if (map.size >= limits.max) map.clear();
    }
    map.set(key, { failures: 1, last: now });
    return 1;
  }
}
