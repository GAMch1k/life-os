import assert from "node:assert/strict";
import test from "node:test";

import { describeUserAgent, totpSecret, totpUriForUsername } from "./devices";

const uri =
  "otpauth://totp/Life%20OS:u-abc%40users.invalid?secret=JBSWY3DPEHPK3PXP&issuer=Life%20OS&digits=6&period=30";

test("the authenticator app shows the username, not the placeholder email", () => {
  const named = totpUriForUsername(uri, "anna");
  assert.match(named, /^otpauth:\/\/totp\/Life%20OS:anna\?/);
  const url = new URL(named);
  assert.equal(url.searchParams.get("secret"), "JBSWY3DPEHPK3PXP");
  assert.equal(url.searchParams.get("issuer"), "Life OS");
  assert.equal(totpUriForUsername("not a uri", "anna"), "not a uri");
});

test("the secret is shown in groups of four", () => {
  assert.equal(totpSecret(uri), "JBSW Y3DP EHPK 3PXP");
});

test("sessions are labelled by browser and system", () => {
  assert.equal(
    describeUserAgent(
      "Mozilla/5.0 (Linux; Android 15; SM-S921B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Mobile Safari/537.36",
    ),
    "Chrome on Android",
  );
  assert.equal(
    describeUserAgent(
      "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
    ),
    "Firefox on Linux",
  );
  assert.equal(
    describeUserAgent(
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
    ),
    "Safari on macOS",
  );
  assert.equal(describeUserAgent(null), "Unknown device");
});
