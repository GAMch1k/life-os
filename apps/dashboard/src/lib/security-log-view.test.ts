import assert from "node:assert/strict";
import test from "node:test";

import {
  clientAddress,
  detailCode,
  deviceLabel,
  SECURITY_KINDS,
} from "./auth/security-log";
import {
  parseSecurityFilters,
  securityActor,
  securityDetail,
  securityHref,
  securityRange,
  securityTitle,
} from "./security-log-view";

test("every kind has a title", () => {
  for (const kind of SECURITY_KINDS) {
    assert.notEqual(securityTitle({ kind }), kind.replace(/_/g, " "));
  }
  assert.equal(securityTitle({ kind: "something_new" }), "something new");
});

test("details read as words", () => {
  const event = (kind: string, detail: string | null, count = 1) =>
    securityDetail({ kind, detail, count });
  assert.equal(event("sign_in", "passkey"), "with a passkey");
  assert.equal(
    event("sign_in_failed", "password:invalid_username_or_password"),
    "with a password: wrong username or password",
  );
  assert.equal(
    event("sign_in_failed", "totp:invalid_code", 3),
    "with an authenticator code: wrong code · 3 times",
  );
  assert.equal(
    event("sign_in_failed", "other:too_many_entries", 40),
    "more failed sign-ins from this address, counted together · 40 times",
  );
  assert.equal(event("role_changed", "admin"), "to admin");
  assert.equal(event("legacy_access_changed", "off"), "turned off");
  assert.equal(
    event("legacy_access_changed", "on:host_only"),
    "turning it on refused: only on the server",
  );
  assert.equal(event("legacy_access_used", null, 1234), "1,234 requests");
  assert.equal(
    event("admin_refused", "backup:second_factor_required"),
    "Back up now: needs two-factor sign-in",
  );
  assert.equal(event("owner_recovered", "reset_link"), "");
});

test("who did it", () => {
  assert.equal(securityActor({ actor: null, via: "host" }), "On the server");
  assert.equal(
    securityActor({ actor: { id: "x", name: "Owner" }, via: "legacy" }),
    "Without signing in",
  );
  assert.equal(
    securityActor({ actor: { id: "x", name: null }, via: "session" }),
    "A deleted account",
  );
  assert.equal(
    securityActor({ actor: null, via: null }),
    "Someone not signed in",
  );
});

test("the entry's fields keep their form, never free text", () => {
  const headers = new Headers({
    "x-real-ip": "203.0.113.9, 10.0.0.1",
    "user-agent":
      "Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 Chrome/141.0 Mobile Safari/537.36",
  });
  assert.equal(clientAddress(headers), "203.0.113.9");
  assert.equal(deviceLabel(headers), "Chrome on Android");
  assert.equal(clientAddress(new Headers({ "x-real-ip": "Buy milk" })), null);
  assert.equal(detailCode("SETUP_CODE_INVALID"), "setup_code_invalid");
  assert.equal(detailCode("Buy milk & 12.34 EUR"), "buy_milk_12.34_eur");
  assert.equal(detailCode(""), null);
});

test("the admin view's filters", () => {
  const filters = parseSecurityFilters({
    user: "00000000-0000-4000-8000-000000000001",
    kind: "sign_in_failed",
    fromDate: "2026-11-01",
    toDate: "2026-11-14",
    junk: "x",
  });
  assert.deepEqual(filters, {
    user: "00000000-0000-4000-8000-000000000001",
    kind: "sign_in_failed",
    fromDate: "2026-11-01",
    toDate: "2026-11-14",
  });
  assert.deepEqual(parseSecurityFilters({ user: "alice", kind: "nope" }), {});
  const range = securityRange(filters, "Europe/Berlin");
  assert.equal(range.from?.toISOString(), "2026-10-31T23:00:00.000Z");
  assert.equal(range.to?.toISOString(), "2026-11-14T23:00:00.000Z");
  assert.equal(
    securityHref("/admin/security-log", filters, { cursor: "abc" }),
    "/admin/security-log?user=00000000-0000-4000-8000-000000000001&kind=sign_in_failed&fromDate=2026-11-01&toDate=2026-11-14&cursor=abc",
  );
});
