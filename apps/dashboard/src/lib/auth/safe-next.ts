const BASE = "http://lifeos.invalid";

/**
 * A `next` page to go back to after signing in: a path on Life OS itself,
 * never another site and never the account pages. Anything else is Home.
 *
 * Browsers drop tabs and newlines from URLs and read a backslash as a slash,
 * so "/\t/evil.example" or "/\\evil.example" would become
 * "//evil.example", another site: control characters and backslashes are
 * refused outright, and what is left must resolve on Life OS's own origin.
 * The result is the parsed path, query, and fragment, never the raw text.
 */
export function safeNext(value: string | string[] | undefined): string {
  const next = Array.isArray(value) ? value[0] : value;
  if (!next || !next.startsWith("/")) return "/";
  if (/[\u0000-\u001f\u007f\\]/.test(next)) return "/";
  let url: URL;
  try {
    url = new URL(next, BASE);
  } catch {
    return "/";
  }
  if (url.origin !== BASE) return "/";
  const path = `${url.pathname}${url.search}${url.hash}`;
  if (!path.startsWith("/") || path.startsWith("//")) return "/";
  if (/^\/(login|setup|reset)(\/|$)/.test(url.pathname)) return "/";
  return path;
}
