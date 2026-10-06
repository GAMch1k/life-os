import createClient from "openapi-fetch";

import type { paths } from "@/lib/api/generated/template";

export function createTemplateClient(baseUrl: string) {
  return createClient<paths>({ baseUrl });
}

export async function fetchTemplatePing(baseUrl: string) {
  const { data, error } = await createTemplateClient(baseUrl).GET("/v1/ping");

  if (error || !data) {
    throw new Error("Template service ping failed");
  }

  return data.message;
}
