import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/backup";

/**
 * The browser reaches the backup service only through the Dashboard: it has
 * no gateway route, and the Dashboard passes on reading its status and
 * starting a run (src/app/api/backups).
 */
export const BACKUP_PROXY_PATH = "/api/backups";

export function createBackupClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.backup
    : BACKUP_PROXY_PATH,
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const backupClient = createBackupClient();
