import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/finance";

export function createFinanceClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.finance
    : "/api/finance",
) {
  return createClient<paths>({ baseUrl });
}

export const financeClient = createFinanceClient();
