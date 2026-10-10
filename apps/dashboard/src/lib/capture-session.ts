import type { InboxItem } from "@/lib/inbox-summary";

// Text captures made on the current page, by the capture card's composer or
// by editing a Human Review capture, are announced here. The capture card
// lists and follows every one of them under its composer, with its live
// state, so a capture made elsewhere on the page shows its outcome the same
// way.

type Listener = (item: InboxItem) => void;

const listeners = new Set<Listener>();

/** Calls `listener` with every capture announced; returns the unsubscribe. */
export function onCapture(listener: Listener) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Tells the page about a capture Inbox just accepted. */
export function announceCapture(item: InboxItem) {
  for (const listener of listeners) listener(item);
}
