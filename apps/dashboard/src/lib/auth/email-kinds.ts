/**
 * The security alerts a person receives unasked, each a switch in Settings
 * → Email (docs/install/email.md#security-alerts). Nothing here reads the
 * environment, so the browser may import it too.
 */
export const ALERT_KINDS = [
  "new_device",
  "password",
  "two_factor",
  "credentials",
  "legacy_access",
  "sign_out",
] as const;
export type AlertKind = (typeof ALERT_KINDS)[number];

export const ALERT_LABELS: Record<
  AlertKind,
  { label: string; description: string }
> = {
  new_device: {
    label: "Sign-in from a new device",
    description:
      "Your password was used in a browser that never signed in to your account before, or a passkey signed in there.",
  },
  password: {
    label: "Password or email address changed",
    description:
      "Your password was changed or set with a reset link, or your email address changed (sent to the old address).",
  },
  two_factor: {
    label: "Two-factor sign-in changed",
    description:
      "The authenticator app or sign-in codes by email turned on or off, new recovery codes, a passkey added or removed.",
  },
  credentials: {
    label: "New API token, paired device, or connected app",
    description:
      "Something new can now act as you: an API token, a phone or watch, or an app connected over MCP.",
  },
  legacy_access: {
    label: "Legacy access turned on",
    description:
      "Legacy access was turned on on the server: whoever reaches Life OS without signing in acts as the owner.",
  },
  sign_out: {
    label: "Signed out everywhere",
    description:
      "Your account, or everyone on the server, was signed out everywhere.",
  },
};

export function isAlertKind(value: unknown): value is AlertKind {
  return typeof value === "string" && value in ALERT_LABELS;
}
