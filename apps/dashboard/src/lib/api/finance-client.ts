import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/finance";

export function createFinanceClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.finance
    : "/api/finance",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const financeClient = createFinanceClient();
