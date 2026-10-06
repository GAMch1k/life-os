import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/inbox";

export function createInboxClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.inbox
    : "/api/inbox",
) {
  return createClient<paths>({ baseUrl });
}

export const inboxClient = createInboxClient();
