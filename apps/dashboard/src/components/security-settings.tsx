"use client";

import {
  KeyRound,
  LockKeyhole,
  LogOut,
  MonitorSmartphone,
  ShieldCheck,
  Smartphone,
  Trash2,
  Unlock,
} from "lucide-react";
import { useRouter } from "next/navigation";
import { type FormEvent, useId, useState, useTransition } from "react";

import { CopyButton } from "@/components/copy-button";
import { QrCode } from "@/components/qr-code";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { AccountInfo, SessionInfo } from "@/lib/auth/account";
import { authClient, authErrorMessage } from "@/lib/auth/client";
import {
  describeUserAgent,
  totpSecret,
  totpUriForUsername,
} from "@/lib/auth/devices";
import { usePasskeysHere } from "@/lib/auth/use-browser-security";
import { dateTimeFormat } from "@/lib/time-zone";

const ROLE_LABEL = { owner: "Owner", admin: "Admin", member: "Member" };

type Passkey = { id: string; name: string | null; createdAt: string | null };

function when(iso: string | null, timeZone: string) {
  if (!iso) return "";
  return dateTimeFormat(timeZone, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(iso));
}

/** Settings → Security (src/app/settings/security/page.tsx). */
export function SecuritySettings({
  accounts,
  info,
  passkeyOrigin,
  passkeys,
  publicUrl,
  sessions,
  timeZone,
  totpEnabled,
  welcome,
}: {
  accounts: number;
  info: AccountInfo;
  passkeyOrigin: string;
  passkeys: Passkey[];
  publicUrl: string;
  sessions: SessionInfo[];
  timeZone: string;
  totpEnabled: boolean;
  welcome: boolean;
}) {
  const [pending, startTransition] = useTransition();
  const router = useRouter();
  const user = info.user;

  const signOut = () =>
    startTransition(async () => {
      await authClient.signOut();
      router.replace("/login");
      router.refresh();
    });

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold tracking-tight">Security</h1>
          {user ? (
            <p className="text-muted-foreground text-sm">
              Signed in as <strong>{user.username ?? user.name}</strong> ·{" "}
              {ROLE_LABEL[user.role]}
            </p>
          ) : null}
        </div>
        <Button
          disabled={pending}
          onClick={signOut}
          type="button"
          variant="outline"
        >
          <LogOut aria-hidden="true" />
          Sign out
        </Button>
      </header>

      {welcome && !info.secondFactor ? (
        <Alert>
          <ShieldCheck aria-hidden="true" />
          <AlertTitle>Your account is ready</AlertTitle>
          <AlertDescription>
            Turn on two-factor sign-in next: with an authenticator app below, or
            a passkey. The owner&apos;s account needs one before the admin pages
            open.
          </AlertDescription>
        </Alert>
      ) : null}

      <TwoFactorCard enabled={totpEnabled} username={user?.username ?? ""} />
      <PasskeysCard
        passkeyOrigin={passkeyOrigin}
        passkeys={passkeys}
        publicUrl={publicUrl}
        timeZone={timeZone}
      />
      <PasswordCard />
      <SessionsCard sessions={sessions} timeZone={timeZone} />
      {user?.role === "owner" ? (
        <LegacyAccessCard accounts={accounts} enabled={info.legacyAccess} />
      ) : null}
    </div>
  );
}

/** A password field and a button, for actions Better Auth asks it for. */
function PasswordPrompt({
  action,
  busy,
  error,
  onCancel,
  onSubmit,
}: {
  action: string;
  busy: boolean;
  error?: string;
  onCancel: () => void;
  onSubmit: (password: string) => void;
}) {
  const id = useId();
  const [password, setPassword] = useState("");
  return (
    <form
      className="grid gap-3"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(password);
      }}
    >
      <Field data-invalid={Boolean(error)}>
        <FieldLabel htmlFor={`${id}-password`}>Your password</FieldLabel>
        <Input
          autoComplete="current-password"
          autoFocus
          id={`${id}-password`}
          onChange={(event) => setPassword(event.target.value)}
          type="password"
          value={password}
        />
        <FieldError>{error}</FieldError>
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button disabled={busy || !password} type="submit">
          {busy ? "Working…" : action}
        </Button>
        <Button
          disabled={busy}
          onClick={onCancel}
          type="button"
          variant="outline"
        >
          Cancel
        </Button>
      </div>
    </form>
  );
}

