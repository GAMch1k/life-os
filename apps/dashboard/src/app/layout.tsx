import type { Metadata } from "next";

import { AppShell } from "@/components/app-shell";
import { ThemeProvider } from "@/components/theme-provider";
import { TimeZoneSync } from "@/components/time-zone-sync";
import { userTimeZone } from "@/lib/user-time-zone";

import "./globals.css";

export const metadata: Metadata = {
  title: "Life OS",
  description: "A local-first personal operating system.",
};

export default async function RootLayout({ children }: LayoutProps<"/">) {
  // Pages render in the browser's timezone once it has reported it; until
  // then (a first visit) in LIFE_OS_TIMEZONE, and TimeZoneSync refreshes.
  const timeZone = await userTimeZone();
  return (
    // next-themes sets the theme class and color-scheme on <html> before
    // hydration, so the server markup intentionally differs there.
    <html lang="en" className="h-full antialiased" suppressHydrationWarning>
      <body className="min-h-full">
        <ThemeProvider>
          <AppShell timeZone={timeZone}>{children}</AppShell>
        </ThemeProvider>
        <TimeZoneSync timeZone={timeZone} />
      </body>
    </html>
  );
}
