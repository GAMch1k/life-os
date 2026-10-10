"use client";

import { ThemeProvider as NextThemesProvider } from "next-themes";

/**
 * Light, dark, or the system preference, stored in localStorage ("theme").
 * next-themes sets the `dark` class on <html> before the first paint, so there
 * is no flash of the wrong theme on load.
 */
export function ThemeProvider({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="system"
      disableTransitionOnChange
      enableSystem
    >
      {children}
    </NextThemesProvider>
  );
}
