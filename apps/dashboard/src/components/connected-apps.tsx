"use client";

import { Bot, Plug } from "lucide-react";
import { useRouter } from "next/navigation";
import { useId, useState, useTransition } from "react";

import { CopyButton } from "@/components/copy-button";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { ConnectedApp } from "@/lib/auth/oauth";
import { dateTimeFormat } from "@/lib/time-zone";

function when(iso: string, timeZone: string) {
  return dateTimeFormat(timeZone, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(iso));
}

/** What an MCP grant lets an app do, in words. */
export function grantLabel(scopes: string[]) {
  if (scopes.includes("mcp:write")) return "Read and change";
  if (scopes.includes("mcp:read")) return "Read only";
  return "Nothing";
}

/** Settings → Connected apps (src/app/settings/connected-apps/page.tsx). */
export function ConnectedApps({
  apps,
  mcpUrl,
  nav,
  timeZone,
}: {
  apps: ConnectedApp[];
  mcpUrl: string;
  nav?: React.ReactNode;
  timeZone: string;
}) {
  const id = useId();
  const router = useRouter();
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const disconnect = (app: ConnectedApp) =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch(
        `/api/account/connected-apps/${encodeURIComponent(app.clientId)}`,
        { method: "DELETE" },
      ).catch(() => null);
      if (!response?.ok && response?.status !== 404) {
        return setError("Could not disconnect that app.");
      }
      router.refresh();
    });

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Connected apps</h1>
        <p className="text-muted-foreground text-sm">
          AI assistants and tools you let use Life OS through MCP. Disconnecting
          one stops it at once.
        </p>
      </header>
      {nav}
      <Card aria-labelledby={`${id}-connect`} role="region">
        <CardHeader>
          <CardTitle className="flex items-center gap-2" id={`${id}-connect`}>
            <Plug aria-hidden="true" className="size-4" />
            Connect an app
          </CardTitle>
          <CardDescription>
            Add Life OS to the app as a remote MCP server with this address. The
            app opens Life OS to ask you, and you choose whether it may only
            read or also change things.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex items-center gap-2 text-sm">
          <code
            className="bg-muted rounded px-2 py-1 break-all"
            id={`${id}-url`}
          >
            {mcpUrl}
          </code>
          <CopyButton label="Copy the address" targetId={`${id}-url`} />
        </CardContent>
      </Card>
      <Card aria-labelledby={`${id}-title`} role="region">
        <CardHeader>
          <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
            <Bot aria-hidden="true" className="size-4" />
            Your connected apps
          </CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3 text-sm">
          {apps.length === 0 ? (
            <p className="text-muted-foreground">No app is connected.</p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {apps.map((app) => (
                <li
                  className="flex items-center justify-between gap-2 px-3 py-2"
                  key={app.clientId}
                >
                  <span className="min-w-0">
                    <span className="flex flex-wrap items-center gap-2 font-medium">
                      {app.name ?? "An unnamed app"}
                      <Badge variant="outline">{grantLabel(app.scopes)}</Badge>
                    </span>
                    <span className="text-muted-foreground block text-xs">
                      Connected {when(app.createdAt, timeZone)}
                      {app.uri ? ` · ${app.uri}` : ""}
                    </span>
                  </span>
                  <Button
                    aria-label={`Disconnect ${app.name ?? "the app"}`}
                    disabled={busy}
                    onClick={() => disconnect(app)}
                    size="sm"
                    type="button"
                    variant="outline"
                  >
                    Disconnect
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
    </div>
  );
}
