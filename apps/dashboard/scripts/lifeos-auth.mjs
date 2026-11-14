#!/usr/bin/env node
// Life OS's account commands on the host (docs/install/accounts.md):
// ./setup.sh runs them inside the Dashboard's container, which has auth_db's
// URL and the public address in its environment:
//
//   node scripts/lifeos-auth.mjs setup-code [--if-no-account]
//   node scripts/lifeos-auth.mjs has-account
//   node scripts/lifeos-auth.mjs recover-owner [--reset-2fa]
//   node scripts/lifeos-auth.mjs legacy-access [on|off]
//   node scripts/lifeos-auth.mjs sign-out-all [--keep-devices]
//
// Plain JavaScript and only the pg driver, so that it runs in the runtime
// image as it is. It never prints a password, hash, or key; it prints a setup
// code or a reset link only to the person at the host's terminal.
import { createHash, randomBytes } from "node:crypto";
import { pathToFileURL } from "node:url";

import pg from "pg";

const SETUP_CODE_TTL_HOURS = 24;
const RESET_TTL_HOURS = 24;
const LEGACY_ACCESS = "legacy_access";
const FIRST_USER_ID = "00000000-0000-4000-8000-000000000001";

/**
 * An entry in the security log (src/lib/auth/security-log.ts), for a
 * command run on the host: who ran it is the host, never an account. It
 * never stops the command (the log's table may not exist yet, before
 * auth-migrate ran): an emergency command must work all the same. Never
 * inside a transaction, so that a failed entry cannot undo the command.
 * @param {{ query: Function }} db
 * @param {string} kind
 * @param {{ outcome?: string, subject?: string | null, detail?: string | null }} [fields]
 */
export async function securityEvent(db, kind, fields = {}) {
  try {
    await db.query(
      `INSERT INTO security_events (kind, outcome, subject_user_id, via, detail)
       VALUES ($1, $2, $3, 'host', $4)`,
      [
        kind,
        fields.outcome ?? "ok",
        fields.subject ?? null,
        fields.detail ?? null,
      ],
    );
  } catch (error) {
    console.error(
      `lifeos-auth: the security log has no entry for this (${error.message})`,
    );
  }
}

function sha256Hex(value) {
  return createHash("sha256").update(value).digest("hex");
}

/** As src/lib/auth/store.ts: 4 groups of 4, no look-alike letters. */
export function newSetupCode() {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = randomBytes(16);
  let code = "";
  for (let i = 0; i < 16; i += 1) {
    if (i > 0 && i % 4 === 0) code += "-";
    code += alphabet[bytes[i] % alphabet.length];
  }
  return code;
}

function normalizeSetupCode(code) {
  return code.toUpperCase().replace(/[^A-Z0-9]/g, "");
}

export async function accountCount(db) {
  const { rows } = await db.query('SELECT count(*)::int AS n FROM "user"');
  return rows[0].n;
}

/** A new setup code, or null when an account exists already. */
export async function setupCode(db) {
  if ((await accountCount(db)) > 0) return null;
  const code = newSetupCode();
  await db.query("DELETE FROM setup_codes WHERE used_at IS NULL");
  await db.query(
    `INSERT INTO setup_codes (code_hash, expires_at)
     VALUES ($1, now() + make_interval(hours => $2))`,
    [sha256Hex(normalizeSetupCode(code)), SETUP_CODE_TTL_HOURS],
  );
  await securityEvent(db, "setup_code_created");
  return code;
}

/**
 * A one-time link to set the owner's password (Better Auth's reset token,
 * stored hashed as auth_db's verification storeIdentifier "hashed" keeps
 * it); with resetTwoFactor, the owner's TOTP goes too.
 */
