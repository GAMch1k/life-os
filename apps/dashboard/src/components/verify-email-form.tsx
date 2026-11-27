"use client";

import Link from "next/link";
import { useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Button } from "@/components/ui/button";

/**
 * A verification link from an email (docs/install/email.md). It asks for a
 * click rather than verifying on opening: mail filters open links to
 * check them, and must not use the link up.
 */
export function VerifyEmailForm({ token }: { token: string }) {
  const [state, setState] = useState<"ready" | "done" | "failed">("ready");
  const [message, setMessage] = useState<string>();
  const [pending, startTransition] = useTransition();

  const verify = () =>
    startTransition(async () => {
      const response = await fetch("/api/auth/email/verify", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ token }),
      }).catch(() => null);
      if (response?.ok) return setState("done");
      const body = (await response?.json().catch(() => null)) as {
        error?: { message?: string };
      } | null;
      setMessage(body?.error?.message ?? "Could not verify the address.");
      setState("failed");
    });

  if (state === "done") {
    return (
      <AccountPage title="Email address verified">
        <p className="text-sm">
          Life OS can now email you a link when you forget your password, and
          the security alerts you choose in Settings → Email.
        </p>
        <Link className="text-sm font-medium underline" href="/settings/email">
          Open Settings → Email
        </Link>
      </AccountPage>
    );
  }

  return (
    <AccountPage title="Verify your email address">
      {state === "failed" ? (
        <p className="text-destructive text-sm" role="alert">
          {message}
        </p>
      ) : (
        <p className="text-sm">
          Verify this address for your Life OS account. The link works once.
        </p>
      )}
      {state === "failed" ? (
        <Link className="text-sm font-medium underline" href="/settings/email">
          Open Settings → Email
        </Link>
      ) : (
        <Button disabled={pending} onClick={verify} type="button">
          {pending ? "Verifying…" : "Verify my email address"}
        </Button>
      )}
    </AccountPage>
  );
}
