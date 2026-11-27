import assert from "node:assert/strict";
import test from "node:test";

import {
  EmailLimiter,
  isPlaceholderEmail,
  normalizeAddress,
} from "./email-account";
import { alertFor } from "./email-alerts";
import { ALERT_KINDS, ALERT_LABELS } from "./email-kinds";
import { EmailService } from "./email-service";
import {
  escapeHtml,
  passwordResetMessage,
  securityAlertMessage,
  serverName,
  signInCodeMessage,
  verifyEmailMessage,
} from "./email-templates";

const PUBLIC_URL = "https://lifeos.example.com";

test("an address: one, bare, lowercase, never a placeholder", () => {
  assert.equal(normalizeAddress(" You@Example.COM "), "you@example.com");
  for (const bad of [
    "",
    "you",
    "you@",
    "@example.com",
    "You <you@example.com>",
    "a@example.com, b@example.com",
    "you@example.com\r\nBcc: x@example.com",
    "u-abc@users.invalid",
    "you@example..com",
    42,
  ]) {
    assert.equal(normalizeAddress(bad), null, String(bad));
  }
  assert.equal(isPlaceholderEmail("u-0a1b@users.invalid"), true);
  assert.equal(isPlaceholderEmail("you@example.com"), false);
});

test("the limiter answers how long to wait, per key and window", () => {
  const limiter = new EmailLimiter();
  const windows = [
    { max: 1, ms: 30_000 },
    { max: 3, ms: 60 * 60_000 },
  ];
  const t0 = 1_000_000;
  assert.equal(limiter.take("a", windows, t0), 0);
  assert.equal(limiter.take("a", windows, t0 + 1_000), 29_000);
  assert.equal(limiter.take("b", windows, t0 + 1_000), 0, "another key");
  assert.equal(limiter.take("a", windows, t0 + 30_000), 0);
  assert.equal(limiter.take("a", windows, t0 + 60_000), 0);
  // Three this hour: the fourth waits for the first to leave the window.
  assert.equal(limiter.take("a", windows, t0 + 90_000), 60 * 60_000 - 90_000);
  limiter.clear();
  assert.equal(limiter.take("a", windows, t0 + 90_000), 0);
});

test("which security log entries alert, and under which switch", () => {
  assert.equal(alertFor({ kind: "password_changed" }), "password");
  assert.equal(
    alertFor({ kind: "password_changed", outcome: "failed" }),
    null,
    "a wrong current password changes nothing",
  );
  assert.equal(alertFor({ kind: "token_created" }), "credentials");
  assert.equal(alertFor({ kind: "device_paired" }), "credentials");
  assert.equal(alertFor({ kind: "oauth_consent_given" }), "credentials");
  assert.equal(alertFor({ kind: "two_factor_disabled" }), "two_factor");
  assert.equal(alertFor({ kind: "passkey_added" }), "two_factor");
  assert.equal(alertFor({ kind: "signed_out_everywhere" }), "sign_out");
  assert.equal(
    alertFor({ kind: "legacy_access_changed", detail: "on" }),
    "legacy_access",
  );
  assert.equal(
    alertFor({ kind: "legacy_access_changed", detail: "off" }),
    null,
  );
  for (const kind of [
    "sign_in",
    "sign_out",
    "token_revoked",
    "backup_started",
  ]) {
    assert.equal(alertFor({ kind }), null, kind);
  }
  for (const kind of ALERT_KINDS) assert.ok(ALERT_LABELS[kind].label, kind);
});

