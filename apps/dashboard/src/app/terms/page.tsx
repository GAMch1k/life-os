import type { Metadata } from "next";
import Link from "next/link";

import { LegalPage } from "@/components/legal-page";

export const metadata: Metadata = { title: "Terms of service · Life OS" };

/** Linked from Google's OAuth consent screen (Branding: terms of service). */
export default function TermsPage() {
  return (
    <LegalPage title="Terms of service" updated="10 October 2026">
      <section>
        <h2>Personal use only</h2>
        <p>
          Life OS is a personal system for its owner&apos;s own use. It runs on
          the owner&apos;s server and has no other users; nobody else may sign
          up or use it.
        </p>
      </section>
      <section>
        <h2>As is</h2>
        <p>
          Life OS is provided as is, without warranties of any kind. The owner
          runs and maintains it at their own discretion and may change or stop
          it at any time.
        </p>
      </section>
      <section>
        <h2>Google services</h2>
        <p>
          Connecting Google Calendar is optional. Its use is also subject to
          Google&apos;s own terms, and how Life OS handles Google data is
          described in the{" "}
          <Link
            className="text-primary underline-offset-4 hover:underline"
            href="/privacy"
          >
            privacy policy
          </Link>
          .
        </p>
      </section>
    </LegalPage>
  );
}
