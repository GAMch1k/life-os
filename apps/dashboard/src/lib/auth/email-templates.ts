import { dateTimeFormat, resolveTimeZone } from "../time-zone";
import { ALERT_LABELS, type AlertKind } from "./email-kinds";

/**
 * The account emails' words (docs/install/email.md): plain text, and the
 * same as simple HTML (inline styles, links, nothing that loads or runs: no
 * images, no tracking). English, with the server's address in each, so the
 * person knows which Life OS wrote.
 */

export type RenderedEmail = { subject: string; text: string; html: string };

type Block =
  | string
  | { link: string; label: string }
  | { code: string }
  | { facts: [string, string][] };

/**
 * Text as HTML. "=" is escaped too, so that no name can look like an
 * attribute to the notifications service's check, which refuses HTML with
 * event handlers or src (an entity reads the same in a link).
 */
export function escapeHtml(value: string) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;")
    .replace(/=/g, "&#61;");
}

/** The server's name as the emails say it: "Life OS at lifeos.example.com". */
export function serverName(publicUrl: string) {
  try {
    return `Life OS at ${new URL(publicUrl).host}`;
  } catch {
    return "Life OS";
  }
}

function render(
  publicUrl: string,
  subject: string,
  greeting: string,
  blocks: Block[],
  footer: string,
): RenderedEmail {
  const name = serverName(publicUrl);
  const text: string[] = [greeting, ""];
  const html: string[] = [
    `<p style="margin:0 0 16px">${escapeHtml(greeting)}</p>`,
  ];
  for (const block of blocks) {
    if (typeof block === "string") {
      text.push(block, "");
      html.push(`<p style="margin:0 0 16px">${escapeHtml(block)}</p>`);
    } else if ("link" in block) {
      text.push(block.link, "");
      html.push(
        `<p style="margin:0 0 16px"><a href="${escapeHtml(block.link)}" style="color:#1d4ed8">${escapeHtml(block.label)}</a></p>`,
      );
    } else if ("code" in block) {
      text.push(`    ${block.code}`, "");
      html.push(
        `<p style="margin:0 0 16px;font-family:monospace;font-size:24px;letter-spacing:4px"><strong>${escapeHtml(block.code)}</strong></p>`,
      );
    } else {
      for (const [label, value] of block.facts) text.push(`${label}: ${value}`);
      text.push("");
      html.push(
        `<table style="margin:0 0 16px;border-collapse:collapse">${block.facts
          .map(
            ([label, value]) =>
              `<tr><td style="padding:2px 12px 2px 0;color:#555">${escapeHtml(label)}</td><td style="padding:2px 0">${escapeHtml(value)}</td></tr>`,
          )
          .join("")}</table>`,
      );
    }
  }
  text.push("--", `${name} (${publicUrl})`, footer);
  html.push(
    `<hr style="border:none;border-top:1px solid #ddd;margin:24px 0 12px">`,
    `<p style="margin:0;color:#555;font-size:13px">${escapeHtml(name)} (<a href="${escapeHtml(publicUrl)}" style="color:#555">${escapeHtml(publicUrl)}</a>)<br>${escapeHtml(footer)}</p>`,
  );
  return {
    subject: `${subject} · Life OS`,
    text: `${text.join("\n")}\n`,
    html: `<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;line-height:1.5;color:#111;max-width:560px;margin:0 auto;padding:16px">${html.join("")}</body></html>`,
  };
}

function hello(name: string | null | undefined) {
  return name ? `Hello ${name},` : "Hello,";
}

const ASKED =
  "You get this email because it was asked for on that Life OS. If it was not you, you can ignore it.";

export function verifyEmailMessage(input: {
  publicUrl: string;
  name: string | null;
  link: string;
  hours: number;
}): RenderedEmail {
  return render(
    input.publicUrl,
    "Verify your email address",
    hello(input.name),
    [
      "Open this link to verify the email address of your Life OS account. Then Life OS can send you a link when you forget your password, sign-in codes, and security alerts.",
      { link: input.link, label: "Verify my email address" },
      `The link works once, for ${input.hours} hours. If it has run out, send a new one in Settings → Email.`,
    ],
    ASKED,
  );
}

export function passwordResetMessage(input: {
  publicUrl: string;
  name: string | null;
  username: string | null;
  link: string;
  minutes: number;
}): RenderedEmail {
  return render(
    input.publicUrl,
    "Set a new password",
    hello(input.name),
    [
      `Someone asked for a link to set a new password for your Life OS account${input.username ? ` (${input.username})` : ""}. Open it to choose one:`,
      { link: input.link, label: "Set a new password" },
      `The link works once, for ${input.minutes} minutes. Setting the password signs out every session of your account, and its paired phones and watches, API tokens, and connected apps.`,
      "If you did not ask for it, ignore this email: your password stays as it is.",
    ],
    ASKED,
  );
}