test("the emails: plain text and simple HTML, the server named, nothing that loads", () => {
  const messages = [
    verifyEmailMessage({
      publicUrl: PUBLIC_URL,
      name: "owner",
      link: `${PUBLIC_URL}/verify-email/tok`,
      hours: 24,
    }),
    passwordResetMessage({
      publicUrl: PUBLIC_URL,
      name: "owner",
      username: "owner",
      link: `${PUBLIC_URL}/reset/tok`,
      minutes: 30,
    }),
    signInCodeMessage({
      publicUrl: PUBLIC_URL,
      name: "owner",
      code: "123456",
      minutes: 10,
    }),
    securityAlertMessage({
      publicUrl: PUBLIC_URL,
      name: "owner",
      kind: "new_device",
      event: "sign_in",
      detail: "password",
      at: new Date("2026-11-27T09:00:00Z"),
      timeZone: "Europe/Berlin",
      address: "203.0.113.5",
      device: "Firefox on Linux",
    }),
  ];
  for (const message of messages) {
    assert.match(message.subject, / · Life OS$/);
    assert.match(
      message.text,
      /Life OS at lifeos\.example\.com \(https:\/\/lifeos\.example\.com\)/,
    );
    assert.match(message.html, /^<!doctype html>/);
    assert.doesNotMatch(
      message.html,
      /<img|<script|<link|\bsrc\s*=|\bon[a-z]+\s*=|url\(/i,
    );
  }
  assert.match(
    messages[0].text,
    /https:\/\/lifeos\.example\.com\/verify-email\/tok/,
  );
  assert.match(messages[1].text, /once, for 30 minutes/);
  assert.match(messages[2].subject, /123456/);
  const alert = messages[3];
  assert.match(alert.text, /When: 27 Nov 2026, 10:00 \(Europe\/Berlin\)/);
  assert.match(alert.text, /Device: Firefox on Linux/);
  assert.match(alert.text, /Network address: 203\.0\.113\.5/);
  assert.match(
    alert.text,
    /"Sign-in from a new device" is on in Settings → Email/,
  );
});

test("names cannot become markup", () => {
  assert.equal(
    escapeHtml(`<b onload="x">'&`),
    "&lt;b onload&#61;&quot;x&quot;&gt;&#39;&amp;",
  );
  const message = verifyEmailMessage({
    publicUrl: PUBLIC_URL,
    name: `x" onmouseover="steal()`,
    link: `${PUBLIC_URL}/verify-email/tok`,
    hours: 24,
  });
  assert.doesNotMatch(message.html, /\bon[a-z]+\s*=/i);
  assert.equal(serverName("not a url"), "Life OS");
});

test("the email service: cached status, and what a refusal means", async () => {
  let statusCalls = 0;
  const fetcher = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/v1/email/status")) {
      statusCalls += 1;
      return Response.json({ configured: true, state: "ok" });
    }
    const body = JSON.parse(String(init?.body)) as { to: string };
    if (body.to === "gone@example.com") {
      return Response.json(
        { error: { code: "email_recipient_refused", message: "refused" } },
        { status: 502 },
      );
    }
    if (body.to === "slow@example.com") {
      return Response.json(
        { error: { code: "email_connect_failed", message: "down" } },
        { status: 502 },
      );
    }
    return new Response(null, { status: 204 });
  }) as typeof fetch;
  const service = new EmailService("http://notifications.test", fetcher);
  assert.equal(service.known(), undefined);
  assert.equal(await service.enabled(), true);
  assert.equal(await service.enabled(), true);
  assert.equal(statusCalls, 1, "cached");
  assert.equal(service.known(), true);
  const message = { subject: "s", text: "t", kind: "test" };
  assert.deepEqual(await service.send({ ...message, to: "you@example.com" }), {
    ok: true,
  });
  const gone = await service.send({ ...message, to: "gone@example.com" });
  assert.equal(gone.ok, false);
  assert.equal(!gone.ok && gone.retry, false);
  const slow = await service.send({ ...message, to: "slow@example.com" });
  assert.equal(!slow.ok && slow.retry, true);

  const down = new EmailService("http://notifications.test", (async () => {
    throw new Error("ECONNREFUSED");
  }) as typeof fetch);
  assert.equal(await down.status(), null);
  assert.equal(await down.enabled(), false);
});
