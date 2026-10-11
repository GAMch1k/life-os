import { FoodWorkspace } from "@/components/food-workspace";
import { getFoodWorkspace, parseFoodTab } from "@/lib/food";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function FoodPage({ searchParams }: PageProps<"/food">) {
  // The server decides today once, in the owner's timezone, and the client
  // workspace renders from these props, so hydration matches the server
  // HTML. The key starts the workspace over when TimeZoneSync re-renders
  // the page in the browser's zone. `?tab=pantry` opens the pantry, as
  // Home's expiry line links, and `?store=<id>` filters the list.
  const params = await searchParams;
  const timeZone = await userTimeZone();
  return (
    <FoodWorkspace
      initialStore={typeof params.store === "string" ? params.store : undefined}
      initialTab={parseFoodTab(params.tab)}
      key={timeZone}
      state={await getFoodWorkspace()}
      timeZone={timeZone}
      today={dayKey(new Date(), timeZone)}
    />
  );
}
