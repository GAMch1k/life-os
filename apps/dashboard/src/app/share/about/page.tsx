import type { Metadata } from "next";
import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

export const metadata: Metadata = { title: "Share to Life OS" };

/** How to install Life OS on the phone and share into it (GET /share). */
export default function ShareAboutPage() {
  return (
    <div className="mx-auto max-w-lg space-y-4 py-8">
      <h1 className="text-xl font-semibold tracking-tight">Share to Life OS</h1>
      <Card aria-labelledby="share-install" role="region">
        <CardContent className="space-y-2 text-sm">
          <h2 className="font-semibold" id="share-install">
            Install it on your phone
          </h2>
          <p>
            Open Life OS in Chrome on your Android phone and log in. In
            Chrome&apos;s menu (⋮), tap <strong>Install app</strong> (or{" "}
            <strong>Add to Home screen</strong>, then Install). Life OS gets its
            own icon and opens without the browser bar.
          </p>
        </CardContent>
      </Card>
      <Card aria-labelledby="share-how" role="region">
        <CardContent className="space-y-2 text-sm">
          <h2 className="font-semibold" id="share-how">
            Share from any app
          </h2>
          <p>
            In Instagram, Chrome, a notes app, or anywhere else, tap Share and
            choose <strong>Life OS</strong>. What you share (its text and link)
            is saved to Inbox as a capture, and the AI processes it like
            anything you type.
          </p>
        </CardContent>
      </Card>
      <Card aria-labelledby="share-lost" role="region">
        <CardContent className="space-y-2 text-sm">
          <h2 className="font-semibold" id="share-lost">
            Landed here after sharing?
          </h2>
          <p>
            Then your Life OS login had expired, and what you shared was not
            saved. You are logged in again now: share it once more.
          </p>
        </CardContent>
      </Card>
      <Link className={buttonVariants({ variant: "outline" })} href="/">
        Back to Life OS
      </Link>
    </div>
  );
}
