"use client";

// Life OS's dialog: shadcn's Dialog exactly as the shadcn CLI writes it
// (components/ui/dialog.tsx, never edited by hand) with the app's defaults
// layered on through className. Import dialogs from here.

import type { ComponentProps } from "react";

import {
  Dialog,
  DialogContent as ShadcnDialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader as ShadcnDialogHeader,
  DialogTitle as ShadcnDialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

/**
 * The dialog box, centred over a dimmed page on the page's own background.
 * It never grows past the screen: a tall dialog (a form on a phone) scrolls
 * inside itself.
 */
function DialogContent({
  className,
  ...props
}: ComponentProps<typeof ShadcnDialogContent>) {
  return (
    <ShadcnDialogContent
      className={cn(
        "bg-background max-h-[calc(100dvh-2rem)] overflow-y-auto overscroll-contain sm:max-w-md",
        className,
      )}
      {...props}
    />
  );
}

/** Keeps a long title clear of the close button in the corner. */
function DialogHeader({
  className,
  ...props
}: ComponentProps<typeof ShadcnDialogHeader>) {
  return <ShadcnDialogHeader className={cn("pr-8", className)} {...props} />;
}

/** A title that wraps (an event's can be long) keeps readable lines. */
function DialogTitle({
  className,
  ...props
}: ComponentProps<typeof ShadcnDialogTitle>) {
  return (
    <ShadcnDialogTitle className={cn("leading-snug", className)} {...props} />
  );
}

export {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
};
