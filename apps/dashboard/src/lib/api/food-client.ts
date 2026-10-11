import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/food";

export function createFoodClient(
  baseUrl = typeof window === "undefined" ? serviceBaseUrls.food : "/api/food",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const foodClient = createFoodClient();