export async function recoverOwner(
  db,
  publicUrl,
  { resetTwoFactor = false } = {},
) {
  const { rows } = await db.query(
    `SELECT id, "displayUsername" AS username FROM "user" WHERE role = 'owner'`,
  );
  const owner = rows[0];
  if (!owner) return null;
  const token = randomBytes(24).toString("base64url");
  const identifier = createHash("sha256")
    .update(`reset-password:${token}`)
    .digest("base64url");
  await db.query("BEGIN");
  try {
    await db.query(
      `INSERT INTO verification (identifier, value, "expiresAt")
       VALUES ($1, $2, now() + make_interval(hours => $3))`,
      [identifier, owner.id, RESET_TTL_HOURS],
    );
    if (resetTwoFactor) {
      await db.query('DELETE FROM "twoFactor" WHERE "userId" = $1', [owner.id]);
      await db.query(
        `UPDATE "user" SET "twoFactorEnabled" = false, "updatedAt" = now() WHERE id = $1`,
        [owner.id],
      );
    }
    await db.query("COMMIT");
  } catch (error) {
    await db.query("ROLLBACK");
    throw error;
  }
  await securityEvent(db, "owner_recovered", {
    subject: owner.id,
    detail: resetTwoFactor ? "reset_link_2fa" : "reset_link",
  });
  return {
    username: owner.username,
    link: `${publicUrl.replace(/\/+$/, "")}/reset/${token}`,
  };
}

export async function legacyAccess(db) {
  const { rows } = await db.query(
    "SELECT (value)::boolean AS value FROM server_settings WHERE key = $1",
    [LEGACY_ACCESS],
  );
  return rows.length ? rows[0].value === true : null;
}

/** Turns legacy access on or off; on is refused while more than one account exists. */
export async function setLegacyAccess(db, on) {
  if (on && (await accountCount(db)) > 1) {
    await securityEvent(db, "legacy_access_changed", {
      outcome: "refused",
      subject: FIRST_USER_ID,
      detail: "on:accounts",
    });
    return "accounts";
  }
  await securityEvent(db, "legacy_access_changed", {
    subject: FIRST_USER_ID,
    detail: on ? "on" : "off",
  });
  await db.query(
    `INSERT INTO server_settings (key, value, updated_at, updated_by)
     VALUES ($1, to_jsonb($2::boolean), now(), NULL)
     ON CONFLICT (key) DO UPDATE
       SET value = EXCLUDED.value, updated_at = now(), updated_by = NULL`,
    [LEGACY_ACCESS, on],
  );
  return "ok";
}

const DEVICE_KEY_PREFIX = "lo_dev_";

/**
 * Signs every account out, in one transaction (as "Sign out everywhere"
 * does for one, src/lib/auth/keys.ts revokeCredentials): every session,
 * device key (unless keepDevices), personal API token, MCP client's OAuth
 * tokens and consent, and pairing code goes, and every authz_version moves
 * on. The device keys' push subscriptions are deleted by the Dashboard
 * within a minute (device_key_cleanup). (Invites come with P21-07.)
 */
export async function signOutAll(db, { keepDevices = false } = {}) {
  await db.query("BEGIN");
  let sessions;
  let devices = 0;
  let tokens;
  let grants;
  try {
    if (!keepDevices) {
      await db.query(
        `INSERT INTO device_key_cleanup (key_id)
         SELECT id FROM apikey WHERE prefix = $1
         ON CONFLICT (key_id) DO NOTHING`,
        [DEVICE_KEY_PREFIX],
      );
      devices =
        (
          await db.query("DELETE FROM apikey WHERE prefix = $1", [
            DEVICE_KEY_PREFIX,
          ])
        ).rowCount ?? 0;
    }
    tokens = await db.query(
      "DELETE FROM apikey WHERE prefix IS DISTINCT FROM $1",
      [DEVICE_KEY_PREFIX],
    );
    await db.query('DELETE FROM "oauthAccessToken"');
    await db.query('DELETE FROM "oauthRefreshToken"');
    grants = await db.query('DELETE FROM "oauthConsent"');
    await db.query("DELETE FROM pairing_codes");
    sessions = await db.query("DELETE FROM session");
    await db.query(
      `UPDATE "user" SET "authzVersion" = "authzVersion" + 1, "updatedAt" = now()`,
    );
    await db.query("COMMIT");
  } catch (error) {
    await db.query("ROLLBACK");
    throw error;
  }
  await securityEvent(db, "signed_out_everyone", {
    detail: keepDevices ? "keep_devices" : null,
  });
  return {
    sessions: sessions.rowCount ?? 0,
    devices,
    tokens: tokens.rowCount ?? 0,
    grants: grants.rowCount ?? 0,
    keepDevices,
  };
}

function usage() {
  return `usage: lifeos-auth.mjs <command>
  setup-code [--if-no-account]   a one-time code for the first account
  has-account                    exit 0 when an account exists, 1 when none
  recover-owner [--reset-2fa]    a one-time link to set the owner's password
  legacy-access [on|off]         show or change legacy access
  sign-out-all [--keep-devices]  sign every account out`;
}

