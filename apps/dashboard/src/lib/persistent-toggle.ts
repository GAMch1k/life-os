import { useCallback, useSyncExternalStore } from "react";

// On/off UI state (an expanded section) remembered in localStorage per key.
// The server, and the browser while it hydrates, render the default; the
// stored value applies right after hydration, so the HTML always matches.
// A value set on this page is also kept in memory, so toggling works when
// storage is unavailable.

const listeners = new Set<() => void>();
const memory = new Map<string, boolean>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** A stored "true"/"false", or null when absent, invalid, or unreadable. */
function parseToggle(value: string | null | undefined) {
  return value === "true" ? true : value === "false" ? false : null;
}

function stored(key: string) {
  if (memory.has(key)) return memory.get(key)!;
  try {
    return parseToggle(window.localStorage.getItem(key));
  } catch {
    return null;
  }
}

export function usePersistentToggle(key: string, initial = false) {
  const value = useSyncExternalStore(
    subscribe,
    () => stored(key) ?? initial,
    () => initial,
  );
  const set = useCallback(
    (next: boolean) => {
      memory.set(key, next);
      try {
        window.localStorage.setItem(key, String(next));
      } catch {
        // Private mode or a full quota: the in-memory value still applies.
      }
      for (const listener of listeners) listener();
    },
    [key],
  );
  return [value, set] as const;
}
