"use client";

import { Bot } from "lucide-react";
import { useId, useState, useTransition } from "react";

import { AccountPage } from "@/components/account-page";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import type { ConsentRequest } from "@/lib/auth/oauth-pages";

/**
 * Consent for an MCP client (src/app/oauth/consent/page.tsx): who asks, as
 * whom, and what for (reading, and changing); Allow or Deny posts the
 * signed request to the OAuth provider, which answers where the browser
 * goes next: back to the app's registered redirect address.
 */
export function OAuthConsent({
  request,
  username,
}: {
  request: ConsentRequest;
  username: string;
}) {
  const id = useId();
  const asked = request.ok && request.scopes.length ? request.scopes : [];
  const [write, setWrite] = useState(asked.includes("mcp:write"));
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();

  if (!request.ok) {
    return (
      <AccountPage title="Connect an app">
        <Alert variant="destructive">
          <AlertTitle>This request is not valid any more</AlertTitle>
          <AlertDescription>
            It expired, or it was changed on the way. Go back to the app and
            connect it to Life OS again.
          </AlertDescription>
        </Alert>
      </AccountPage>
    );
  }

  const name = request.clientName?.trim() || "An app";
  const canWrite = asked.length === 0 || asked.includes("mcp:write");
  const canRead = asked.length === 0 || asked.includes("mcp:read");

  // What is accepted: a subset of what the app asked for (the provider
  // refuses anything else); when it named no scope, everything it may ask.
  const scope =
    asked.length === 0
      ? null
      : [
          ...(canRead ? ["mcp:read"] : []),
          ...(write && canWrite ? ["mcp:write"] : []),
        ];
  const answer = (accept: boolean) =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch("/api/auth/oauth2/consent", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          accept,
          oauth_query: request.oauthQuery,
          ...(accept && scope
            ? {
                scope: [
                  ...scope,
                  ...(request.offlineAccess ? ["offline_access"] : []),
                ].join(" "),
              }
            : {}),
        }),
      }).catch(() => null);
      const body = (await response?.json().catch(() => null)) as {
        url?: string;
        redirect?: boolean;
        message?: string;
        error_description?: string;
      } | null;
      if (!response?.ok || typeof body?.url !== "string") {
        return setError(
          body?.error_description ??
            body?.message ??
            "Could not answer the app. Go back to it and connect again.",
        );
      }
      window.location.assign(body.url);
    });

  return (
    <AccountPage
      description={`Signed in as ${username}`}
      title={`Connect ${name} to Life OS?`}
    >
      <p className="flex items-start gap-2 text-sm">
        <Bot aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
        <span>
          <strong>{name}</strong>
          {request.clientUri ? ` (${request.clientUri})` : ""} asks to use your
          Life OS through MCP: your tasks, calendar, inbox, food, and finance.
          It answers to <strong>{request.redirectHost}</strong>.
        </span>
      </p>
      <ul className="text-sm">
        {canRead ? <li>Read what is in Life OS</li> : null}
      </ul>
      {canWrite ? (
        <Label
          className="flex items-start gap-2 font-normal"
          htmlFor={`${id}-write`}
        >
          <Checkbox
            checked={write}
            id={`${id}-write`}
            onCheckedChange={(checked) => setWrite(checked === true)}
          />
          <span>
            Also add and change things (tasks, events, shopping, and the rest)
          </span>
        </Label>
      ) : null}
      <p className="text-muted-foreground text-xs">
        You can disconnect it at any time in Settings → Connected apps. Only
        connect an app you just added yourself.
      </p>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <Button
        disabled={pending || (scope !== null && scope.length === 0)}
        onClick={() => answer(true)}
        type="button"
      >
        Allow
      </Button>
      <Button
        disabled={pending}
        onClick={() => answer(false)}
        type="button"
        variant="outline"
      >
        Deny
      </Button>
    </AccountPage>
  );
}
