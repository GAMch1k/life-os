import type { Metadata } from "next";

import { LegalPage } from "@/components/legal-page";

export const metadata: Metadata = { title: "Privacy policy · Life OS" };

/** Linked from Google's OAuth consent screen (Branding: privacy policy). */
export default function PrivacyPage() {
  return (
    <LegalPage title="Privacy policy" updated="10 October 2026">
      <section>
        <h2>Who uses Life OS</h2>
        <p>
          Life OS is a single-user personal system. Its owner runs it on their
          own server, for themselves only. It has no other users and no accounts
          for anyone else.
        </p>
      </section>
      <section>
        <h2>Google Calendar data</h2>
        <p>
          When the owner connects their Google account, Life OS reads and
          changes the owner&apos;s Google Calendar data (their list of calendars
          and the events in the calendars they choose) only to show and sync the
          owner&apos;s own calendars in Life OS. It asks Google only for the
          calendar permissions it needs for that.
        </p>
      </section>
      <section>
        <h2>Where it is kept</h2>
        <p>
          Everything stays on the owner&apos;s server. The tokens Google issues
          are stored there encrypted, and they are never shown in the app or
          written to logs.
        </p>
      </section>
      <section>
        <h2>Sharing</h2>
        <p>
          Life OS does not sell Google user data, share it with third parties,
          or use it for advertising, and it does not use it to train AI models.
        </p>
      </section>
      <section>
        <h2>Google API Services User Data Policy</h2>
        <p>
          Life OS&apos;s use and transfer of information received from Google
          APIs adheres to the{" "}
          <a
            className="text-primary underline-offset-4 hover:underline"
            href="https://developers.google.com/terms/api-services-user-data-policy"
            rel="noreferrer"
            target="_blank"
          >
            Google API Services User Data Policy
          </a>
          , including the Limited Use requirements.
        </p>
      </section>
      <section>
        <h2>Revoking access</h2>
        <p>
          The owner can withdraw Life OS&apos;s access at any time: with
          Disconnect in Life OS (Calendar, Google Calendar), which also deletes
          the stored tokens and calendar choices, or in their Google
          Account&apos;s third-party access settings at{" "}
          <a
            className="text-primary underline-offset-4 hover:underline"
            href="https://myaccount.google.com/connections"
            rel="noreferrer"
            target="_blank"
          >
            myaccount.google.com/connections
          </a>
          .
        </p>
      </section>
    </LegalPage>
  );
}
