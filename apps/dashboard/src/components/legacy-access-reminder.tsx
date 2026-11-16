"use client";

import { Unlock, X } from "lucide-react";
import Link from "next/link";
import { useSyncExternalStore } from "react";

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { AccountInfo } from "@/lib/auth/account";
import { showsLegacyReminder } from "@/lib/auth/legacy-reminder";

const DISMISSED_KEY = "lifeos-legacy-reminder";

function today() {
  return new Date().toLocaleDateString("en-CA");
}

// The day the reminder was last hidden, in localStorage (and in memory,
// for a browser without storage); read with useSyncExternalStore, so the
// server and the hydration pass render nothing and match.
const listeners = new Set<() => void>();
let hiddenOn: string | null = null;

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function storedDay() {
  try {
    return window.localStorage.getItem(DISMISSED_KEY) ?? hiddenOn;
  } catch {
    return hiddenOn;
  }
}

function hideForToday() {
  hiddenOn = today();
  try {
    window.localStorage.setItem(DISMISSED_KEY, hiddenOn);
  } catch {
    // Private mode: hidden for this page only.
  }
  for (const listener of listeners) listener();
}

/**
 * While legacy access is on and an account exists, a reminder once a day:
 * anyone who reaches Life OS acts as the owner
 * (docs/architecture/multi-user.md#legacy-access). It shows since devices
 * can be paired (DEVICE_PAIRING_AVAILABLE, P21-06): the phone and watch apps
 * no longer need legacy access once they are paired in Settings → Devices.
 * Before the first account exists, the header's account button points to
 * /setup instead.
 */
export function LegacyAccessReminder({ account }: { account?: AccountInfo }) {
  const dismissed = useSyncExternalStore(
    subscribe,
    () => storedDay() === today(),
    () => true,
  );

  if (!showsLegacyReminder(account, dismissed)) return null;

  return (
    <Alert
      aria-label="Legacy access"
      className="mb-3"
      data-testid="legacy-access-reminder"
      role="region"
    >
      <Unlock aria-hidden="true" />
      <AlertTitle>Legacy access is on</AlertTitle>
      <AlertDescription>
        Anyone who reaches Life OS without signing in acts as the owner. Pair
        your phone and watch apps in{" "}
        <Link className="underline" href="/settings/devices">
          Settings → Devices
        </Link>
        , then turn it off in{" "}
        <Link className="underline" href="/settings/security">
          Settings → Security
        </Link>
        .
      </AlertDescription>
      <AlertAction>
        <Button
          aria-label="Hide until tomorrow"
          onClick={hideForToday}
          size="icon-sm"
          title="Hide until tomorrow"
          variant="ghost"
        >
          <X aria-hidden="true" />
        </Button>
      </AlertAction>
    </Alert>
  );
}
