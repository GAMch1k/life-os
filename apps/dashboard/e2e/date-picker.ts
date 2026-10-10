import { expect, type Locator, type Page } from "./fixtures";

// The Dashboard's date fields are shadcn date pickers
// (src/components/date-picker.tsx), not native date inputs: a button named
// "<label>, <the day it shows>" carrying the day in `data-value`, which opens
// a calendar popover (portalled to the page body, so it is found from the
// page, not from a dialog) with month and year dropdowns.

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** The date picker button named `label`, such as "Start date". */
export function datePicker(scope: Page | Locator, label: string) {
  return scope.getByRole("button", {
    name: new RegExp(`^${escape(label)}, `),
  });
}

/**
 * Picks `day` (YYYY-MM-DD) in the date picker named `label`: opens it, moves
 * its calendar to the day's month with the year and month dropdowns, clicks
 * the day, and waits until the popover has closed and the picker holds it.
 */
export async function pickDate(
  scope: Page | Locator,
  label: string,
  day: string,
) {
  const page = "page" in scope ? scope.page() : scope;
  const trigger = datePicker(scope, label);
  // Picking the chosen day again would clear an optional picker.
  if ((await trigger.getAttribute("data-value")) === day) return;
  await trigger.click();
  const popover = page.locator(
    `[data-slot="popover-content"][aria-label="${label}"]`,
  );
  const [year, month] = day.split("-").map(Number);
  await popover
    .locator('select[aria-label="Choose the Year"]')
    .selectOption(String(year));
  await popover
    .locator('select[aria-label="Choose the Month"]')
    .selectOption(String(month - 1));
  await popover
    .locator(`td[data-day="${day}"]:not([data-outside]) button`)
    .click();
  // Until the popover is gone a dialog locator would match it too.
  await expect(popover).toHaveCount(0);
  await expect(trigger).toHaveAttribute("data-value", day);
}
