"use client";

import Link from "next/link";
import { type FormEvent, useId, useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";

/**
 * "Forgot password?" (docs/install/email.md): a username or an address,
 * and Life OS mails a one-time reset link to the account's verified
 * address. The answer is the same whether or not such an account exists.
 */
export function ForgotPasswordForm() {
  const id = useId();
  const [login, setLogin] = useState("");
  const [error, setError] = useState<string>();
  const [sent, setSent] = useState<number | null>(null);
  const [pending, startTransition] = useTransition();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!login.trim()) return setError("Enter your username or email address.");
    startTransition(async () => {
      setError(undefined);
      const response = await fetch("/api/auth/email/forgot-password", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ login: login.trim() }),
      }).catch(() => null);
      const body = (await response?.json().catch(() => null)) as {
        minutes?: number;
        error?: { message?: string };
      } | null;
      if (!response?.ok) {
        return setError(
          body?.error?.message ?? "Could not ask for a link. Try again.",
        );
      }
      setSent(body?.minutes ?? 30);
    });
  };

  if (sent !== null) {
    return (
      <AccountPage title="Check your email">
        <p className="text-sm">
          If the account has a verified email address, a link to set a new
          password is on its way there. It works once, for {sent} minutes.
        </p>
        <p className="text-muted-foreground text-sm">
          Nothing came? Look in the spam folder. Without a verified address, the
          server&apos;s owner can make a link with ./setup.sh recover-owner.
        </p>
        <Link className="text-sm font-medium underline" href="/login">
          Back to sign in
        </Link>
      </AccountPage>
    );
  }

  return (
    <AccountPage
      description="Life OS emails a link to set a new password to your account's verified address."
      title="Forgot your password?"
    >
      <form className="grid gap-4" noValidate onSubmit={submit}>
        <Field data-invalid={Boolean(error)}>
          <FieldLabel htmlFor={`${id}-login`}>
            Username or email address
          </FieldLabel>
          <Input
            autoCapitalize="none"
            autoComplete="username"
            autoFocus
            id={`${id}-login`}
            onChange={(event) => setLogin(event.target.value)}
            spellCheck={false}
            value={login}
          />
          <FieldDescription>
            Setting a new password signs out every session of the account, and
            its paired devices, API tokens, and connected apps.
          </FieldDescription>
          <FieldError>{error}</FieldError>
        </Field>
        <Button disabled={pending} type="submit">
          {pending ? "Sending…" : "Email me a link"}
        </Button>
        <Link className="text-center text-sm underline" href="/login">
          Back to sign in
        </Link>
      </form>
    </AccountPage>
  );
}
