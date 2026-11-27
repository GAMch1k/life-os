import type { Metadata } from "next";

import { VerifyEmailForm } from "@/components/verify-email-form";

export const metadata: Metadata = { title: "Verify your email · Life OS" };
export const dynamic = "force-dynamic";

/**
 * A one-time link to verify an account's email address, from the email
 * Settings → Email sends (docs/install/email.md). It works once, for 24
 * hours, and only for the address it was sent to.
 */
export default async function VerifyEmailPage({
  params,
}: PageProps<"/verify-email/[token]">) {
  const { token } = await params;
  return <VerifyEmailForm token={token} />;
}
