"use client";

import { KeySquare, Plus, Terminal } from "lucide-react";
import { useRouter } from "next/navigation";
import { type FormEvent, useId, useState, useTransition } from "react";

import { CopyButton } from "@/components/copy-button";
import { DatePicker } from "@/components/date-picker";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import type { TokenInfo } from "@/lib/auth/account-credentials";
import { dateToDay } from "@/lib/date-picker";
import { dateTimeFormat } from "@/lib/time-zone";

/** The modules a token's scopes name, as Settings calls them. */
const MODULES = [
  { id: "tasks", label: "Tasks" },
  { id: "calendar", label: "Calendar" },
  { id: "inbox", label: "Inbox" },
  { id: "food", label: "Food" },
  { id: "finance", label: "Finance" },
  { id: "voice", label: "Voice notes" },
  { id: "notifications", label: "Notifications" },
  { id: "activity", label: "Event Log", readOnly: true },
] as const;

type Access = "none" | "read" | "write";

const DAY_MS = 24 * 60 * 60 * 1000;

function dayFromNow(days: number) {
  return dateToDay(new Date(Date.now() + days * DAY_MS));
}

function when(iso: string | null, timeZone: string, time = true) {
  if (!iso) return "";
  return dateTimeFormat(timeZone, {
    dateStyle: "medium",
    ...(time ? { timeStyle: "short" as const } : {}),
  }).format(new Date(iso));
}

function scopeLabel(scope: string) {
  const [module, level] = scope.split(":");
  const name = MODULES.find((m) => m.id === module)?.label ?? module;
  return level === "write" ? `${name}: read and write` : `${name}: read`;
}

type Created = { token: string; name: string; actor: string | null };

/** Settings → API tokens (src/app/settings/tokens/page.tsx). */
export function TokensSettings({
  nav,
  publicUrl,
  timeZone,
  tokens,
}: {
  nav?: React.ReactNode;
  publicUrl: string;
  timeZone: string;
  tokens: TokenInfo[];
}) {
  const [created, setCreated] = useState<Created>();
  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">API tokens</h1>
        <p className="text-muted-foreground text-sm">
          For your own scripts and development tools: a token can do only what
          its scopes allow, until it expires. It can never manage your account,
          devices, or tokens.
        </p>
      </header>
      {nav}
      {created ? (
        <NewToken
          created={created}
          onDone={() => setCreated(undefined)}
          publicUrl={publicUrl}
        />
      ) : (
        <CreateTokenCard onCreated={setCreated} />
      )}
      <TokenList timeZone={timeZone} tokens={tokens} />
    </div>
  );
}

