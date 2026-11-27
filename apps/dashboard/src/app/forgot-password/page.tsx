import type { Metadata } from "next";
import { notFound } from "next/navigation";

import { ForgotPasswordForm } from "@/components/forgot-password-form";
import { authRuntime } from "@/lib/auth/runtime";

export const metadata: Metadata = { title: "Forgot password · Life OS" };
export const dynamic = "force-dynamic";

/**
 * "Forgot password?" (docs/install/email.md): a reset link by email to the
 * account's verified address. Only while email works on this server;
 * otherwise ./setup.sh recover-owner is the way (docs/install/accounts.md).
 */
export default async function ForgotPasswordPage() {
  const enabled = (await authRuntime().email?.enabled()) ?? false;
  if (!enabled) notFound();
  return <ForgotPasswordForm />;
}
