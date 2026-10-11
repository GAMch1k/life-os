import { useCallback, useRef, useState } from "react";

/**
 * A list the page changes at once (optimistically) and then reads back from
 * its service. `change` applies a local change; `write` runs a request, and
 * once no write is left in flight the list is read again in the background.
 * An answer that arrives after a newer local change, or while a write is
 * still running, is dropped, so a quick second tap never flickers back.
 */
export function useSyncedList<T>(
  initial: T[],
  read: () => Promise<{ data?: { items: T[] }; error?: string }>,
) {
  const [items, setItems] = useState(initial);
  const version = useRef(0);
  const inFlight = useRef(0);

  const reload = useCallback(() => {
    const seen = version.current;
    void read().then((result) => {
      if (!result.data || inFlight.current > 0 || seen !== version.current)
        return;
      setItems(result.data.items);
    });
  }, [read]);

  const change = useCallback((update: (items: T[]) => T[]) => {
    version.current++;
    setItems(update);
  }, []);

  const write = useCallback(
    async <R>(run: () => Promise<R>): Promise<R> => {
      version.current++;
      inFlight.current++;
      try {
        return await run();
      } finally {
        inFlight.current--;
        version.current++;
        if (inFlight.current === 0) reload();
      }
    },
    [reload],
  );

  return { items, change, write, reload };
}

export type SyncedList<T> = ReturnType<typeof useSyncedList<T>>;
