import type { MetadataRoute } from "next";

import { SHARE_ACTION, SHARE_FIELDS } from "@/lib/share";

// The installed Dashboard: the web app manifest that puts Life OS on the
// phone's home screen and into its share sheet. It is served by
// app/app.webmanifest/route.ts and linked from the root layout with
// crossorigin="use-credentials", so the browser sends the Cloudflare Access
// cookie with it (a manifest is fetched without cookies otherwise, and Access
// would answer with its login page).

/** Where the manifest is served. */
export const MANIFEST_PATH = "/app.webmanifest";

/** The service worker; at the root, so its scope is the whole app. */
export const SERVICE_WORKER_PATH = "/sw.js";

/** The page and status bar colours of the light and dark themes. */
export const THEME_COLORS = {
  light: "#ffffff", // --background, oklch(1 0 0)
  dark: "#0a0a0a", // --background in .dark, oklch(0.145 0 0)
} as const;

export function webAppManifest(): MetadataRoute.Manifest {
  return {
    id: "/",
    name: "Life OS",
    short_name: "Life OS",
    description:
      "Your personal operating system: capture, Tasks, Calendar, Finance, and Food.",
    lang: "en",
    start_url: "/",
    scope: "/",
    display: "standalone",
    background_color: THEME_COLORS.light,
    theme_color: THEME_COLORS.light,
    categories: ["productivity"],
    icons: [
      { src: "/icons/icon-192.png", sizes: "192x192", type: "image/png" },
      { src: "/icons/icon-512.png", sizes: "512x512", type: "image/png" },
      {
        src: "/icons/icon-maskable-192.png",
        sizes: "192x192",
        type: "image/png",
        purpose: "maskable",
      },
      {
        src: "/icons/icon-maskable-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      },
    ],
    // Share → Life OS: the share sheet posts what was shared to /share,
    // which saves it to Inbox as a capture.
    share_target: {
      action: SHARE_ACTION,
      method: "POST",
      enctype: "multipart/form-data",
      params: { ...SHARE_FIELDS },
    },
  };
}
