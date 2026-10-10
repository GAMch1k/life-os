"use client";

// Date and time fields, composed as shadcn's Base UI Date Picker docs show
// (https://ui.shadcn.com/docs/components/base/date-picker): a Popover holding
// the Calendar, opened by an outline Button that shows the day. shadcn has
// no time picker; its "Time Picker" example pairs the date picker with a
// time Input, which TimeInput is. Everything under components/ui is the
// shadcn CLI's own output; Life OS's choices live here.

import { ChevronDownIcon } from "lucide-react";
import { type ComponentProps, useState, useSyncExternalStore } from "react";
import { enGB } from "react-day-picker/locale/en-GB";

import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  dateToDay,
  dayToDate,
  pickerLabel,
  pickerRange,
} from "@/lib/date-picker";
import { DATE_LOCALE } from "@/lib/time-zone";
import { cn } from "@/lib/utils";

const noSubscription = () => () => {};

/**
 * The browser's language for the button's date. The server render (and the
 * hydration pass) uses DATE_LOCALE, so a server-rendered picker hydrates
 * without a mismatch and then shows the browser's format.
 */
function useBrowserLocale() {
  return useSyncExternalStore(
    noSubscription,
    () => navigator.language || DATE_LOCALE,
    () => DATE_LOCALE,
  );
}

export type DatePickerProps = {
  /**
   * The accessible name, such as "Start date". The button is announced
   * with the day it shows: "Start date, Wed, 21 Oct 2026".
   */
  label: string;
  /** The chosen day (YYYY-MM-DD); "" when none. Pair with onChange. */
  value?: string;
  /** The first day when uncontrolled (a form field with `name`). */
  defaultValue?: string;
  onChange?: (day: string) => void;
  /** Submits the day ("" when none) under this name, as a date input did. */
  name?: string;
  /** The earliest day that can be picked (YYYY-MM-DD). */
  min?: string;
  /** The latest day that can be picked (YYYY-MM-DD). */
  max?: string;
  /**
   * A day is always chosen. Otherwise picking the chosen day again clears
   * the picker.
   */
  required?: boolean;
  /**
   * The owner's today (YYYY-MM-DD): marked in the calendar, and the month
   * it opens on when no day is chosen. The browser's today by default.
   */
  today?: string;
  placeholder?: string;
  id?: string;
  className?: string;
  disabled?: boolean;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
};

/**
 * A date field: an outline button showing the day in the browser's format,
 * opening a month calendar (weeks start on Monday, as on the Tasks page)
 * with month and year dropdowns. Picking a day closes it. It works with the
 * keyboard (the calendar grid takes arrow keys; Escape closes) and by
 * touch, and holds plain YYYY-MM-DD days, never instants.
 */
export function DatePicker({
  label,
  value: controlled,
  defaultValue = "",
  onChange,
  name,
  min,
  max,
  required = false,
  today,
  placeholder = "Pick a date",
  id,
  className,
  disabled,
  ...aria
}: DatePickerProps) {
  const [open, setOpen] = useState(false);
  const [uncontrolled, setUncontrolled] = useState(defaultValue);
  // Only the calendar uses it, and that renders in the browser.
  const [browserToday] = useState(() => dateToDay(new Date()));
  const value = controlled ?? uncontrolled;
  const locale = useBrowserLocale();
  const selected = dayToDate(value);
  const shown = selected ? pickerLabel(value, locale) : placeholder;
  const todayDay = today && dayToDate(today) ? today : browserToday;
  const todayDate = dayToDate(todayDay);
  const range = pickerRange({ max, min, today: todayDay, value });
  const before = dayToDate(min);
  const after = dayToDate(max);

  const choose = (date: Date | undefined) => {
    setOpen(false);
    // A required picker keeps its day when the chosen day is picked again.
    if (!date && required) return;
    const day = date ? dateToDay(date) : "";
    if (controlled === undefined) setUncontrolled(day);
    onChange?.(day);
  };

  return (
    <>
      <Popover onOpenChange={setOpen} open={open}>
        <PopoverTrigger
          render={
            <Button
              aria-label={`${label}, ${shown}`}
              className={cn(
                "data-[empty=true]:text-muted-foreground min-w-0 justify-between font-normal",
                className,
              )}
              data-empty={!selected}
              data-value={value}
              disabled={disabled}
              id={id}
              variant="outline"
              {...aria}
            />
          }
        >
          <span className="truncate">{shown}</span>
          <ChevronDownIcon data-icon="inline-end" />
        </PopoverTrigger>
        <PopoverContent
          align="start"
          aria-label={label}
          className="w-auto overflow-hidden p-0"
        >
          <Calendar
            captionLayout="dropdown"
            // Larger days on a phone, for fingers.
            className="[--cell-size:--spacing(9)] sm:[--cell-size:--spacing(8)]"
            defaultMonth={selected ?? todayDate}
            disabled={[
              ...(before ? [{ before }] : []),
              ...(after ? [{ after }] : []),
            ]}
            endMonth={dayToDate(range.end)}
            locale={enGB}
            mode="single"
            onSelect={choose}
            selected={selected}
            startMonth={dayToDate(range.start)}
            today={todayDate}
            weekStartsOn={1}
          />
        </PopoverContent>
      </Popover>
      {name ? <input name={name} type="hidden" value={value} /> : null}
    </>
  );
}

/**
 * A time of day (HH:mm) field, as in shadcn's date and time picker example:
 * a time Input with the browser's clock indicator hidden. Phones still open
 * their own time wheel.
 */
export function TimeInput({
  className,
  ...props
}: Omit<ComponentProps<typeof Input>, "type">) {
  return (
    <Input
      className={cn(
        "bg-background appearance-none [&::-webkit-calendar-picker-indicator]:hidden [&::-webkit-calendar-picker-indicator]:appearance-none",
        className,
      )}
      type="time"
      {...props}
    />
  );
}
