"use client";

import {
  Activity,
  CalendarDays,
  CheckSquare2,
  HeartPulse,
  Inbox,
  Landmark,
  LayoutDashboard,
  Sparkles,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { ThemeToggle } from "@/components/theme-toggle";
import { cn } from "@/lib/utils";

const navigation = [
  { href: "/", label: "Home", icon: LayoutDashboard },
  { href: "/finance", label: "Finance", icon: Landmark },
  { href: "/tasks", label: "Tasks", icon: CheckSquare2 },
  { href: "/inbox", label: "Inbox", icon: Inbox },
  { href: "/calendar", label: "Calendar", icon: CalendarDays },
  { href: "/activity", label: "Activity", icon: Activity },
  { href: "/status", label: "Status", icon: HeartPulse },
] as const;

function isCurrent(pathname: string, href: string) {
  return href === "/"
    ? pathname === "/"
    : pathname === href || pathname.startsWith(`${href}/`);
}

export function AppShell({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  const pathname = usePathname();
  return (
    <div className="bg-canvas min-h-dvh">
      <a
        className="bg-background focus:ring-ring sr-only z-50 rounded-md px-4 py-2 font-medium focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:ring-2"
        href="#main-content"
      >
        Skip to content
      </a>
      <header className="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky top-0 z-10 border-b backdrop-blur">
        <div className="mx-auto flex h-12 max-w-screen-2xl items-center justify-between gap-3 px-3 sm:px-4">
          <Link
            className="focus-visible:ring-ring flex items-center gap-2 rounded-md text-sm font-semibold tracking-tight outline-none focus-visible:ring-2"
            href="/"
          >
            <span className="bg-primary text-primary-foreground flex size-7 items-center justify-center rounded-lg">
              <Sparkles aria-hidden="true" className="size-3.5" />
            </span>
            Life OS
          </Link>
          <ThemeToggle />
        </div>
      </header>

      <div className="mx-auto grid max-w-screen-2xl md:grid-cols-[12rem_minmax(0,1fr)]">
        <aside className="bg-background sticky top-12 hidden h-[calc(100dvh-3rem)] self-start overflow-y-auto border-r p-2 md:block">
          <nav aria-label="Main navigation" className="space-y-0.5">
            {navigation.map(({ href, icon: Icon, label }) => {
              const current = isCurrent(pathname, href);
              return (
                <Link
                  aria-current={current ? "page" : undefined}
                  className={cn(
                    "focus-visible:ring-ring flex h-8 items-center gap-2.5 rounded-md px-2.5 text-sm font-medium transition-colors outline-none focus-visible:ring-2",
                    current
                      ? "bg-secondary text-secondary-foreground"
                      : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                  )}
                  href={href}
                  key={href}
                >
                  <Icon aria-hidden="true" className="size-4" />
                  {label}
                </Link>
              );
            })}
          </nav>
        </aside>

        <main
          className="min-w-0 px-3 pt-3 pb-[calc(4.5rem+env(safe-area-inset-bottom))] sm:px-4 sm:pt-4 md:p-5 lg:p-6"
          id="main-content"
        >
          {children}
        </main>
      </div>

      <nav
        aria-label="Mobile navigation"
        className="bg-background/95 supports-[backdrop-filter]:bg-background/85 fixed inset-x-0 bottom-0 z-20 border-t px-1 pt-1 pb-[calc(0.25rem+env(safe-area-inset-bottom))] backdrop-blur md:hidden"
      >
        <div className="mx-auto grid max-w-lg grid-cols-7 gap-0.5">
          {navigation.map(({ href, icon: Icon, label }) => {
            const current = isCurrent(pathname, href);
            return (
              <Link
                aria-current={current ? "page" : undefined}
                className={cn(
                  "focus-visible:ring-ring flex min-h-12 min-w-0 flex-col items-center justify-center gap-0.5 rounded-md text-[0.625rem] leading-tight font-medium transition-colors outline-none focus-visible:ring-2 focus-visible:ring-inset",
                  current
                    ? "bg-secondary text-secondary-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                )}
                href={href}
                key={href}
              >
                <Icon aria-hidden="true" className="size-5" />
                <span className="max-w-full truncate">{label}</span>
              </Link>
            );
          })}
        </div>
      </nav>
    </div>
  );
}
