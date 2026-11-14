"use client";

import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";

import type { AccountInfo } from "./account";

/**
 * Who the app is signed in as (GET /api/account), for the header's account
 * button and the legacy access reminder. Read again on every navigation, so
 * that signing in or out shows at once.
 */
export function useAccount(): AccountInfo | undefined {
  const pathname = usePathname();
  const [account, setAccount] = useState<AccountInfo>();
  useEffect(() => {
    let cancelled = false;
    fetch("/api/account", { cache: "no-store" })
      .then((response) => (response.ok ? response.json() : undefined))
      .then((info: AccountInfo | undefined) => {
        if (!cancelled) setAccount(info);
      })
      .catch(() => {
        if (!cancelled) setAccount(undefined);
      });
    return () => {
      cancelled = true;
    };
  }, [pathname]);
  return account;
}
