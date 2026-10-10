"use client";

import { AlertTriangle } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { bankingClient } from "@/lib/api/banking-client";
import {
  BANK_CALLBACK_PATH,
  BANK_CONNECTED,
  BANK_PARAM,
  bankFailure,
  type CallbackStep,
} from "@/lib/banking";

/** Shown when banking cannot be reached at all. */
export const BANKING_UNAVAILABLE =
  "Banking is unavailable right now. Try again.";

/**
 * Completes a bank connection: posts the code and state the bank returned to
 * the banking integration (through /api/integrations/banking, as the owner),
 * which connects and runs the first sync, then returns to Finance. The code
 * is used once and dropped from the address bar right away. When the bank
 * returned an error instead, the page reports it to banking (the state, the
 * error, and its description), which keeps it for the Finance page.
 */
export function BankCallback({ step }: { step: CallbackStep }) {
  const router = useRouter();
  // The first answer only: the page may re-render without the query.
  const [initial] = useState(step);
  const [failure, setFailure] = useState(
    initial.kind === "error" ? initial.message : undefined,
  );
  const started = useRef(false);

  useEffect(() => {
    window.history.replaceState(window.history.state, "", BANK_CALLBACK_PATH);
    if (started.current) return;
    if (initial.kind === "error") {
      if (!initial.report) return;
      started.current = true;
      bankingClient
        .POST("/v1/connections/consent-failure", { body: initial.report })
        .catch(() => undefined);
      return;
    }
    started.current = true;
    bankingClient
      .POST("/v1/connections", {
        body: { code: initial.code, state: initial.state },
      })
      .then(({ data, error, response }) => {
        if (data) {
          router.replace(`/finance?${BANK_PARAM}=${BANK_CONNECTED}`);
        } else {
          setFailure(bankFailure(response.status, error).message);
        }
      })
      .catch(() => setFailure(BANKING_UNAVAILABLE));
  }, [initial, router]);

  return (
    <div className="mx-auto max-w-lg space-y-4 py-8">
      <h1 className="text-xl font-semibold tracking-tight">Bank connection</h1>
      {failure ? (
        <div
          className="bg-warning-subtle border-warning/30 space-y-3 rounded-xl border p-4"
          role="alert"
        >
          <p className="flex items-start gap-2 font-medium">
            <AlertTriangle
              aria-hidden="true"
              className="text-warning mt-0.5 size-4 shrink-0"
            />
            Your bank was not connected.
          </p>
          <p className="text-sm">{failure}</p>
          <Link
            className={buttonVariants({ variant: "outline" })}
            href="/finance"
          >
            Back to Finance
          </Link>
        </div>
      ) : (
        <p className="text-muted-foreground" role="status">
          Connecting your bank and importing the last 90 days…
        </p>
      )}
    </div>
  );
}
