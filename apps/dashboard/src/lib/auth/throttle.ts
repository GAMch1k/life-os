/**
 * The per-username delay after failed sign-ins
 * (docs/architecture/multi-user.md#browser-security): after FREE_FAILURES
 * failures for one username from one address, the next attempt from there
 * must wait, 15 s at first, doubling with each further failure, up to 15
 * minutes. It is per address so that guessing someone's password is slow
 * without letting a stranger lock that person out from elsewhere; the
 * gateway's and Better Auth's limits per address bound the rest. Kept in
 * memory: a restart of the Dashboard forgets it.
 */
const FREE_FAILURES = 5;
const FIRST_DELAY_MS = 15_000;
const MAX_DELAY_MS = 15 * 60_000;
const FORGET_AFTER_MS = 60 * 60_000;
const MAX_ENTRIES = 50_000;

type Entry = { failures: number; last: number };

export class LoginThrottle {
  private readonly entries = new Map<string, Entry>();

  private key(username: string, address: string) {
    return `${username.trim().toLowerCase()}\n${address}`;
  }

  /** Milliseconds until the next attempt is allowed; 0: now. */
  waitMs(username: string, address: string, now = Date.now()): number {
    const entry = this.entries.get(this.key(username, address));
    if (!entry || entry.failures < FREE_FAILURES) return 0;
    if (now - entry.last > FORGET_AFTER_MS) return 0;
    const delay = Math.min(
      MAX_DELAY_MS,
      FIRST_DELAY_MS * 2 ** (entry.failures - FREE_FAILURES),
    );
    return Math.max(0, entry.last + delay - now);
  }

  failed(username: string, address: string, now = Date.now()) {
    const key = this.key(username, address);
    const entry = this.entries.get(key);
    if (entry && now - entry.last <= FORGET_AFTER_MS) {
      entry.failures += 1;
      entry.last = now;
      return;
    }
    if (this.entries.size >= MAX_ENTRIES) this.prune(now);
    this.entries.set(key, { failures: 1, last: now });
  }

  succeeded(username: string, address: string) {
    this.entries.delete(this.key(username, address));
  }

  clear() {
    this.entries.clear();
  }

  private prune(now: number) {
    for (const [key, entry] of this.entries) {
      if (now - entry.last > FORGET_AFTER_MS) this.entries.delete(key);
    }
    if (this.entries.size >= MAX_ENTRIES) this.entries.clear();
  }
}
