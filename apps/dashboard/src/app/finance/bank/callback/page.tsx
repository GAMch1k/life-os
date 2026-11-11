import type { Metadata } from "next";
import { notFound } from "next/navigation";

import { BankCallback } from "@/components/bank-callback";
import { callbackStep } from "@/lib/banking";
import { featureOn } from "@/lib/modules";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Connecting your bank · Life OS",
  // The URL carries the bank's one-time code: never send it on.
  referrer: "no-referrer",
  robots: { index: false, follow: false },
};

/**
 * Where the bank returns after the owner's consent (the redirect URI
 * registered on the Enable Banking application,
 * LIFE_OS_BANKING_REDIRECT_URI). The browser hands the code and state to the
 * banking integration, which completes the connection and runs the first
 * sync; the page then goes back to Finance.
 */
export default async function BankCallbackPage({
  searchParams,
}: PageProps<"/finance/bank/callback">) {
  // Bank sync's own page, while its module is on.
  if (!featureOn("finance.bank")) notFound();
  const query = await searchParams;
  const step = callbackStep(query);
  if (query.error) {
    // The server log keeps why a consent failed (never the code or state),
    // so a failure the owner saw on the phone can be looked into later.
    console.warn(
      JSON.stringify({
        msg: "bank consent not completed",
        error: String(query.error).slice(0, 64),
        description: query.error_description
          ? String(query.error_description).slice(0, 300)
          : null,
      }),
    );
  }
  return <BankCallback step={step} />;
}
