import { useState } from "react";

/**
 * A dialog's state, kept after it becomes null so the dialog's content
 * stays rendered while it animates closed.
 */
export function useClosingState<T>(state: T | null) {
  const [shown, setShown] = useState(state);
  if (state !== null && state !== shown) setShown(state);
  return state ?? shown;
}
