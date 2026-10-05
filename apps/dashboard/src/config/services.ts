const DEFAULT_SERVICE_URLS = {
  finance: "http://localhost:8081",
  tasks: "http://localhost:8082",
  inbox: "http://localhost:8083",
  calendar: "http://localhost:8084",
} as const;

export function normalizeServiceUrl(name: string, value: string): string {
  let url: URL;

  try {
    url = new URL(value);
  } catch {
    throw new Error(`${name} must be an absolute HTTP or HTTPS URL`);
  }

  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`${name} must use HTTP or HTTPS`);
  }
  if (url.username || url.password) {
    throw new Error(`${name} must not contain credentials`);
  }
  if (url.search || url.hash) {
    throw new Error(`${name} must not contain a query string or fragment`);
  }

  return url.toString().replace(/\/$/, "");
}

export const serviceBaseUrls = Object.freeze({
  finance: normalizeServiceUrl(
    "NEXT_PUBLIC_FINANCE_SERVICE_URL",
    process.env.NEXT_PUBLIC_FINANCE_SERVICE_URL ?? DEFAULT_SERVICE_URLS.finance,
  ),
  tasks: normalizeServiceUrl(
    "NEXT_PUBLIC_TASKS_SERVICE_URL",
    process.env.NEXT_PUBLIC_TASKS_SERVICE_URL ?? DEFAULT_SERVICE_URLS.tasks,
  ),
  inbox: normalizeServiceUrl(
    "NEXT_PUBLIC_INBOX_SERVICE_URL",
    process.env.NEXT_PUBLIC_INBOX_SERVICE_URL ?? DEFAULT_SERVICE_URLS.inbox,
  ),
  calendar: normalizeServiceUrl(
    "NEXT_PUBLIC_CALENDAR_SERVICE_URL",
    process.env.NEXT_PUBLIC_CALENDAR_SERVICE_URL ??
      DEFAULT_SERVICE_URLS.calendar,
  ),
});

export type ServiceName = keyof typeof serviceBaseUrls;
