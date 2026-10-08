import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/voice";

export function createVoiceClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.voice
    : "/api/voice",
) {
  return createClient<paths>({ baseUrl });
}

export const voiceClient = createVoiceClient();
