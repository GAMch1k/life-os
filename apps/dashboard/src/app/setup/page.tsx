import type { Metadata } from "next";
import Link from "next/link";

import { AccountPage } from "@/components/account-page";
import { SetupForm } from "@/components/setup-form";
import { loginContext } from "@/lib/auth/page-context";

export const metadata: Metadata = { title: "Set up · Life OS" };
export const dynamic = "force-dynamic";

/**
 * The first account, with the one-time setup code ./setup.sh prints. It
 * becomes the server's owner. Once an account exists, this page only says so.
 */
export default async function SetupPage() {
  const context = await loginContext();
  if (context.accountExists) {
    return (
      <AccountPage title="Life OS is set up">
        <p className="text-sm">
          This Life OS already has its first account. New accounts come with an
          invite from the owner.
        </p>
        <Link className="text-sm font-medium underline" href="/login">
          Sign in
        </Link>
      </AccountPage>
    );
  }
  return <SetupForm context={context} />;
}
