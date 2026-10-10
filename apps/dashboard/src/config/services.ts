const DEFAULT_SERVICE_URLS = {
  finance: "http://localhost:8081",
  tasks: "http://localhost:8082",
  inbox: "http://localhost:8083",
  calendar: "http://localhost:8084",
  voice: "http://localhost:8085",
  activity: "http://localhost:8086",
  notifications: "http://localhost:8087",
  banking: "http://localhost:8089",
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

function configuredServiceUrl(
  publicName: string,
  serverName: string,
  fallback: string,
): string {
  const value =
    (typeof window === "undefined" ? process.env[serverName] : undefined) ??
    process.env[publicName] ??
    fallback;
  return normalizeServiceUrl(publicName, value);
}

export const serviceBaseUrls = Object.freeze({
  finance: configuredServiceUrl(
    "NEXT_PUBLIC_FINANCE_SERVICE_URL",
    "FINANCE_SERVICE_URL",
    DEFAULT_SERVICE_URLS.finance,
  ),
  tasks: configuredServiceUrl(
    "NEXT_PUBLIC_TASKS_SERVICE_URL",
    "TASKS_SERVICE_URL",
    DEFAULT_SERVICE_URLS.tasks,
  ),
  inbox: configuredServiceUrl(
    "NEXT_PUBLIC_INBOX_SERVICE_URL",
    "INBOX_SERVICE_URL",
    DEFAULT_SERVICE_URLS.inbox,
  ),
  calendar: configuredServiceUrl(
    "NEXT_PUBLIC_CALENDAR_SERVICE_URL",
    "CALENDAR_SERVICE_URL",
    DEFAULT_SERVICE_URLS.calendar,
  ),
  voice: configuredServiceUrl(
    "NEXT_PUBLIC_VOICE_SERVICE_URL",
    "VOICE_SERVICE_URL",
    DEFAULT_SERVICE_URLS.voice,
  ),
  activity: configuredServiceUrl(
    "NEXT_PUBLIC_ACTIVITY_SERVICE_URL",
    "ACTIVITY_SERVICE_URL",
    DEFAULT_SERVICE_URLS.activity,
  ),
  notifications: configuredServiceUrl(
    "NEXT_PUBLIC_NOTIFICATIONS_SERVICE_URL",
    "NOTIFICATIONS_SERVICE_URL",
    DEFAULT_SERVICE_URLS.notifications,
  ),
  banking: configuredServiceUrl(
    "NEXT_PUBLIC_BANKING_SERVICE_URL",
    "BANKING_SERVICE_URL",
    DEFAULT_SERVICE_URLS.banking,
  ),
});

export type ServiceName = keyof typeof serviceBaseUrls;
