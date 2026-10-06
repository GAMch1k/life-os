import { inboxClient } from "@/lib/api/inbox-client";
import type { components } from "@/lib/api/generated/inbox";

export type InboxItem = components["schemas"]["InboxItem"];

export type InboxSummaryState =
  | { kind: "ready"; items: InboxItem[] }
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "unavailable" };

export async function getInboxSummary(): Promise<InboxSummaryState> {
  const { data, error } = await inboxClient.GET("/v1/inbox-items", {
    params: { query: { unresolved: true, limit: 10 } },
  });

  if (error || !data) {
    return { kind: "unavailable" };
  }
  if (data.items.length === 0) {
    return { kind: "empty" };
  }
  return { kind: "ready", items: data.items };
}
