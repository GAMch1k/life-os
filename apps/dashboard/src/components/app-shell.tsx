"use client";

import {
  Activity,
  CalendarDays,
  CheckSquare2,
  CircleUserRound,
  Inbox,
  Landmark,
  LayoutDashboard,
  Sparkles,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const navigation = [
  { href: "/", label: "Overview", icon: LayoutDashboard },
  { href: "/finance", label: "Finance", icon: Landmark },
  { href: "/tasks", label: "Tasks", icon: CheckSquare2 },
  { href: "/inbox", label: "Inbox", icon: Inbox },
  { href: "/calendar", label: "Calendar", icon: CalendarDays },
  { href: "/activity", label: "Activity", icon: Activity },
] as const;

export function AppShell({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  const pathname = usePathname();
  return (
    <div className="bg-muted/40 min-h-screen">
      <a
        className="bg-background focus:ring-ring sr-only z-50 rounded-md px-4 py-2 font-medium focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:ring-2"
        href="#main-content"
      >
        Skip to content
      </a>
      <header className="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky top-0 z-10 border-b backdrop-blur">
        <div className="mx-auto flex h-16 max-w-screen-2xl items-center justify-between px-4 sm:px-6">
          <div className="flex items-center gap-3">
            <div className="bg-primary text-primary-foreground flex size-9 items-center justify-center rounded-xl shadow-sm">
              <Sparkles aria-hidden="true" className="size-4" />
            </div>
            <div>
              <p className="font-semibold tracking-tight">Life OS</p>
              <p className="text-muted-foreground text-xs">
                Personal workspace
              </p>
            </div>
          </div>

          <Button
            aria-label="Account controls are not available yet"
            disabled
            size="icon"
            variant="ghost"
          >
            <CircleUserRound aria-hidden="true" />
          </Button>
        </div>
      </header>

      <div className="mx-auto grid max-w-screen-2xl md:grid-cols-[15rem_minmax(0,1fr)]">
        <aside className="bg-background hidden min-h-[calc(100vh-4rem)] border-r p-4 md:block">
          <nav aria-label="Main navigation" className="space-y-1">
            {navigation.map(({ href, icon: Icon, label }) => (
              <Link
                className={cn(
                  "focus-visible:ring-ring flex h-10 items-center gap-3 rounded-lg px-3 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none",
                  pathname === href
                    ? "bg-secondary text-secondary-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                )}
                href={href}
                key={label}
              >
                <Icon aria-hidden="true" className="size-4" />
                <span>{label}</span>
              </Link>
            ))}
          </nav>
        </aside>

        <main
          className="min-w-0 p-4 pb-28 sm:p-6 sm:pb-28 md:pb-8 lg:p-10"
          id="main-content"
        >
          {children}
        </main>
      </div>

      <nav
        aria-label="Mobile navigation"
        className="bg-background/95 supports-[backdrop-filter]:bg-background/85 fixed inset-x-0 bottom-0 z-20 border-t px-2 pt-2 pb-[calc(0.5rem+env(safe-area-inset-bottom))] backdrop-blur md:hidden"
      >
        <div className="mx-auto grid max-w-lg grid-cols-6">
          {navigation.map(({ href, icon: Icon, label }) => (
            <Link
              className={cn(
                "focus-visible:ring-ring flex min-h-14 flex-col items-center justify-center gap-1 rounded-lg px-1 text-[0.6875rem] font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none",
                pathname === href
                  ? "text-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
              )}
              href={href}
              key={label}
            >
              <Icon aria-hidden="true" className="size-5" />
              <span>{label}</span>
            </Link>
          ))}
        </div>
      </nav>
    </div>
  );
}
