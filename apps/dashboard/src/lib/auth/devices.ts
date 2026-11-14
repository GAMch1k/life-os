/**
 * Short labels for the Security page's sessions list, such as "Chrome on
 * Android", from a User-Agent header. A guess for people, never a check.
 */
export function describeUserAgent(userAgent: string | null | undefined) {
  const ua = userAgent ?? "";
  if (!ua) return "Unknown device";
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /OPR\/|Opera/.test(ua)
      ? "Opera"
      : /SamsungBrowser\//.test(ua)
        ? "Samsung Internet"
        : /Firefox\//.test(ua)
          ? "Firefox"
          : /Chrome\/|CriOS\//.test(ua)
            ? "Chrome"
            : /Safari\//.test(ua)
              ? "Safari"
              : /okhttp|Dalvik/i.test(ua)
                ? "Android app"
                : /curl\//.test(ua)
                  ? "curl"
                  : "Browser";
  const system = /Android/.test(ua)
    ? "Android"
    : /iPhone|iPad|iPod/.test(ua)
      ? "iOS"
      : /Mac OS X|Macintosh/.test(ua)
        ? "macOS"
        : /Windows/.test(ua)
          ? "Windows"
          : /CrOS/.test(ua)
            ? "ChromeOS"
            : /Linux/.test(ua)
              ? "Linux"
              : "";
  return system ? `${browser} on ${system}` : browser;
}

/**
 * The otpauth:// URI an authenticator app reads, named after the username:
 * Better Auth names the account by its email, which is a placeholder under
 * .invalid for accounts without one.
 */
export function totpUriForUsername(uri: string, username: string) {
  try {
    const url = new URL(uri);
    const issuer = url.searchParams.get("issuer") ?? "Life OS";
    url.pathname = `//${encodeURIComponent(issuer)}:${encodeURIComponent(username)}`;
    return url.toString().replace(/^otpauth:\/\/totp\/+/, "otpauth://totp/");
  } catch {
    return uri;
  }
}

/** The secret of an otpauth:// URI, in groups of four for typing. */
export function totpSecret(uri: string) {
  try {
    const secret = new URL(uri).searchParams.get("secret") ?? "";
    return secret.replace(/(.{4})(?=.)/g, "$1 ");
  } catch {
    return "";
  }
}
