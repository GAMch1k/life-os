import { ActivityLogView } from "@/components/activity-log";
import { getActivityLog, type SearchParams } from "@/lib/activity-log";
import { userTimeZone } from "@/lib/time-zone";

export const dynamic = "force-dynamic";

export default async function ActivityPage({
  searchParams,
}: {
  searchParams: Promise<SearchParams>;
}) {
  return (
    <ActivityLogView
      log={await getActivityLog(await searchParams, userTimeZone())}
    />
  );
}
