"use client";

import { KeyRound } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, useId, useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { PlainHttpNotice } from "@/components/plain-http-notice";
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

type Step = "password" | "totp" | "recovery" | "email";

/**
 * Sign in: username and password, then the authenticator app's code, a
 * code by email (when the account turned those on, docs/install/email.md),
 * or a recovery code when two-factor is on; or a passkey, which needs
 * nothing else. Only where signing in is safe enough: HTTPS, this
 * machine's loopback (an SSH tunnel), or a host the owner allowed over
 * plain HTTP; elsewhere it says how to get there (PlainHttpNotice).
 */
export function LoginForm({
  context,
  navigate = "app",
  next,
}: {
  context: LoginContext;
  /**
   * How to go on after signing in: within the app, or (for an MCP
   * client's OAuth request) a full page load of the provider's authorize
   * endpoint, which redirects onwards.
   */
  navigate?: "app" | "document";
  next: string;
}) {
  const id = useId();
  const [step, setStep] = useState<Step>("password");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string>();
  // The second factors Better Auth offers this sign-in: "totp" (the
  // authenticator app) and "otp" (a code by email).
  const [methods, setMethods] = useState<string[]>([]);
  const [sentTo, setSentTo] = useState(false);
  const [pending, startTransition] = useTransition();
  const router = useRouter();
  // Known only in the browser: whether this page is a secure context, and
  // whether it is the public address, where passkeys work.
  const secure = useSecureContext();
  const passkeys = usePasskeysHere(context.passkeyOrigin);

  const done = () => {
    if (navigate === "document") {
      window.location.assign(next);
      return;
    }
    router.replace(next);
    router.refresh();
  };
  const blocked = secure === false && !context.insecureAllowed;

  /** Emails a sign-in code, and shows the email step. */
  const sendCode = async () => {
    setError(undefined);
    setCode("");
    setStep("email");
    const { error } = await authClient.twoFactor.sendOtp();
    if (error) {
      setSentTo(false);
      return setError(authErrorMessage(error, "Could not send a code."));
    }
    setSentTo(true);
  };

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
          const offered = (
            "twoFactorMethods" in data && Array.isArray(data.twoFactorMethods)
              ? data.twoFactorMethods
              : ["totp"]
          ) as string[];
          setMethods(offered);
          setCode("");
          if (offered.includes("totp")) return setStep("totp");
          if (offered.includes("otp")) return sendCode();
          setStep("recovery");
          return setError(
            "Your second factor is a code by email, and Life OS cannot send email now. Use a recovery code, or ask the server's owner.",
          );
        }
        return done();
      }
      const { error } =
        step === "totp"
          ? await authClient.twoFactor.verifyTotp({ code: code.trim() })
          : step === "email"
            ? await authClient.twoFactor.verifyOtp({ code: code.trim() })
            : await authClient.twoFactor.verifyBackupCode({
                code: code.trim(),
              });
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

  const switchTo = (next: Step) => {
    setError(undefined);
    setCode("");
    if (next === "email") {
      startTransition(sendCode);
      return;
    }
    setStep(next);
  };
  const otherWays: { step: Step; label: string }[] = [
    ...(step !== "totp" && methods.includes("totp")
      ? [{ step: "totp" as Step, label: "Use the authenticator app instead" }]
      : []),
    ...(step !== "email" && methods.includes("otp")
      ? [{ step: "email" as Step, label: "Email me a code instead" }]
      : []),
    ...(step !== "recovery" && methods.includes("totp")
      ? [{ step: "recovery" as Step, label: "Use a recovery code instead" }]
      : []),
  ];

  return (
    <AccountPage
      description={
        step === "password"
          ? undefined
          : step === "totp"
            ? "Enter the 6-digit code from your authenticator app."
            : step === "email"
              ? sentTo
                ? "Life OS emailed a 6-digit code to your verified address. It works for 10 minutes."
                : "Life OS emails a 6-digit code to your verified address."
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
        <PlainHttpNotice path="/login" publicUrl={context.publicUrl} />
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
                <div className="flex items-center justify-between gap-2">
                  <FieldLabel htmlFor={`${id}-password`}>Password</FieldLabel>
                  {context.emailEnabled ? (
                    <Link
                      className="text-muted-foreground text-sm underline-offset-4 hover:underline"
                      href="/forgot-password"
                    >
                      Forgot password?
                    </Link>
                  ) : null}
                </div>
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
                {step === "recovery" ? "Recovery code" : "Code"}
              </FieldLabel>
              <Input
                autoComplete="one-time-code"
                autoFocus
                id={`${id}-code`}
                inputMode={step === "recovery" ? "text" : "numeric"}
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

        {step === "email" ? (
          <Button
            disabled={pending}
            onClick={() => switchTo("email")}
            type="button"
            variant="link"
          >
            Send a new code
          </Button>
        ) : null}
        {step !== "password"
          ? otherWays.map((way) => (
              <Button
                disabled={pending}
                key={way.step}
                onClick={() => switchTo(way.step)}
                type="button"
                variant="link"
              >
                {way.label}
              </Button>
            ))
          : null}
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