export function signInCodeMessage(input: {
  publicUrl: string;
  name: string | null;
  code: string;
  minutes: number;
}): RenderedEmail {
  return render(
    input.publicUrl,
    `Your sign-in code: ${input.code}`,
    hello(input.name),
    [
      "Your password was right. Enter this code to finish signing in:",
      { code: input.code },
      `It works once, for ${input.minutes} minutes. Never tell it to anyone: Life OS never asks for it by phone or email.`,
      "If you are not signing in now, someone knows your password: change it in Settings → Security.",
    ],
    ASKED,
  );
}

/** What happened, in words, for an alert: its subject and first lines. */
function alertWords(
  event: string,
  detail: string | null,
): { subject: string; lines: string[] } {
  switch (event) {
    case "sign_in":
      return {
        subject: "New sign-in from a new device",
        lines: [
          detail === "passkey"
            ? "Your account was signed in to with a passkey in a browser that had not signed in to it before."
            : "Your password was just used to sign in to your account in a browser that had not signed in to it before. If two-factor sign-in is on, the second step was still needed.",
        ],
      };
    case "password_changed":
      return {
        subject: "Your password was changed",
        lines: ["The password of your account was changed."],
      };
    case "password_reset":
      return {
        subject: "Your password was set with a reset link",
        lines: [
          "A new password was set for your account with a reset link. Every session, paired phone and watch, API token, and connected app of your account was signed out.",
        ],
      };
    case "email_changed":
      return {
        subject: "Your email address was changed",
        lines: [
          "The email address of your account was changed. Life OS's emails go to the new address once it is verified; this one gets no more.",
        ],
      };
    case "two_factor_enabled":
      return detail === "email_code"
        ? {
            subject: "Sign-in codes by email turned on",
            lines: [
              "Sign-in codes by email were turned on for your account: after your password, Life OS can email you a code.",
            ],
          }
        : {
            subject: "Two-factor sign-in turned on",
            lines: [
              "Two-factor sign-in with an authenticator app was turned on for your account.",
            ],
          };
    case "two_factor_disabled":
      return detail === "email_code"
        ? {
            subject: "Sign-in codes by email turned off",
            lines: ["Sign-in codes by email were turned off for your account."],
          }
        : {
            subject: "Two-factor sign-in turned off",
            lines: [
              "Two-factor sign-in with an authenticator app was turned off for your account.",
            ],
          };
    case "recovery_codes_created":
      return {
        subject: "New recovery codes",
        lines: [
          "New recovery codes were made for your account. The old ones no longer work.",
        ],
      };
    case "passkey_added":
      return {
        subject: "Passkey added",
        lines: ["A passkey was added to your account."],
      };
    case "passkey_removed":
      return {
        subject: "Passkey removed",
        lines: ["A passkey was removed from your account."],
      };
    case "token_created":
      return {
        subject: "New API token",
        lines: [
          "An API token was made for your account. It acts as you, within its scopes, until it expires or is revoked.",
        ],
      };
    case "device_paired":
      return {
        subject: "New paired device",
        lines: [
          detail === "watch"
            ? "A watch was paired with your account."
            : "A phone was paired with your account.",
        ],
      };
    case "oauth_consent_given":
      return {
        subject: "New connected app",
        lines: [
          "An app was connected to your account over MCP. It acts as you until it is disconnected.",
        ],
      };
    case "legacy_access_changed":
      return {
        subject: "Legacy access turned on",
        lines: [
          "Legacy access was turned on on the server (./setup.sh legacy-access on): whoever reaches Life OS without signing in acts as the owner.",
        ],
      };
    case "signed_out_everywhere":
      return {
        subject: "Signed out everywhere",
        lines: [
          "Your account was signed out everywhere: every session, paired phone and watch, API token, and connected app.",
        ],
      };
    case "signed_out_everyone":
      return {
        subject: "Everyone was signed out",
        lines: [
          "Every account of this Life OS was signed out on the server (./setup.sh sign-out-all), yours too. Sign in again.",
        ],
      };
  }
  return {
    subject: "Security alert",
    lines: ["Something changed in your account's sign-in."],
  };
}

export function securityAlertMessage(input: {
  publicUrl: string;
  name: string | null;
  kind: AlertKind;
  event: string;
  detail: string | null;
  at: Date;
  timeZone?: string;
  address: string | null;
  device: string | null;
}): RenderedEmail {
  const timeZone = resolveTimeZone(input.timeZone);
  const when = `${dateTimeFormat(timeZone, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(input.at)} (${timeZone})`;
  const words = alertWords(input.event, input.detail);
  const facts: [string, string][] = [["When", when]];
  if (input.device) facts.push(["Device", input.device]);
  if (input.address) facts.push(["Network address", input.address]);
  const settings = `${input.publicUrl}/settings/security`;
  return render(
    input.publicUrl,
    words.subject,
    hello(input.name),
    [
      ...words.lines,
      { facts },
      "If it was you, there is nothing to do. If it was not, change your password and sign out everywhere in Settings → Security, and look at Settings → Security log:",
      { link: settings, label: "Open Settings → Security" },
    ],
    `You get this email because "${ALERT_LABELS[input.kind].label}" is on in Settings → Email (${input.publicUrl}/settings/email).`,
  );
}
