import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";
import { CircleCheck, TriangleAlert } from "lucide-react";

import { CaptureImageStrip } from "@/components/capture-images";
import { InboxStateBadge } from "@/components/inbox-summary";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { createInboxClient } from "@/lib/api/inbox-client";
import { captureImages } from "@/lib/capture-images";
import { type InboxItem, inboxItemStatus } from "@/lib/inbox-summary";
import { SHARE_ERRORS, shareError } from "@/lib/share";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Shared to Life OS",
  robots: { index: false, follow: false },
};

/** The saved capture, or undefined when Inbox cannot say right now. */
async function sharedItem(id: string): Promise<InboxItem | undefined> {
  try {
    const { data } = await createInboxClient().GET("/v1/inbox-items/{itemId}", {
      params: { path: { itemId: id } },
    });
    return data;
  } catch {
    return undefined;
  }
}

/**
 * Where a share lands (/share answers 303 here): "Saved to Inbox" with the
 * capture and where to follow it, or why nothing was saved.
 */
export default async function ShareSavedPage({
  searchParams,
}: PageProps<"/share/saved">) {
  const params = await searchParams;
  const error = shareError(params.error);
  const id = typeof params.item === "string" ? params.item : undefined;
  if (!error && !id) redirect("/share/about");
  const [item, timeZone] = await Promise.all([
    id && !error ? sharedItem(id) : undefined,
    userTimeZone(),
  ]);
  const now = new Date();
  return (
    <div className="mx-auto max-w-lg space-y-4 py-8">
      <h1 className="text-xl font-semibold tracking-tight">Share to Life OS</h1>
      {error ? (
        <div
          className="bg-warning-subtle border-warning/30 space-y-3 rounded-xl border p-4"
          role="alert"
        >
          <p className="flex items-start gap-2 font-medium">
            <TriangleAlert
              aria-hidden="true"
              className="text-warning mt-0.5 size-4 shrink-0"
            />
            Not saved
          </p>
          <p className="text-sm">{SHARE_ERRORS[error]}</p>
          <Link
            className={buttonVariants({ variant: "outline" })}
            href="/share/about"
          >
            How sharing works
          </Link>
        </div>
      ) : (
        <Card aria-labelledby="share-saved" role="region">
          <CardContent className="space-y-3">
            <p
              className="flex items-center gap-2 font-medium"
              id="share-saved"
              role="status"
            >
              <CircleCheck aria-hidden="true" className="size-4 shrink-0" />
              Saved to Inbox
            </p>
            {item ? (
              <div>
                <div className="flex items-start gap-2">
                  <p className="text-muted-foreground line-clamp-4 min-w-0 flex-1 text-sm wrap-anywhere whitespace-pre-line">
                    {item.content}
                  </p>
                  <InboxStateBadge
                    now={now}
                    status={inboxItemStatus(item, now)}
                    timeZone={timeZone}
                  />
                </div>
                <CaptureImageStrip
                  label={item.content}
                  references={captureImages(item)}
                />
              </div>
            ) : null}
            <p className="text-muted-foreground text-sm">
              The AI processes it like anything you capture.
            </p>
            <div className="flex flex-wrap gap-2">
              <Link className={buttonVariants()} href="/inbox#pending">
                Open Inbox
              </Link>
              <Link
                className={buttonVariants({ variant: "outline" })}
                href={`/activity?correlationId=${encodeURIComponent(item?.correlationId ?? id ?? "")}`}
              >
                Follow it in the Event Log
              </Link>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
