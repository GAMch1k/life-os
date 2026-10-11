import type { Metadata, Viewport } from "next";

import { AppShell } from "@/components/app-shell";
import { ServiceWorker } from "@/components/service-worker";
import { ThemeProvider } from "@/components/theme-provider";
import { TimeZoneSync } from "@/components/time-zone-sync";
import { userTimeZone } from "@/lib/user-time-zone";
import { MANIFEST_PATH, THEME_COLORS } from "@/lib/web-app";

import "./globals.css";

export const metadata: Metadata = {
  title: "Life OS",
  description: "A local-first personal operating system.",
  applicationName: "Life OS",
};

// The browser's and the installed app's status bar follow the theme.
export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: THEME_COLORS.light },
    { media: "(prefers-color-scheme: dark)", color: THEME_COLORS.dark },
  ],
};

export default async function RootLayout({ children }: LayoutProps<"/">) {
  // Pages render in the browser's timezone once it has reported it; until
  // then (a first visit) in LIFE_OS_TIMEZONE, and TimeZoneSync refreshes.
  const timeZone = await userTimeZone();
  return (
    // next-themes sets the theme class and color-scheme on <html> before
    // hydration, so the server markup intentionally differs there.
    <html lang="en" className="h-full antialiased" suppressHydrationWarning>
      <head>
        {/* With the Access cookie: a manifest is fetched without cookies
            unless asked, and Cloudflare Access would refuse it. */}
        <link
          crossOrigin="use-credentials"
          href={MANIFEST_PATH}
          rel="manifest"
        />
      </head>
      <body className="min-h-full">
        <ThemeProvider>
          <AppShell timeZone={timeZone}>{children}</AppShell>
        </ThemeProvider>
        <TimeZoneSync timeZone={timeZone} />
        <ServiceWorker />
      </body>
    </html>
  );
}
