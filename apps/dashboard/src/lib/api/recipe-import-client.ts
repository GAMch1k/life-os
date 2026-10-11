import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/recipe-import";

/** The browser reaches the recipe-import integration under the integrations prefix. */
export const RECIPE_IMPORT_PROXY_PATH = "/api/integrations/recipe-import";

export function createRecipeImportClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.recipeImport
    : RECIPE_IMPORT_PROXY_PATH,
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const recipeImportClient = createRecipeImportClient();
