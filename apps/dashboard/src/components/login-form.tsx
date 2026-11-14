"use client";

import { KeyRound } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, useId, useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { authClient, authErrorMessage } from "@/lib/auth/client";
import type { LoginContext } from "@/lib/auth/page-context";
import {
  usePasskeysHere,
  useSecureContext,
} from "@/lib/auth/use-browser-security";

type Step = "password" | "totp" | "recovery";

/**
 * Sign in: username and password, then the authenticator app's code (or a
 * recovery code) when two-factor is on; or a passkey, which needs nothing
 * else. Only where the browser can keep the Secure session cookie: HTTPS,
 * localhost, or a host the owner allowed over plain HTTP.
 */
export function LoginForm({
  context,
  next,
}: {
  context: LoginContext;
  next: string;
}) {
  const id = useId();
  const [step, setStep] = useState<Step>("password");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const router = useRouter();
  // Known only in the browser: whether this page is a secure context, and
  // whether it is the public address, where passkeys work.
  const secure = useSecureContext();
  const passkeys = usePasskeysHere(context.passkeyOrigin);

  const done = () => {
    router.replace(next);
    router.refresh();
  };
  const blocked = secure === false && !context.insecureAllowed;

  const signIn = (event: FormEvent) => {
    event.preventDefault();
    startTransition(async () => {
      setError(undefined);
      if (step === "password") {
        const { data, error } = await authClient.signIn.username({
          username: username.trim(),
          password,
        });
        if (error)
          return setError(authErrorMessage(error, "Could not sign in."));
        if (data && "twoFactorRedirect" in data && data.twoFactorRedirect) {
          setCode("");
          return setStep("totp");
        }
        return done();
      }
      const { error } =
        step === "totp"
          ? await authClient.twoFactor.verifyTotp({ code: code.trim() })
          : await authClient.twoFactor.verifyBackupCode({ code: code.trim() });
      if (error) return setError(authErrorMessage(error, "Could not sign in."));
      done();
    });
  };

  const signInWithPasskey = () =>
    startTransition(async () => {
      setError(undefined);
      const result = await authClient.signIn.passkey();
      if (result?.error) {
        return setError(
          authErrorMessage(result.error, "The passkey did not sign you in."),
        );
      }
      done();
    });

  return (
    <AccountPage
      description={
        step === "password"
          ? undefined
          : step === "totp"
            ? "Enter the 6-digit code from your authenticator app."
            : "Enter one of your recovery codes. Each works once."
      }
      title="Sign in to Life OS"
    >
      {!context.accountExists ? (
        <Alert>
          <AlertTitle>No account yet</AlertTitle>
          <AlertDescription>
            This Life OS has no account. Create the first one with the setup
            code: <Link href="/setup">Set up the first account</Link>.
          </AlertDescription>
        </Alert>
      ) : null}

      {blocked ? (
        <Alert variant="destructive">
          <AlertTitle>Use the HTTPS address</AlertTitle>
          <AlertDescription>
            This address is plain HTTP, where signing in is not safe. Open{" "}
            <a href={`${context.publicUrl}/login`}>{context.publicUrl}</a>{" "}
            instead.
          </AlertDescription>
        </Alert>
      ) : null}

      <form className="grid gap-4" noValidate onSubmit={signIn}>
        <FieldGroup>
          {step === "password" ? (
            <>
              <Field>
                <FieldLabel htmlFor={`${id}-username`}>Username</FieldLabel>
                <Input
                  autoCapitalize="none"
                  autoComplete="username webauthn"
                  autoFocus
                  disabled={blocked}
                  id={`${id}-username`}
                  onChange={(event) => setUsername(event.target.value)}
                  required
                  spellCheck={false}
                  value={username}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor={`${id}-password`}>Password</FieldLabel>
                <Input
                  autoComplete="current-password"
                  disabled={blocked}
                  id={`${id}-password`}
                  onChange={(event) => setPassword(event.target.value)}
                  required
                  type="password"
                  value={password}
                />
              </Field>
            </>
          ) : (
            <Field>
              <FieldLabel htmlFor={`${id}-code`}>
                {step === "totp" ? "Code" : "Recovery code"}
              </FieldLabel>
              <Input
                autoComplete="one-time-code"
                autoFocus
                id={`${id}-code`}
                inputMode={step === "totp" ? "numeric" : "text"}
                onChange={(event) => setCode(event.target.value)}
                required
                spellCheck={false}
                value={code}
              />
            </Field>
          )}
        </FieldGroup>

        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {error}
          </p>
        ) : null}

        <Button disabled={pending || blocked} type="submit">
          {pending
            ? "Signing in…"
            : step === "password"
              ? "Sign in"
              : "Continue"}
        </Button>

        {step === "totp" ? (
          <Button
            onClick={() => {
              setError(undefined);
              setCode("");
              setStep("recovery");
            }}
            type="button"
            variant="link"
          >
            Use a recovery code instead
          </Button>
        ) : step === "recovery" ? (
          <Button
            onClick={() => {
              setError(undefined);
              setCode("");
              setStep("totp");
            }}
            type="button"
            variant="link"
          >
            Use the authenticator app instead
          </Button>
        ) : null}
      </form>

      {step === "password" && passkeys ? (
        <Button
          disabled={pending}
          onClick={signInWithPasskey}
          type="button"
          variant="outline"
        >
          <KeyRound aria-hidden="true" />
          Sign in with a passkey
        </Button>
      ) : null}
    </AccountPage>
  );
}