function RecoveryCodes({ codes }: { codes: string[] }) {
  const id = useId();
  return (
    <div className="grid gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm font-medium">Recovery codes</p>
        <CopyButton
          label="Copy the recovery codes"
          targetId={`${id}-codes`}
          text={codes.join("\n")}
        />
      </div>
      <ul
        className="bg-muted grid grid-cols-2 gap-x-4 gap-y-1 rounded-lg p-3 font-mono text-sm"
        id={`${id}-codes`}
      >
        {codes.map((code) => (
          <li key={code}>{code}</li>
        ))}
      </ul>
      <p className="text-muted-foreground text-xs">
        Keep them somewhere safe, such as a password manager. Each signs you in
        once if your phone is not at hand. They are shown only now.
      </p>
    </div>
  );
}

type Enrolment =
  | { stage: "idle" }
  | { stage: "password" }
  | { stage: "scan"; uri: string; codes: string[] }
  | { stage: "codes"; codes: string[] }
  | { stage: "disable" }
  | { stage: "regenerate" };

function TwoFactorCard({
  enabled,
  username,
}: {
  enabled: boolean;
  username: string;
}) {
  const id = useId();
  const router = useRouter();
  const [state, setState] = useState<Enrolment>({ stage: "idle" });
  const [code, setCode] = useState("");
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const reset = () => {
    setError(undefined);
    setCode("");
    setState({ stage: "idle" });
  };

  const start = (password: string) =>
    startTransition(async () => {
      setError(undefined);
      const { data, error } = await authClient.twoFactor.enable({ password });
      if (error || !data || !("totpURI" in data)) {
        return setError(authErrorMessage(error, "Could not start."));
      }
      setState({
        stage: "scan",
        uri: totpUriForUsername(data.totpURI, username),
        codes: data.backupCodes,
      });
    });

  const confirm = (event: FormEvent) => {
    event.preventDefault();
    if (state.stage !== "scan") return;
    startTransition(async () => {
      setError(undefined);
      const { error } = await authClient.twoFactor.verifyTotp({
        code: code.trim(),
      });
      if (error)
        return setError(authErrorMessage(error, "Could not check it."));
      setState({ stage: "codes", codes: state.codes });
      router.refresh();
    });
  };

  const disable = (password: string) =>
    startTransition(async () => {
      setError(undefined);
      const { error } = await authClient.twoFactor.disable({ password });
      if (error)
        return setError(authErrorMessage(error, "Could not turn it off."));
      reset();
      router.refresh();
    });

  const regenerate = (password: string) =>
    startTransition(async () => {
      setError(undefined);
      const { data, error } = await authClient.twoFactor.generateBackupCodes({
        password,
      });
      if (error || !data) {
        return setError(authErrorMessage(error, "Could not make new codes."));
      }
      setState({ stage: "codes", codes: data.backupCodes });
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <Smartphone aria-hidden="true" className="size-4" />
          Authenticator app
        </CardTitle>
        <CardDescription>
          After your password, Life OS asks for the 6-digit code an app such as
          Google Authenticator, Aegis, or 1Password shows.
        </CardDescription>
        <CardAction>
          <Badge variant={enabled ? "default" : "outline"}>
            {enabled ? "On" : "Off"}
          </Badge>
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {state.stage === "idle" ? (
          enabled ? (
            <div className="flex flex-wrap gap-2">
              <Button
                onClick={() => setState({ stage: "regenerate" })}
                type="button"
                variant="outline"
              >
                New recovery codes
              </Button>
              <Button
                onClick={() => setState({ stage: "disable" })}
                type="button"
                variant="outline"
              >
                Turn off
              </Button>
            </div>
          ) : (
            <div>
              <Button
                onClick={() => setState({ stage: "password" })}
                type="button"
              >
                Turn on
              </Button>
            </div>
          )
        ) : null}

        {state.stage === "password" ? (
          <PasswordPrompt
            action="Continue"
            busy={busy}
            error={error}
            onCancel={reset}
            onSubmit={start}
          />
        ) : null}
        {state.stage === "disable" ? (
          <PasswordPrompt
            action="Turn off"
            busy={busy}
            error={error}
            onCancel={reset}
            onSubmit={disable}
          />
        ) : null}
        {state.stage === "regenerate" ? (
          <PasswordPrompt
            action="Make new codes"
            busy={busy}
            error={error}
            onCancel={reset}
            onSubmit={regenerate}
          />
        ) : null}

        {state.stage === "scan" ? (
          <form className="grid gap-3" noValidate onSubmit={confirm}>
            <p>
              Scan this code with your authenticator app, or type the key in it.
              Then enter the code the app shows.
            </p>
            <QrCode
              className="size-44 rounded-lg border"
              label="QR code for the authenticator app"
              text={state.uri}
            />
            <div className="flex items-center gap-2">
              <code
                className="bg-muted rounded px-2 py-1 font-mono text-xs"
                id={`${id}-secret`}
              >
                {totpSecret(state.uri)}
              </code>
              <CopyButton
                label="Copy the key"
                targetId={`${id}-secret`}
                text={totpSecret(state.uri).replace(/ /g, "")}
              />
            </div>
            <Field data-invalid={Boolean(error)}>
              <FieldLabel htmlFor={`${id}-code`}>Code from the app</FieldLabel>
              <Input
                autoComplete="one-time-code"
                className="max-w-40"
                id={`${id}-code`}
                inputMode="numeric"
                onChange={(event) => setCode(event.target.value)}
                value={code}
              />
              <FieldError>{error}</FieldError>
            </Field>
            <div className="flex flex-wrap gap-2">
              <Button disabled={busy || code.trim().length < 6} type="submit">
                {busy ? "Checking…" : "Turn on"}
              </Button>
              <Button
                disabled={busy}
                onClick={reset}
                type="button"
                variant="outline"
              >
                Cancel
              </Button>
            </div>
          </form>
        ) : null}

        {state.stage === "codes" ? (
          <div className="grid gap-3">
            <RecoveryCodes codes={state.codes} />
            <div>
              <Button onClick={reset} type="button">
                Done
              </Button>
            </div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function PasskeysCard({
  passkeyOrigin,
  passkeys,
  publicUrl,
  timeZone,
}: {
  passkeyOrigin: string;
  passkeys: Passkey[];
  publicUrl: string;
  timeZone: string;
}) {
  const id = useId();
  const router = useRouter();
  const [name, setName] = useState("");
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();
  const here = usePasskeysHere(passkeyOrigin);

  const add = (event: FormEvent) => {
    event.preventDefault();
    startTransition(async () => {
      setError(undefined);
      const result = await authClient.passkey.addPasskey({
        name: name.trim() || "Passkey",
      });
      if (result?.error) {
        return setError(
          authErrorMessage(result.error, "The passkey was not added."),
        );
      }
      setName("");
      router.refresh();
    });
  };

  const remove = (passkey: Passkey) =>
    startTransition(async () => {
      setError(undefined);
      const { error } = await authClient.passkey.deletePasskey({
        id: passkey.id,
      });
      if (error)
        return setError(authErrorMessage(error, "Could not remove it."));
      router.refresh();
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <KeyRound aria-hidden="true" className="size-4" />
          Passkeys
        </CardTitle>
        <CardDescription>
          Sign in with your phone&apos;s or computer&apos;s screen lock instead
          of a password and code.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {passkeys.length ? (
          <ul className="divide-y rounded-lg border">
            {passkeys.map((passkey) => (
              <li
                className="flex items-center justify-between gap-2 px-3 py-2"
                key={passkey.id}
              >
                <span className="min-w-0">
                  <span className="block truncate font-medium">
                    {passkey.name || "Passkey"}
                  </span>
                  {passkey.createdAt ? (
                    <span className="text-muted-foreground text-xs">
                      Added {when(passkey.createdAt, timeZone)}
                    </span>
                  ) : null}
                </span>
                <Button
                  aria-label={`Remove ${passkey.name || "the passkey"}`}
                  disabled={busy}
                  onClick={() => remove(passkey)}
                  size="icon-sm"
                  type="button"
                  variant="ghost"
                >
                  <Trash2 aria-hidden="true" />
                </Button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground">No passkeys yet.</p>
        )}
        {here === false ? (
          <p className="text-muted-foreground">
            Passkeys work at{" "}
            <a className="underline" href={`${publicUrl}/settings/security`}>
              {publicUrl}
            </a>
            : add one there.
          </p>
        ) : here ? (
          <form
            className="flex flex-wrap items-end gap-2"
            noValidate
            onSubmit={add}
          >
            <Field className="w-56">
              <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
              <Input
                id={`${id}-name`}
                onChange={(event) => setName(event.target.value)}
                placeholder="Such as Pixel or MacBook"
                value={name}
              />
            </Field>
            <Button disabled={busy} type="submit">
              {busy ? "Waiting for the device…" : "Add a passkey"}
            </Button>
          </form>
        ) : null}
        {error ? (
          <p className="text-destructive" role="alert">
            {error}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function PasswordCard() {
  const id = useId();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<
    Partial<Record<"next" | "confirm" | "form", string>>
  >({});
  const [notice, setNotice] = useState<string>();
  const [busy, startTransition] = useTransition();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const found: typeof errors = {};
    if (next.length < 10) found.next = "At least 10 characters.";
    if (confirm !== next) found.confirm = "The two passwords differ.";
    setErrors(found);
    setNotice(undefined);
    if (Object.keys(found).length) return;
    startTransition(async () => {
      const { error } = await authClient.changePassword({
        currentPassword: current,
        newPassword: next,
        revokeOtherSessions: true,
      });
      if (error) {
        return setErrors({
          form: authErrorMessage(error, "Could not change the password."),
        });
      }
      setCurrent("");
      setNext("");
      setConfirm("");
      setNotice("Password changed. Your other sessions were signed out.");
    });
  };

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <LockKeyhole aria-hidden="true" className="size-4" />
          Password
        </CardTitle>
        <CardDescription>
          Changing it signs out every other session.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid max-w-sm gap-3" noValidate onSubmit={submit}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={`${id}-current`}>
                Current password
              </FieldLabel>
              <Input
                autoComplete="current-password"
                id={`${id}-current`}
                onChange={(event) => setCurrent(event.target.value)}
                type="password"
                value={current}
              />
            </Field>
            <Field data-invalid={Boolean(errors.next)}>
              <FieldLabel htmlFor={`${id}-next`}>New password</FieldLabel>
              <Input
                autoComplete="new-password"
                id={`${id}-next`}
                onChange={(event) => setNext(event.target.value)}
                type="password"
                value={next}
              />
              <FieldDescription className="text-xs">
                At least 10 characters.
              </FieldDescription>
              <FieldError>{errors.next}</FieldError>
            </Field>
            <Field data-invalid={Boolean(errors.confirm)}>
              <FieldLabel htmlFor={`${id}-confirm`}>
                New password again
              </FieldLabel>
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
          {notice ? (
            <p className="text-sm" role="status">
              {notice}
            </p>
          ) : null}
          <div>
            <Button disabled={busy || !current} type="submit">
              {busy ? "Saving…" : "Change the password"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function SessionsCard({
  sessions,
  timeZone,
}: {
  sessions: SessionInfo[];
  timeZone: string;
}) {
  const id = useId();
  const router = useRouter();
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();
  const others = sessions.filter((session) => !session.current).length;

  const revoke = (session: SessionInfo) =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch(`/api/account/sessions/${session.id}`, {
        method: "DELETE",
      }).catch(() => null);
      if (!response?.ok && response?.status !== 404) {
        return setError("Could not sign that session out.");
      }
      if (session.current) router.replace("/login");
      router.refresh();
    });

  const revokeOthers = () =>
    startTransition(async () => {
      setError(undefined);
      const { error } = await authClient.revokeOtherSessions();
      if (error)
        return setError(authErrorMessage(error, "Could not sign them out."));
      router.refresh();
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <MonitorSmartphone aria-hidden="true" className="size-4" />
          Where you are signed in
        </CardTitle>
        <CardDescription>
          A session lasts 30 days from its last use.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        <ul className="divide-y rounded-lg border">
          {sessions.map((session) => (
            <li
              className="flex items-center justify-between gap-2 px-3 py-2"
              key={session.id}
            >
              <span className="min-w-0">
                <span className="flex items-center gap-2 font-medium">
                  {describeUserAgent(session.userAgent)}
                  {session.current ? (
                    <Badge variant="secondary">This device</Badge>
                  ) : null}
                </span>
                <span className="text-muted-foreground block text-xs">
                  Last used {when(session.updatedAt, timeZone)}
                  {session.ipAddress ? ` · ${session.ipAddress}` : ""}
                </span>
              </span>
              <Button
                disabled={busy}
                onClick={() => revoke(session)}
                size="sm"
                type="button"
                variant="outline"
              >
                Sign out
              </Button>
            </li>
          ))}
        </ul>
        {others > 0 ? (
          <div>
            <Button
              disabled={busy}
              onClick={revokeOthers}
              type="button"
              variant="outline"
            >
              Sign out everywhere else
            </Button>
          </div>
        ) : null}
        {error ? (
          <p className="text-destructive" role="alert">
            {error}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function LegacyAccessCard({
  accounts,
  enabled,
}: {
  accounts: number;
  enabled: boolean;
}) {
  const id = useId();
  const router = useRouter();
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  // Only off from here: turning it on turns sign-in off for the whole
  // server, which only the host may do (./setup.sh legacy-access on).
  const turnOff = () =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch("/api/account/legacy-access", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ enabled: false }),
      }).catch(() => null);
      if (!response?.ok) {
        const body = await response?.json().catch(() => null);
        return setError(
          body?.error?.message ?? "Could not change legacy access.",
        );
      }
      router.refresh();
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <Unlock aria-hidden="true" className="size-4" />
          Legacy access
        </CardTitle>
        <CardDescription>
          While it is on, anyone who reaches Life OS without signing in acts as
          you, as before accounts existed. The phone and watch apps need it
          until they are paired with your account.
        </CardDescription>
        <CardAction>
          <Badge variant={enabled ? "destructive" : "outline"}>
            {enabled ? "On" : "Off"}
          </Badge>
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        <Label className="w-fit">
          <Switch
            checked={enabled}
            disabled={busy || !enabled}
            onCheckedChange={(checked) => {
              if (!checked) turnOff();
            }}
          />
          Allow access without signing in
        </Label>
        {enabled ? null : (
          <p className="text-muted-foreground">
            {accounts > 1 ? (
              "It stays off while more than one account exists."
            ) : (
              <>
                To turn it back on, run <code>./setup.sh legacy-access on</code>{" "}
                on the server.
              </>
            )}
          </p>
        )}
        {error ? (
          <p className="text-destructive" role="alert">
            {error}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}
