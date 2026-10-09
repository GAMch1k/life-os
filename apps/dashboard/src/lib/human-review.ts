import { inboxClient } from "@/lib/api/inbox-client";
import type { components } from "@/lib/api/generated/inbox";

export type HumanReviewItem = components["schemas"]["InboxItem"];

export async function getHumanReviewItems(): Promise<HumanReviewItem[]> {
  const { data, error } = await inboxClient.GET("/v1/inbox-items", {
    params: { query: { state: "human_review", limit: 50 } },
  });
  return error || !data ? [] : data.items;
}
