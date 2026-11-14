import type { Metadata } from "next";

import { ResetForm } from "@/components/reset-form";

export const metadata: Metadata = { title: "New password · Life OS" };
export const dynamic = "force-dynamic";

/**
 * A one-time link to set a new password: ./setup.sh recover-owner prints
 * one for the owner (and P21-04's admin panel for others). It works once,
 * for 24 hours, and signs out every session of the account.
 */
export default async function ResetPage({
  params,
}: PageProps<"/reset/[token]">) {
  const { token } = await params;
  return <ResetForm token={token} />;
}
