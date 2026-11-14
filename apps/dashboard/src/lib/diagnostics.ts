import { createActivityClient } from "@/lib/api/activity-client";
import type { components } from "@/lib/api/generated/activity";

/**
 * Server diagnostics (docs/architecture/multi-user.md#logs), for the
 * admins' Diagnostics page: each service's server errors by code, route,
 * and status, which the services send to Activity (pkg/diagnostics). Codes,
 * ids, counts, and times; never content. Read on the private network after
 * the page's own admin check.
 */

export type DiagnosticEvent = components["schemas"]["DiagnosticEvent"];
export type DiagnosticCount = components["schemas"]["DiagnosticCount"];
export type DiagnosticCode = components["schemas"]["DiagnosticCode"];

export type Diagnostics = {
  /** Counts by service and code, over the last day and the last week. */
  day: DiagnosticCount[] | null;
  week: DiagnosticCount[] | null;
  /** The newest rows; null when Activity could not be read. */
  recent: DiagnosticEvent[] | null;
};

/** What each code means, for the page. */
export const DIAGNOSTIC_CODES: Record<DiagnosticCode, string> = {
  internal_error: "Internal error",
  bad_gateway: "Bad answer from another service",
  service_unavailable: "Unavailable",
  gateway_timeout: "Timed out",
  server_error: "Server error",
  panic: "Crashed while answering (recovered)",
  not_ready: "Not ready",
  provider_unavailable: "Provider unavailable",
  media_unavailable: "Media unavailable",
  google_rejected: "Google refused",
  google_unavailable: "Google unavailable",
  google_not_configured: "Google not set up",
  banking_not_configured: "Bank sync not set up",
  food_unavailable: "Food unavailable",
  calendar_unavailable: "Calendar unavailable",
};

const TIMEOUT = 2500;

export async function getDiagnostics(): Promise<Diagnostics> {
  const client = createActivityClient();
  const summary = (hours: number) =>
    client
      .GET("/v1/diagnostic-events/summary", {
        params: { query: { hours } },
        signal: AbortSignal.timeout(TIMEOUT),
      })
      .then(({ data }) => data?.items ?? null)
      .catch(() => null);
  const [day, week, recent] = await Promise.all([
    summary(24),
    summary(24 * 7),
    client
      .GET("/v1/diagnostic-events", {
        params: { query: { limit: 50 } },
        signal: AbortSignal.timeout(TIMEOUT),
      })
      .then(({ data }) => data?.items ?? null)
      .catch(() => null),
  ]);
  return { day, week, recent };
}
