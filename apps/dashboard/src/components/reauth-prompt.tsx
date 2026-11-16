"use client";

import { LockKeyhole } from "lucide-react";
import { type FormEvent, useId, useState, useTransition } from "react";

import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

/**
 * Asks for the password again (POST /api/account/confirm-password,
 * src/lib/auth/reauth.ts) before something that outlives the session:
 * an API token, a pairing code, an app's access, or a new second factor.
 */
export function ReauthPrompt({
  description = "You signed in more than 12 hours ago. Enter your password to go on.",
  onConfirmed,
}: {
  description?: string;
  onConfirmed: () => void;
}) {
  const id = useId();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const confirm = (event: FormEvent) => {
    event.preventDefault();
    startTransition(async () => {
      setError(undefined);
      const response = await fetch("/api/account/confirm-password", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ password }),
      }).catch(() => null);
      if (response?.status === 204) {
        setPassword("");
        return onConfirmed();
      }
      const body = (await response?.json().catch(() => null)) as {
        error?: { message?: string };
      } | null;
      setError(body?.error?.message ?? "Could not check your password.");
    });
  };

  return (
    <form
      aria-label="Confirm your password"
      className="grid gap-3"
      noValidate
      onSubmit={confirm}
    >
      <Field>
        <FieldLabel
          className="flex items-center gap-2"
          htmlFor={`${id}-password`}
        >
          <LockKeyhole aria-hidden="true" className="size-4" />
          Confirm your password
        </FieldLabel>
        <FieldDescription>{description}</FieldDescription>
        <Input
          autoComplete="current-password"
          id={`${id}-password`}
          onChange={(event) => setPassword(event.target.value)}
          required
          type="password"
          value={password}
        />
      </Field>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <div>
        <Button disabled={busy || !password} type="submit">
          Confirm
        </Button>
      </div>
    </form>
  );
}
