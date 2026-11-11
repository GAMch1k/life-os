import type { Metadata } from "next";
import { notFound } from "next/navigation";

import { GoogleCallback } from "@/components/google-callback";
import { callbackStep } from "@/lib/google-calendar";
import { featureOn } from "@/lib/modules";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Connecting Google Calendar · Life OS",
  // The URL carries Google's one-time code: never send it on.
  referrer: "no-referrer",
  robots: { index: false, follow: false },
};

/**
 * Where Google returns after the owner's consent (the OAuth redirect URI,
 * LIFE_OS_GOOGLE_REDIRECT_URI). The browser hands the code and state to
 * Calendar, which completes the connection; the page then goes back to the
 * Calendar.
 */
export default async function GoogleCallbackPage({
  searchParams,
}: PageProps<"/calendar/google/callback">) {
  // Google Calendar's own page, while its module is on.
  if (!featureOn("calendar.google")) notFound();
  return <GoogleCallback step={callbackStep(await searchParams)} />;
}
