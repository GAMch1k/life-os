import { useSyncExternalStore } from "react";

import { DATE_LOCALE } from "@/lib/time-zone";

const noSubscription = () => () => {};

/**
 * The browser's language, for text formatted the owner's way (a date
 * picker's day, an amount of money). The server render and the hydration
 * pass use DATE_LOCALE, so the page hydrates without a mismatch and then
 * shows the browser's format.
 */
export function useBrowserLocale() {
  return useSyncExternalStore(
    noSubscription,
    () => navigator.language || DATE_LOCALE,
    () => DATE_LOCALE,
  );
}