function NewToken({
  created,
  onDone,
  publicUrl,
}: {
  created: Created;
  onDone: () => void;
  publicUrl: string;
}) {
  const id = useId();
  return (
    <Alert>
      <KeySquare aria-hidden="true" />
      <AlertTitle>Copy the token “{created.name}” now</AlertTitle>
      <AlertDescription className="grid gap-2">
        <p>It is shown only this once. Keep it like a password.</p>
        <p className="flex items-center gap-2">
          <code
            className="bg-muted rounded px-2 py-1 font-mono text-xs break-all"
            id={`${id}-token`}
          >
            {created.token}
          </code>
          <CopyButton label="Copy the token" targetId={`${id}-token`} />
        </p>
        <p className="text-muted-foreground">
          Send it as <code>Authorization: Bearer …</code> to{" "}
          <code>{publicUrl}/api/…</code>.
          {created.actor
            ? ` Its changes show in the Event Log as ${created.actor}.`
            : ""}
        </p>
        <div>
          <Button onClick={onDone} size="sm" type="button" variant="outline">
            Done
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}

function CreateTokenCard({
  onCreated,
}: {
  onCreated: (created: Created) => void;
}) {
  const id = useId();
  const router = useRouter();
  const [name, setName] = useState("");
  const [access, setAccess] = useState<Record<string, Access>>({});
  const [expires, setExpires] = useState(() => dayFromNow(90));
  const [devTool, setDevTool] = useState(false);
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const scopes = MODULES.flatMap((module) => {
    const level = access[module.id] ?? "none";
    return level === "none" ? [] : [`${module.id}:${level}`];
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    startTransition(async () => {
      setError(undefined);
      // The end of the chosen day, in this browser's time.
      const end = new Date(`${expires}T23:59:59`);
      const response = await fetch("/api/account/tokens", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          name,
          scopes,
          expiresAt: end.toISOString(),
          devTool,
        }),
      }).catch(() => null);
      const body = (await response?.json().catch(() => null)) as {
        token?: string;
        name?: string;
        actor?: string | null;
        error?: { message?: string };
      } | null;
      if (!response?.ok || !body?.token) {
        return setError(body?.error?.message ?? "Could not make the token.");
      }
      setName("");
      setAccess({});
      setDevTool(false);
      setExpires(dayFromNow(90));
      onCreated({
        token: body.token,
        name: body.name ?? name,
        actor: body.actor ?? null,
      });
      router.refresh();
    });
  };

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <Plus aria-hidden="true" className="size-4" />
          New token
        </CardTitle>
        <CardDescription>
          Give it only the access it needs. It expires after 90 days unless you
          choose another day, at most a year from now.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" noValidate onSubmit={submit}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
              <Input
                id={`${id}-name`}
                maxLength={64}
                onChange={(event) => setName(event.target.value)}
                placeholder="claude-code"
                required
                value={name}
              />
            </Field>
            <Field>
              <FieldLabel>Access</FieldLabel>
              <div className="grid gap-2 sm:grid-cols-2">
                {MODULES.map((module) => (
                  <Label
                    className="flex items-center justify-between gap-2 font-normal"
                    key={module.id}
                  >
                    {module.label}
                    <NativeSelect
                      aria-label={`${module.label} access`}
                      onChange={(event) =>
                        setAccess((current) => ({
                          ...current,
                          [module.id]: event.target.value as Access,
                        }))
                      }
                      size="sm"
                      value={access[module.id] ?? "none"}
                    >
                      <NativeSelectOption value="none">None</NativeSelectOption>
                      <NativeSelectOption value="read">Read</NativeSelectOption>
                      {"readOnly" in module ? null : (
                        <NativeSelectOption value="write">
                          Read and write
                        </NativeSelectOption>
                      )}
                    </NativeSelect>
                  </Label>
                ))}
              </div>
            </Field>
            <Field>
              <FieldLabel htmlFor={`${id}-expires`}>Expires</FieldLabel>
              <DatePicker
                id={`${id}-expires`}
                label="Expires"
                max={dayFromNow(365)}
                min={dayFromNow(1)}
                onChange={setExpires}
                required
                value={expires}
              />
            </Field>
            <Field orientation="horizontal">
              <Switch
                checked={devTool}
                id={`${id}-dev`}
                onCheckedChange={setDevTool}
              />
              <div className="grid gap-1">
                <FieldLabel htmlFor={`${id}-dev`}>
                  A development tool
                </FieldLabel>
                <FieldDescription>
                  Its changes show in the Event Log as the tool (dev:
                  {name.trim() || "name"}), not as you.
                </FieldDescription>
              </div>
            </Field>
          </FieldGroup>
          {error ? (
            <p className="text-destructive text-sm" role="alert">
              {error}
            </p>
          ) : null}
          <div>
            <Button
              disabled={busy || !name.trim() || scopes.length === 0}
              type="submit"
            >
              Make the token
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function TokenList({
  timeZone,
  tokens,
}: {
  timeZone: string;
  tokens: TokenInfo[];
}) {
  const id = useId();
  const router = useRouter();
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const revoke = (token: TokenInfo) =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch(`/api/account/tokens/${token.id}`, {
        method: "DELETE",
      }).catch(() => null);
      if (!response?.ok && response?.status !== 404) {
        return setError("Could not revoke that token.");
      }
      router.refresh();
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <KeySquare aria-hidden="true" className="size-4" />
          Your tokens
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {tokens.length === 0 ? (
          <p className="text-muted-foreground">You have no API tokens.</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {tokens.map((token) => (
              <li
                className="flex items-start justify-between gap-2 px-3 py-2"
                key={token.id}
              >
                <span className="grid min-w-0 gap-1">
                  <span className="flex flex-wrap items-center gap-2 font-medium">
                    {token.name}
                    {token.start ? (
                      <code className="text-muted-foreground text-xs">
                        {token.start}…
                      </code>
                    ) : null}
                    {token.devTool ? (
                      <Badge variant="secondary">
                        <Terminal aria-hidden="true" />
                        {token.actor}
                      </Badge>
                    ) : null}
                    {token.expired ? (
                      <Badge variant="destructive">Expired</Badge>
                    ) : null}
                  </span>
                  <span className="text-muted-foreground text-xs">
                    {token.scopes.map(scopeLabel).join(" · ")}
                  </span>
                  <span className="text-muted-foreground text-xs">
                    Made {when(token.createdAt, timeZone, false)}
                    {token.expiresAt
                      ? ` · expires ${when(token.expiresAt, timeZone, false)}`
                      : ""}
                    {" · "}
                    {token.lastUsedAt
                      ? `last used ${when(token.lastUsedAt, timeZone)}`
                      : "not used yet"}
                  </span>
                </span>
                <Button
                  aria-label={`Revoke ${token.name}`}
                  disabled={busy}
                  onClick={() => revoke(token)}
                  size="sm"
                  type="button"
                  variant="outline"
                >
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
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
