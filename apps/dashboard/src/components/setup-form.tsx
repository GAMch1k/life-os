"use client";

import { useRouter } from "next/navigation";
import { type FormEvent, useId, useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { authClient, authErrorMessage } from "@/lib/auth/client";
import { SETUP_CODE_HEADER } from "@/lib/auth/constants";
import type { LoginContext } from "@/lib/auth/page-context";
import { useSecureContext } from "@/lib/auth/use-browser-security";

type Errors = Partial<
  Record<"code" | "username" | "password" | "confirm" | "form", string>
>;

const USERNAME = /^[a-zA-Z0-9_.]{3,30}$/;

/**
 * The first account: the setup code from the server (./setup.sh
 * setup-code), a username and password, and, if wanted, a name and an
 * email address (only shown, never used to sign in or to send anything).
 */
export function SetupForm({ context }: { context: LoginContext }) {
  const id = useId();
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [pending, startTransition] = useTransition();
  const router = useRouter();
  const blocked = useSecureContext() === false && !context.insecureAllowed;

  const invalid = (field: keyof Errors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const found: Errors = {};
    if (!code.trim()) found.code = "Enter the setup code the server printed.";
    if (!USERNAME.test(username.trim())) {
      found.username =
        "3 to 30 letters, digits, dots, or underscores, such as anna.";
    }
    if (password.length < 10) found.password = "At least 10 characters.";
    if (confirm !== password) found.confirm = "The two passwords differ.";
    setErrors(found);
    if (Object.keys(found).length) return;
    startTransition(async () => {
      const { error } = await authClient.signUp.email(
        {
          username: username.trim(),
          name: name.trim() || username.trim(),
          // The server stores a placeholder under .invalid when empty.
          email: email.trim(),
          password,
        },
        { headers: { [SETUP_CODE_HEADER]: code.trim() } },
      );
      if (error) {
        return setErrors({
          form: authErrorMessage(error, "Could not create the account."),
        });
      }
      // Two-factor next: the owner's account manages the server.
      router.replace("/settings/security?welcome=1");
      router.refresh();
    });
  };

  return (
    <AccountPage
      description="Create the first account. It owns this Life OS and everything already in it."
      title="Set up Life OS"
    >
      {blocked ? (
        <Alert variant="destructive">
          <AlertTitle>Use the HTTPS address</AlertTitle>
          <AlertDescription>
            This address is plain HTTP. Open{" "}
            <a href={`${context.publicUrl}/setup`}>{context.publicUrl}</a>{" "}
            instead.
          </AlertDescription>
        </Alert>
      ) : null}
      <form className="grid gap-4" noValidate onSubmit={submit}>
        <FieldGroup>
          <Field data-invalid={Boolean(errors.code)}>
            <FieldLabel htmlFor={`${id}-code`}>Setup code</FieldLabel>
            <Input
              autoCapitalize="characters"
              autoComplete="off"
              autoFocus
              disabled={blocked}
              id={`${id}-code`}
              onChange={(event) => setCode(event.target.value)}
              placeholder="XXXX-XXXX-XXXX-XXXX"
              spellCheck={false}
              value={code}
              {...invalid("code")}
            />
            <FieldDescription className="text-xs">
              On the server, <code>./setup.sh setup-code</code> prints one. It
              works once, for 24 hours.
            </FieldDescription>
            <FieldError id={`${id}-code-error`}>{errors.code}</FieldError>
          </Field>
          <Field data-invalid={Boolean(errors.username)}>
            <FieldLabel htmlFor={`${id}-username`}>Username</FieldLabel>
            <Input
              autoCapitalize="none"
              autoComplete="username"
              disabled={blocked}
              id={`${id}-username`}
              onChange={(event) => setUsername(event.target.value)}
              spellCheck={false}
              value={username}
              {...invalid("username")}
            />
            <FieldError id={`${id}-username-error`}>
              {errors.username}
            </FieldError>
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-name`}>Name (optional)</FieldLabel>
            <Input
              autoComplete="name"
              disabled={blocked}
              id={`${id}-name`}
              onChange={(event) => setName(event.target.value)}
              value={name}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-email`}>Email (optional)</FieldLabel>
            <Input
              autoComplete="email"
              disabled={blocked}
              id={`${id}-email`}
              inputMode="email"
              onChange={(event) => setEmail(event.target.value)}
              type="email"
              value={email}
            />
            <FieldDescription className="text-xs">
              Only shown. Life OS never signs in or sends mail with it.
            </FieldDescription>
          </Field>
          <Field data-invalid={Boolean(errors.password)}>
            <FieldLabel htmlFor={`${id}-password`}>Password</FieldLabel>
            <Input
              autoComplete="new-password"
              disabled={blocked}
              id={`${id}-password`}
              onChange={(event) => setPassword(event.target.value)}
              type="password"
              value={password}
              {...invalid("password")}
            />
            <FieldError id={`${id}-password-error`}>
              {errors.password}
            </FieldError>
          </Field>
          <Field data-invalid={Boolean(errors.confirm)}>
            <FieldLabel htmlFor={`${id}-confirm`}>Password again</FieldLabel>
            <Input
              autoComplete="new-password"
              disabled={blocked}
              id={`${id}-confirm`}
              onChange={(event) => setConfirm(event.target.value)}
              type="password"
              value={confirm}
              {...invalid("confirm")}
            />
            <FieldError id={`${id}-confirm-error`}>{errors.confirm}</FieldError>
          </Field>
        </FieldGroup>
        {errors.form ? (
          <p className="text-destructive text-sm" role="alert">
            {errors.form}
          </p>
        ) : null}
        <Button disabled={pending || blocked} type="submit">
          {pending ? "Creating the account…" : "Create the account"}
        </Button>
      </form>
    </AccountPage>
  );
}
