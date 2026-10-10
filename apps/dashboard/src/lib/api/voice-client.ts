import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/voice";

export function createVoiceClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.voice
    : "/api/voice",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const voiceClient = createVoiceClient();
