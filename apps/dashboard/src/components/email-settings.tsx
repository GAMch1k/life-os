"use client";

import { BellRing, Mail, Send } from "lucide-react";
import { useRouter } from "next/navigation";
import { type FormEvent, useId, useState, useTransition } from "react";

import { ReauthPrompt } from "@/components/reauth-prompt";
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
  FieldContent,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  ALERT_KINDS,
  ALERT_LABELS,
  type AlertKind,
} from "@/lib/auth/email-kinds";

type ApiError = { error?: { code?: string; message?: string } };

async function call(path: string, method: string, body?: unknown) {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? {} : { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).catch(() => null);
  const data = (await response?.json().catch(() => null)) as
    (ApiError & Record<string, unknown>) | null;
  return { ok: response?.ok === true, status: response?.status ?? 0, data };
}

/** Settings → Email (src/app/settings/email/page.tsx, docs/install/email.md). */
export function EmailSettings({
  address,
  alerts,
  nav,
  recentAuth,
  signInCodes,
  verified,
}: {
  address: string | null;
  alerts: Record<AlertKind, boolean>;
  nav?: React.ReactNode;
  recentAuth: boolean;
  signInCodes: boolean;
  verified: boolean;
}) {
  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Email</h1>
        <p className="text-muted-foreground text-sm">
          Life OS emails your verified address a link when you forget your
          password, sign-in codes if you turn them on, and the security alerts
          below.
        </p>
      </header>
      {nav}
      <AddressCard
        address={address}
        recentAuth={recentAuth}
        signInCodes={signInCodes}
        verified={verified}
      />
      <AlertsCard alerts={alerts} verified={verified} />
    </div>
  );
}

function AddressCard({
  address,
  recentAuth,
  signInCodes,
  verified,
}: {
  address: string | null;
  recentAuth: boolean;
  signInCodes: boolean;
  verified: boolean;
}) {
  const id = useId();
  const router = useRouter();
  const [recent, setRecent] = useState(recentAuth);
  const [editing, setEditing] = useState(address === null);
  const [value, setValue] = useState(address ?? "");
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [busy, startTransition] = useTransition();

  const save = (event: FormEvent) => {
    event.preventDefault();
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      const { ok, data } = await call("/api/account/email", "PUT", {
        address: value.trim() || null,
      });
      if (data?.error?.code === "reauth_required") return setRecent(false);
      if (!ok) {
        return setError(data?.error?.message ?? "Could not save the address.");
      }
      setEditing(false);
      setNotice(
        data?.linkSent === true
          ? "Saved. A verification link is on its way to the new address."
          : "Saved.",
      );
      router.refresh();
    });
  };

  const sendLink = () =>
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      const { ok, data } = await call(
        "/api/account/email/verification",
        "POST",
      );
      if (!ok) {
        return setError(data?.error?.message ?? "Could not send the link.");
      }
      setNotice(
        "A verification link is on its way. It works once, for 24 hours.",
      );
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <Mail aria-hidden="true" className="size-4" />
          Your email address
        </CardTitle>
        <CardDescription>
          Only Life OS&apos;s own emails go there; you never sign in with it.
        </CardDescription>
        {address ? (
          <CardAction>
            <Badge variant={verified ? "default" : "outline"}>
              {verified ? "Verified" : "Not verified"}
            </Badge>
          </CardAction>
        ) : null}
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {address && !editing ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <span className="font-medium break-all">{address}</span>
            {verified ? null : (
              <span className="text-muted-foreground">
                not verified ·{" "}
                <Button
                  className="h-auto p-0"
                  disabled={busy}
                  onClick={sendLink}
                  type="button"
                  variant="link"
                >
                  Send link
                </Button>
              </span>
            )}
          </div>
        ) : null}

        {notice ? (
          <p className="text-muted-foreground" role="status">
            {notice}
          </p>
        ) : null}
        {error && !editing ? (
          <p className="text-destructive" role="alert">
            {error}
          </p>
        ) : null}

        {editing ? (
          <>
            {recent ? null : (
              <ReauthPrompt
                description="Your address receives reset links, so changing it needs a sign-in from the last 12 hours, or your password."
                onConfirmed={() => setRecent(true)}
              />
            )}
            <form
              className="grid gap-3"
              hidden={!recent}
              noValidate
              onSubmit={save}
            >
              <Field data-invalid={Boolean(error)}>
                <FieldLabel htmlFor={`${id}-address`}>Email address</FieldLabel>
                <Input
                  autoComplete="email"
                  id={`${id}-address`}
                  inputMode="email"
                  onChange={(event) => setValue(event.target.value)}
                  placeholder="you@example.com"
                  spellCheck={false}
                  type="email"
                  value={value}
                />
                <FieldDescription>
                  Life OS sends a link to verify it. Until then it gets nothing
                  else.
                </FieldDescription>
                <FieldError>{error}</FieldError>
              </Field>
              <div className="flex flex-wrap gap-2">
                <Button disabled={busy || !value.trim()} type="submit">
                  {busy ? "Saving…" : "Save"}
                </Button>
                {address ? (
                  <Button
                    disabled={busy}
                    onClick={() => {
                      setEditing(false);
                      setValue(address);
                      setError(undefined);
                    }}
                    type="button"
                    variant="outline"
                  >
                    Cancel
                  </Button>
                ) : null}
              </div>
            </form>
          </>
        ) : (
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={busy || signInCodes}
              onClick={() => {
                setEditing(true);
                setNotice(undefined);
              }}
              type="button"
              variant="outline"
            >
              Change
            </Button>
          </div>
        )}
        {signInCodes && !editing ? (
          <p className="text-muted-foreground">
            Your sign-in codes go to this address: to change it, turn them off
            in Settings → Security first.
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function AlertsCard({
  alerts,
  verified,
}: {
  alerts: Record<AlertKind, boolean>;
  verified: boolean;
}) {
  const id = useId();
  const [values, setValues] = useState(alerts);
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const toggle = (kind: AlertKind, on: boolean) =>
    startTransition(async () => {
      setError(undefined);
      setValues((current) => ({ ...current, [kind]: on }));
      const { ok, data } = await call("/api/account/email/preferences", "PUT", {
        alerts: { [kind]: on },
      });
      if (!ok) {
        setValues((current) => ({ ...current, [kind]: !on }));
        return setError(data?.error?.message ?? "Could not save it.");
      }
      if (data?.alerts) setValues(data.alerts as Record<AlertKind, boolean>);
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <BellRing aria-hidden="true" className="size-4" />
          Security alerts
        </CardTitle>
        <CardDescription>
          Emails when something changes in how your account signs in. Each kind
          turns off here; emails you ask for yourself (a verification link, a
          reset link, a sign-in code) always go out.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 text-sm">
        {verified ? null : (
          <p className="text-muted-foreground flex items-center gap-2">
            <Send aria-hidden="true" className="size-4" />
            They go out once your address is verified.
          </p>
        )}
        {ALERT_KINDS.map((kind) => (
          <Field key={kind} orientation="horizontal">
            <FieldContent>
              <FieldLabel htmlFor={`${id}-${kind}`}>
                {ALERT_LABELS[kind].label}
              </FieldLabel>
              <FieldDescription>
                {ALERT_LABELS[kind].description}
              </FieldDescription>
            </FieldContent>
            <Switch
              checked={values[kind]}
              disabled={busy}
              id={`${id}-${kind}`}
              onCheckedChange={(checked) => toggle(kind, checked)}
            />
          </Field>
        ))}
        {error ? (
          <p className="text-destructive" role="alert">
            {error}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}
