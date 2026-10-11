// Share → Life OS: what the installed Dashboard receives from the phone's
// share sheet (the manifest's share_target posts it to /share) and how it
// becomes an ordinary Inbox capture. Nothing here touches the network; the
// route handler (app/share/route.ts) and the pages use it.

/** Where the share sheet posts (the manifest's share_target action). */
export const SHARE_ACTION = "/share";

/** The form fields of a share, as the manifest names them. */
export const SHARE_FIELDS = {
  title: "title",
  text: "text",
  url: "url",
} as const;

export type SharedFields = {
  title?: string | null;
  text?: string | null;
  url?: string | null;
};

/**
 * The capture text of a share: its title, text, and URL, one per line, each
 * once. Apps fill the fields differently: a browser sends the page title and
 * its address (in url, or inside text), Instagram only text with the link in
 * it, a notes app only text. A part already contained in the text is left
 * out, so the link stays in the text exactly once. Empty when nothing was
 * shared.
 */
export function shareContent({ title, text, url }: SharedFields) {
  const shared = {
    title: title?.trim() ?? "",
    text: text?.trim() ?? "",
    url: url?.trim() ?? "",
  };
  const parts: string[] = [];
  if (
    shared.title &&
    !shared.text.includes(shared.title) &&
    shared.title !== shared.url
  ) {
    parts.push(shared.title);
  }
  if (shared.text) parts.push(shared.text);
  if (shared.url && !shared.text.includes(shared.url)) parts.push(shared.url);
  return parts.join("\n");
}

/**
 * The capture's source metadata: marked as shared, and the browser's
 * timezone (the `lifeos-tz` cookie) when it is known, so "tomorrow" in a
 * shared text means the owner's tomorrow.
 */
export function shareMetadata(timeZone?: string) {
  return {
    shared: true,
    via: "share-target",
    ...(timeZone ? { timeZone } : {}),
  };
}

/**
 * Whether a POST to /share may come from the share sheet rather than from
 * another site. The share sheet's post is a navigation of the installed app
 * to its own origin: Sec-Fetch-Site is `none` (the browser started it) or
 * `same-origin`, and Origin is the app's own or `null`. A form on another
 * site (`cross-site`, or a sibling subdomain, `same-site`) is refused, even
 * when the browser would send the Access cookie with it. Older browsers
 * without Sec-Fetch-Site are judged by Origin alone; the host is compared
 * without the port, which the gateway does not pass on.
 */
export function isShareFromApp(headers: Headers) {
  const site = headers.get("sec-fetch-site");
  if (site && site !== "same-origin" && site !== "none") return false;
  const origin = headers.get("origin");
  if (!origin || origin === "null") return true;
  const host = (headers.get("x-forwarded-host") ?? headers.get("host") ?? "")
    .split(",")[0]
    .trim()
    .replace(/:\d+$/, "")
    .toLowerCase();
  try {
    return new URL(origin).hostname.toLowerCase() === host;
  } catch {
    return false;
  }
}

/** Why a share was not saved, as /share/saved?error= names it. */
export type ShareError = "empty" | "unreadable" | "inbox";

export const SHARE_ERRORS: Record<ShareError, string> = {
  empty: "Nothing was shared: share some text, a link, or a photo.",
  unreadable: "Life OS could not read what was shared. Share it again.",
  inbox:
    "Inbox could not take it right now, so nothing was saved. Share it again in a moment.",
};

export function shareError(value: string | string[] | undefined) {
  const code = Array.isArray(value) ? value[0] : value;
  return code && Object.hasOwn(SHARE_ERRORS, code)
    ? (code as ShareError)
    : undefined;
}
