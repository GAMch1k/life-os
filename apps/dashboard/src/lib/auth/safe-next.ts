/**
 * A `next` page to go back to after signing in: on Life OS, a path only,
 * never the account pages themselves. Anything else is Home.
 */
export function safeNext(value: string | string[] | undefined): string {
  const next = Array.isArray(value) ? value[0] : value;
  if (!next || !next.startsWith("/") || next.startsWith("//")) return "/";
  if (next.startsWith("/\\")) return "/";
  if (/^\/(login|setup|reset)(\/|\?|$)/.test(next)) return "/";
  return next;
}
