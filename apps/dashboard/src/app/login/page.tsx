import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { LoginForm } from "@/components/login-form";
import { loginContext, safeNext } from "@/lib/auth/page-context";

export const metadata: Metadata = { title: "Sign in · Life OS" };
export const dynamic = "force-dynamic";

/** Sign in: password, then TOTP or a recovery code; or a passkey. */
export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const params = await searchParams;
  const next = safeNext(params.next);
  const context = await loginContext();
  if (context.signedIn) redirect(next);
  return <LoginForm context={context} next={next} />;
}
