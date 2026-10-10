import { NotificationsView } from "@/components/notifications-view";
import { getNotificationsPage, type SearchParams } from "@/lib/notifications";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function NotificationsPage({
  searchParams,
}: {
  searchParams: Promise<SearchParams>;
}) {
  return (
    <NotificationsView
      page={await getNotificationsPage(
        await searchParams,
        await userTimeZone(),
      )}
    />
  );
}
