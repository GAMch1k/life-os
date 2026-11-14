"use client";

import { Smartphone } from "lucide-react";
import { useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";

/** The only address the pairing page ever sends the browser to. */
const PAIRING_LINK = "lifeos://pair?";

/**
 * Pairing a phone from its Custom Tab (/login?pair=1, docs/api/devices.md):
 * signed in, the person confirms, the page asks for a pairing code bound to
 * the app's challenge, and the browser goes back to the app at
 * lifeos://pair. Cancel goes back with error=access_denied.
 */
export function PairDevice({
  cancelLink,
  challenge,
  publicUrl,
  state,
}: {
  cancelLink: string;
  /** The app's challenge; null when the link the app opened was incomplete. */
  challenge: string | null;
  publicUrl: string;
  state?: string;
}) {
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const host = new URL(publicUrl).host;

  const pair = () =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch("/api/account/devices/pairing-codes", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ challenge, ...(state ? { state } : {}) }),
      }).catch(() => null);
      const body = (await response?.json().catch(() => null)) as {
        link?: string;
        error?: { message?: string };
      } | null;
      if (!response?.ok || !body?.link?.startsWith(PAIRING_LINK)) {
        return setError(
          body?.error?.message ?? "Could not make a pairing code. Try again.",
        );
      }
      window.location.assign(body.link);
    });

  const cancel = () => {
    if (cancelLink.startsWith(PAIRING_LINK)) window.location.assign(cancelLink);
  };

  if (!challenge) {
    return (
      <AccountPage title="Pair a phone">
        <Alert variant="destructive">
          <AlertTitle>This pairing link is incomplete</AlertTitle>
          <AlertDescription>
            Start again from the Life OS app on your phone, or pair it with the
            QR code in Settings → Devices on another screen.
          </AlertDescription>
        </Alert>
      </AccountPage>
    );
  }

  return (
    <AccountPage
      description={`Your phone's Life OS app asks to use ${host} with your account.`}
      title="Pair this phone?"
    >
      <p className="flex items-start gap-2 text-sm">
        <Smartphone aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
        <span>
          The app gets a key of its own, which you can revoke in Settings →
          Devices. Pair only a phone you are holding.
        </span>
      </p>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <Button disabled={pending} onClick={pair} type="button">
        {pending ? "Pairing…" : "Pair"}
      </Button>
      <Button
        disabled={pending}
        onClick={cancel}
        type="button"
        variant="outline"
      >
        Cancel
      </Button>
    </AccountPage>
  );
}
