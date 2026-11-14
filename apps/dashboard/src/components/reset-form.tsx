"use client";

import Link from "next/link";
import { type FormEvent, useId, useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { authClient, authErrorMessage } from "@/lib/auth/client";

/** A new password from a one-time reset link. */
export function ResetForm({ token }: { token: string }) {
  const id = useId();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<
    Partial<Record<"password" | "confirm" | "form", string>>
  >({});
  const [done, setDone] = useState(false);
  const [pending, startTransition] = useTransition();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const found: typeof errors = {};
    if (password.length < 10) found.password = "At least 10 characters.";
    if (confirm !== password) found.confirm = "The two passwords differ.";
    setErrors(found);
    if (Object.keys(found).length) return;
    startTransition(async () => {
      const { error } = await authClient.resetPassword({
        newPassword: password,
        token,
      });
      if (error) {
        return setErrors({
          form:
            error.code === "INVALID_TOKEN"
              ? "This link is used or expired. Ask for a new one."
              : authErrorMessage(error, "Could not set the password."),
        });
      }
      setDone(true);
    });
  };

  if (done) {
    return (
      <AccountPage title="Password changed">
        <p className="text-sm">
          Your new password is set, and every session of your account was signed
          out.
        </p>
        <Link className="text-sm font-medium underline" href="/login">
          Sign in
        </Link>
      </AccountPage>
    );
  }

  return (
    <AccountPage title="Choose a new password">
      <form className="grid gap-4" noValidate onSubmit={submit}>
        <FieldGroup>
          <Field data-invalid={Boolean(errors.password)}>
            <FieldLabel htmlFor={`${id}-password`}>New password</FieldLabel>
            <Input
              autoComplete="new-password"
              autoFocus
              id={`${id}-password`}
              onChange={(event) => setPassword(event.target.value)}
              type="password"
              value={password}
            />
            <FieldError>{errors.password}</FieldError>
          </Field>
          <Field data-invalid={Boolean(errors.confirm)}>
            <FieldLabel htmlFor={`${id}-confirm`}>Password again</FieldLabel>
            <Input
              autoComplete="new-password"
              id={`${id}-confirm`}
              onChange={(event) => setConfirm(event.target.value)}
              type="password"
              value={confirm}
            />
            <FieldError>{errors.confirm}</FieldError>
          </Field>
        </FieldGroup>
        {errors.form ? (
          <p className="text-destructive text-sm" role="alert">
            {errors.form}
          </p>
        ) : null}
        <Button disabled={pending} type="submit">
          {pending ? "Saving…" : "Set the password"}
        </Button>
      </form>
    </AccountPage>
  );
}
