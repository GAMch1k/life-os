"use client";

import { LogIn, UserRound } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import type { AccountInfo } from "@/lib/auth/account";

/**
 * The header's way to the account: Settings → Security when signed in;
 * "Sign in" under legacy access, once an account exists.
 */
export function AccountButton({ account }: { account?: AccountInfo }) {
  const pathname = usePathname();
  if (!account) return null;
  if (account.via === "session") {
    return (
      <Link
        aria-label="Account and security"
        className={buttonVariants({ size: "icon", variant: "ghost" })}
        href="/settings/security"
        title="Account and security"
      >
        <UserRound aria-hidden="true" />
      </Link>
    );
  }
  if (!account.accountExists) return null;
  return (
    <Link
      className={buttonVariants({ size: "sm", variant: "ghost" })}
      href={`/login?next=${encodeURIComponent(pathname)}`}
    >
      <LogIn aria-hidden="true" />
      Sign in
    </Link>
  );
}