/**
 * Runs one command; resolves to the process's exit code.
 * @param {string[]} argv
 * @param {Record<string, string | undefined>} [env]
 * @param {{ log(text: string): void, error(text: string): void }} [out]
 */
export async function main(argv, env = process.env, out = console) {
  const [command, ...args] = argv;
  if (!command || command === "help" || command === "--help") {
    out.log(usage());
    return command ? 0 : 2;
  }
  const url = env.AUTH_DATABASE_URL;
  if (!url) {
    out.error(
      "AUTH_DATABASE_URL is not set: run this in the Dashboard's container",
    );
    return 2;
  }
  const publicUrl = (env.LIFE_OS_PUBLIC_URL ?? "").replace(/\/+$/, "");
  const db = new pg.Client({ connectionString: url });
  await db.connect();
  try {
    switch (command) {
      case "setup-code": {
        const code = await setupCode(db);
        if (!code) {
          if (!args.includes("--if-no-account")) {
            out.log("Life OS already has an account: no setup code is needed.");
          }
          return 0;
        }
        out.log(`Setup code: ${code}`);
        out.log(
          `Open ${publicUrl || "Life OS"}/setup and enter it to create the first account, which becomes the server's owner. It works once, for ${SETUP_CODE_TTL_HOURS} hours; ./setup.sh setup-code prints a new one.`,
        );
        return 0;
      }
      case "has-account":
        return (await accountCount(db)) > 0 ? 0 : 1;
      case "recover-owner": {
        const result = await recoverOwner(db, publicUrl, {
          resetTwoFactor: args.includes("--reset-2fa"),
        });
        if (!result) {
          out.error(
            "There is no owner account yet: create it with ./setup.sh setup-code.",
          );
          return 1;
        }
        out.log(
          `A one-time link to set a new password for ${result.username ?? "the owner"}:`,
        );
        out.log(`  ${result.link}`);
        out.log(
          `It works once, for ${RESET_TTL_HOURS} hours, and signs out every session of the account.`,
        );
        if (args.includes("--reset-2fa")) {
          out.log(
            "Two-factor sign-in (the authenticator app) is off for the owner now: turn it on again in Settings → Security.",
          );
        }
        return 0;
      }
      case "legacy-access": {
        const value = args[0];
        if (value === "on" || value === "off") {
          const result = await setLegacyAccess(db, value === "on");
          if (result === "accounts") {
            out.error(
              "Legacy access stays off while more than one account exists.",
            );
            return 1;
          }
          out.log(
            `Legacy access is ${value}. The Dashboard notices within 5 seconds.`,
          );
          return 0;
        }
        if (value !== undefined) {
          out.error(usage());
          return 2;
        }
        const current = await legacyAccess(db);
        out.log(
          current === null
            ? "Legacy access is not set yet: the Dashboard sets it from LIFE_OS_LEGACY_ACCESS when it first needs it."
            : `Legacy access is ${current ? "on" : "off"}.`,
        );
        return 0;
      }
      case "sign-out-all": {
        const result = await signOutAll(db, {
          keepDevices: args.includes("--keep-devices"),
        });
        out.log(
          `Signed out ${result.sessions} session(s) of every account, and revoked ${result.tokens} API token(s) and ${result.grants} connected app(s).`,
        );
        out.log(
          result.keepDevices
            ? "Paired phones and watches stay signed in (--keep-devices)."
            : `Signed out ${result.devices} paired phone(s) and watch(es): pair them again in Settings → Devices.`,
        );
        // Signing out means little while a request without a sign-in is
        // the owner anyway: say so.
        if ((await legacyAccess(db)) === true) {
          out.log(
            "Legacy access is on: anyone who reaches Life OS without signing in still acts as the owner. Turn it off with ./setup.sh legacy-access off (the phone and watch apps that are not paired lose access).",
          );
        } else {
          out.log("Legacy access is off: every request needs a sign-in.");
        }
        return 0;
      }
      default:
        out.error(usage());
        return 2;
    }
  } finally {
    await db.end();
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  main(process.argv.slice(2)).then(
    (code) => process.exit(code),
    (error) => {
      console.error(`lifeos-auth: ${error.message}`);
      process.exit(1);
    },
  );
}
