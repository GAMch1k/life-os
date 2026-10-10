"use client";

import { AlertTriangle } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import { UNAVAILABLE } from "@/components/calendar-event-dialog";
import { buttonVariants } from "@/components/ui/button";
import { calendarClient } from "@/lib/api/calendar-client";
import {
  type CallbackStep,
  connectFailure,
  GOOGLE_CALLBACK_PATH,
  GOOGLE_CONNECTED,
  GOOGLE_PARAM,
} from "@/lib/google-calendar";

/**
 * Completes a Google connection: posts the code and state Google returned to
 * Calendar (through the Dashboard's /api/calendar proxy, as the owner), then
 * returns to the Calendar, where the Google dialog opens. The code is used
 * once and dropped from the address bar right away.
 */
export function GoogleCallback({ step }: { step: CallbackStep }) {
  const router = useRouter();
  // The first answer only: the page may re-render without the query.
  const [initial] = useState(step);
  const [failure, setFailure] = useState(
    initial.kind === "error" ? initial.message : undefined,
  );
  const started = useRef(false);

  useEffect(() => {
    window.history.replaceState(window.history.state, "", GOOGLE_CALLBACK_PATH);
    if (initial.kind !== "exchange" || started.current) return;
    started.current = true;
    calendarClient
      .POST("/v1/google/connection", {
        body: { code: initial.code, state: initial.state },
      })
      .then(({ data, error, response }) => {
        if (data) {
          router.replace(`/calendar?${GOOGLE_PARAM}=${GOOGLE_CONNECTED}`);
        } else {
          setFailure(connectFailure(response.status, error).message);
        }
      })
      .catch(() => setFailure(UNAVAILABLE));
  }, [initial, router]);

  return (
    <div className="mx-auto max-w-lg space-y-4 py-8">
      <h1 className="text-xl font-semibold tracking-tight">Google Calendar</h1>
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
            Google Calendar was not connected.
          </p>
          <p className="text-sm">{failure}</p>
          <Link
            className={buttonVariants({ variant: "outline" })}
            href="/calendar"
          >
            Back to Calendar
          </Link>
        </div>
      ) : (
        <p className="text-muted-foreground" role="status">
          Connecting Google Calendar…
        </p>
      )}
    </div>
  );
}
