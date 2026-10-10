import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/inbox";

export function createInboxClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.inbox
    : "/api/inbox",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const inboxClient = createInboxClient();
