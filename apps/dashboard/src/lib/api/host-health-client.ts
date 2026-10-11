import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/host-health";

/**
 * host-health is read on the server only, for the Status page's Server
 * check: it has no gateway route and no writes.
 */
export const hostHealthClient = createClient<paths>({
  baseUrl: serviceBaseUrls.hostHealth,
});
