import { FoodWorkspace } from "@/components/food-workspace";
import { getFoodWorkspace, parseFoodTab } from "@/lib/food";
import {
  fetchRecipes,
  fetchWeek,
  parseWeek,
  weekStart,
} from "@/lib/food-recipes";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function FoodPage({ searchParams }: PageProps<"/food">) {
  // The server decides today once, in the owner's timezone, and the client
  // workspace renders from these props, so hydration matches the server
  // HTML. The key starts the workspace over when TimeZoneSync re-renders
  // the page in the browser's zone. `?tab=pantry` opens the pantry, as
  // Home's expiry line links, `?store=<id>` filters the list, and
  // `?tab=plan&week=<a day>` opens the meal plan on that day's week.
  const params = await searchParams;
  const timeZone = await userTimeZone();
  const today = dayKey(new Date(), timeZone);
  const week = parseWeek(params.week) ?? weekStart(today);
  const [state, recipes, plan] = await Promise.all([
    getFoodWorkspace(),
    fetchRecipes(),
    fetchWeek(week),
  ]);
  return (
    <FoodWorkspace
      initialStore={typeof params.store === "string" ? params.store : undefined}
      initialTab={parseFoodTab(params.tab)}
      initialWeek={week}
      key={timeZone}
      plan={plan}
      recipes={recipes}
      state={state}
      timeZone={timeZone}
      today={today}
    />
  );
}
