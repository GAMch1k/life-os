import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The recipe-import integration (`/api/integrations/recipe-import`, like the
 * gateway's route): previews of recipe links and pasted text, and saving a
 * recipe in Food. The recipes themselves are Food's, under `/api/food`.
 */
function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.recipeImport);
}

export const GET = proxy;
export const POST = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
