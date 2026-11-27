import type { components } from "@/lib/api/generated/notifications";

/**
 * Email as the Dashboard sees it (docs/install/email.md): the notifications
 * service sends, through the owner's SMTP provider, and says whether it can
 * (GET /v1/email/status). The Dashboard asks it on the private network,
 * without an identity token (POST /v1/email/messages takes no other
 * caller). Without NOTIFICATIONS_SERVICE_URL, or with email off there,
 * every email option of the Dashboard is hidden.
 */

export type EmailStatus = components["schemas"]["EmailStatus"];

export type EmailMessage = {
  to: string;
  subject: string;
  text: string;
  html?: string;
  /** What it is, for the log: verify_email, password_reset, … */
  kind: string;
};

export type EmailResult =
  | { ok: true }
  | {
      ok: false;
      /** email_auth_failed, email_recipient_refused, rate_limited, … */
      code: string;
      message: string;
      /** Worth trying again later: not a refused recipient or off. */
      retry: boolean;
    };

const ACTOR = { "x-life-os-actor": "system:dashboard" };

export class EmailService {
  private cached: { status: EmailStatus | null; at: number } | null = null;

  constructor(
    private readonly baseUrl: string,
    private readonly fetcher: typeof fetch = fetch,
    private readonly ttlMs = 15_000,
  ) {}

  /**
   * Email's status, cached for a few seconds; null when the notifications
   * service does not answer.
   */
  async status(options: { fresh?: boolean } = {}): Promise<EmailStatus | null> {
    const now = Date.now();
    if (!options.fresh && this.cached && now - this.cached.at < this.ttlMs) {
      return this.cached.status;
    }
    let status: EmailStatus | null = null;
    try {
      const response = await this.fetcher(`${this.baseUrl}/v1/email/status`, {
        headers: ACTOR,
        cache: "no-store",
        signal: AbortSignal.timeout(3000),
      });
      if (response.ok) status = (await response.json()) as EmailStatus;
    } catch {
      status = null;
    }
    this.cached = { status, at: now };
    return status;
  }

  /** Whether email can be sent now (as far as the cache knows). */
  async enabled(): Promise<boolean> {
    return (await this.status())?.configured === true;
  }

  /** The last status read, without asking: undefined before the first. */
  known(): boolean | undefined {
    if (!this.cached || Date.now() - this.cached.at > 5 * 60_000) {
      return undefined;
    }
    return this.cached.status?.configured === true;
  }

  async send(message: EmailMessage): Promise<EmailResult> {
    let response: Response;
    try {
      response = await this.fetcher(`${this.baseUrl}/v1/email/messages`, {
        method: "POST",
        headers: { ...ACTOR, "content-type": "application/json" },
        body: JSON.stringify(message),
        signal: AbortSignal.timeout(45_000),
      });
    } catch {
      return {
        ok: false,
        code: "notifications_unavailable",
        message: "The notifications service does not answer.",
        retry: true,
      };
    }
    if (response.status === 204) return { ok: true };
    let error: { code?: string; message?: string } = {};
    try {
      error = ((await response.json()) as { error?: typeof error }).error ?? {};
    } catch {
      error = {};
    }
    const code = error.code ?? `http_${response.status}`;
    if (code === "email_not_configured") this.cached = null;
    return {
      ok: false,
      code,
      message: error.message ?? "The email was not sent.",
      retry:
        code !== "email_recipient_refused" &&
        code !== "email_not_configured" &&
        code !== "bad_request",
    };
  }
}

/** The email service, or null without NOTIFICATIONS_SERVICE_URL. */
export function emailService(
  baseUrl: string | undefined,
  fetcher?: typeof fetch,
): EmailService | null {
  const base = baseUrl?.trim().replace(/\/+$/, "");
  if (!base) return null;
  return new EmailService(base, fetcher ?? fetch);
}
