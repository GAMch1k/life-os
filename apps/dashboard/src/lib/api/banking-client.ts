import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/banking";

/** The browser reaches banking under the integrations prefix. */
export const BANKING_PROXY_PATH = "/api/integrations/banking";

export function createBankingClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.banking
    : BANKING_PROXY_PATH,
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const bankingClient = createBankingClient();
