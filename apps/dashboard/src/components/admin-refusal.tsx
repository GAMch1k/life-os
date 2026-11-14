import Link from "next/link";
import { ShieldAlert, ShieldCheck } from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import type { AdminRefusalCode } from "@/lib/auth/admin";

/**
 * What an admin page shows someone it does not open for: a member, or an
 * admin who has not turned on two-factor sign-in yet.
 */
export function AdminRefusal({
  code,
  title,
}: {
  code: AdminRefusalCode;
  title: string;
}) {
  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      {code === "second_factor_required" ? (
        <Alert>
          <ShieldCheck aria-hidden="true" />
          <AlertTitle>Turn on two-factor sign-in first</AlertTitle>
          <AlertDescription>
            <p>
              The server&apos;s admin pages need an authenticator app or a
              passkey on your account. Add one in{" "}
              <Link
                className="text-primary underline-offset-4 hover:underline"
                href="/settings/security"
              >
                Settings → Security
              </Link>
              .
            </p>
          </AlertDescription>
        </Alert>
      ) : code === "session_not_fresh" ? (
        <Alert>
          <ShieldAlert aria-hidden="true" />
          <AlertTitle>Sign in again</AlertTitle>
          <AlertDescription>
            <p>This needs a sign-in from the last 12 hours.</p>
          </AlertDescription>
        </Alert>
      ) : (
        <Alert>
          <ShieldAlert aria-hidden="true" />
          <AlertTitle>For the server&apos;s admins</AlertTitle>
          <AlertDescription>
            <p>
              This page is for the admins of this Life OS. Your own sign-ins are
              in{" "}
              <Link
                className="text-primary underline-offset-4 hover:underline"
                href="/settings/security-log"
              >
                Settings → Security log
              </Link>
              .
            </p>
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}
