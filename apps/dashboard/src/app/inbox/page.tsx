import { InboxSummary } from "@/components/inbox-summary";
import { getInboxSummary } from "@/lib/inbox-summary";
export const dynamic = "force-dynamic";
export default async function InboxPage() {
  return <InboxSummary state={await getInboxSummary()} />;
}
