"use client";

import { AlertTriangle, CheckCircle2, Landmark } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { useEffect, useState, useTransition } from "react";

import { BANKING_UNAVAILABLE } from "@/components/bank-callback";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { bankingClient } from "@/lib/api/banking-client";
import {
  accountLabel,
  BANK_CONNECTED,
  BANK_PARAM,
  BANKING_SETTINGS,
  bankFailure,
  type BankConnection,
  type BankConnectionList,
  bankState,
  type BankState,
  consentText,
  DEFAULT_BANK,
  liveConnection,
  needsReconnect,
  nextSyncText,
  syncNowState,
  syncText,
  withoutBankParam,
} from "@/lib/banking";
import { cn } from "@/lib/utils";

const BADGES: Partial<
  Record<
    BankState,
    { label: string; variant: "secondary" | "outline" | "destructive" }
  >
> = {
  connected: { label: "Connected", variant: "secondary" },
  pending: { label: "Importing", variant: "outline" },
  expiring: { label: "Expires soon", variant: "destructive" },
  expired: { label: "Expired", variant: "destructive" },
  error: { label: "Needs attention", variant: "destructive" },
};

/**
 * The Finance page's bank connection card: connect Deutsche Bank (read-only,
 * through the banking integration), see the connected accounts with their
 * masked IBANs, the last and next sync and how long the consent lasts, sync
 * now, disconnect, or reconnect when the consent expires. Back from the
 * bank (`/finance?bank=connected`) it confirms the connection once. Times
 * are shown in timeZone (the browser's, else the owner's).
 */
export function BankConnections({
  timeZone,
  onChanged,
}: {
  timeZone: string;
  /** Called after a sync or a disconnect changed Finance's accounts. */
  onChanged?: () => void;
}) {
  const justConnected = useSearchParams().get(BANK_PARAM) === BANK_CONNECTED;
  const [list, setList] = useState<BankConnectionList>();
  const [failed, setFailed] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState(
    justConnected ? `${DEFAULT_BANK} is connected.` : undefined,
  );
  const [confirming, setConfirming] = useState(false);
  const [opening, setOpening] = useState(false);
  const [working, startTransition] = useTransition();
  const [now, setNow] = useState(() => new Date());
  const pending = working || opening;

  // The marker is shown once: a reload does not repeat it.
  useEffect(() => {
    if (!justConnected) return;
    window.history.replaceState(
      window.history.state,
      "",
      `${window.location.pathname}${withoutBankParam(window.location.search)}`,
    );
  }, [justConnected]);

  // Back from the bank with the browser's back button, the page may be
  // restored as it was left: ready to connect again.
  useEffect(() => {
    const restored = (event: PageTransitionEvent) => {
      if (event.persisted) setOpening(false);
    };
    window.addEventListener("pageshow", restored);
    return () => window.removeEventListener("pageshow", restored);
  }, []);

  // "Sync now" becomes available again as time passes.
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 30_000);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    let current = true;
    bankingClient
      .GET("/v1/connections")
      .then(({ data }) => {
        if (!current) return;
        if (data) setList(data);
        else setFailed(true);
      })
      .catch(() => {
        if (current) setFailed(true);
      });
    return () => {
      current = false;
    };
  }, []);

  const reload = async () => {
    try {
      const { data } = await bankingClient.GET("/v1/connections");
      if (data) setList(data);
    } catch {
      setError(BANKING_UNAVAILABLE);
    }
  };

  const replace = (updated: BankConnection) =>
    setList((current) =>
      current
        ? {
            ...current,
            items: current.items.map((item) =>
              item.id === updated.id ? updated : item,
            ),
          }
        : current,
    );

  // The bank's consent page; it returns to /finance/bank/callback.
  const connect = () =>
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      try {
        const { data, error, response } = await bankingClient.POST(
          "/v1/connections/authorize",
          { body: { institution: DEFAULT_BANK, country: "DE" } },
        );
        if (!data) {
          setError(bankFailure(response.status, error).message);
          return;
        }
        setOpening(true);
        window.location.assign(data.url);
      } catch {
        setError(BANKING_UNAVAILABLE);
      }
    });

  const sync = (connection: BankConnection) =>
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      try {
        const { data, error, response } = await bankingClient.POST(
          "/v1/connections/{connectionId}/sync",
          { params: { path: { connectionId: connection.id } } },
        );
        if (data) {
          replace(data);
          setNow(new Date());
          onChanged?.();
          return;
        }
        setError(bankFailure(response.status, error).message);
        await reload();
      } catch {
        setError(BANKING_UNAVAILABLE);
      }
    });

  const disconnect = (connection: BankConnection) =>
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      try {
        const { response } = await bankingClient.DELETE(
          "/v1/connections/{connectionId}",
          { params: { path: { connectionId: connection.id } } },
        );
        if (!response.ok) {
          setError(
            `${connection.institution} could not be disconnected. Try again.`,
          );
          return;
        }
        setConfirming(false);
        await reload();
        onChanged?.();
        setNotice(
          `${connection.institution} is disconnected. Its accounts are archived in Finance with their history.`,
        );
      } catch {
        setError(BANKING_UNAVAILABLE);
      }
    });

  const connection = list ? liveConnection(list) : undefined;
  const state = list ? bankState(list.configured, connection, now) : undefined;
  const badge = state ? BADGES[state] : undefined;
  const syncLine = connection ? syncText(connection, timeZone) : undefined;
  const syncNow = connection
    ? syncNowState(connection, timeZone, now)
    : undefined;
  const name = connection?.institution ?? DEFAULT_BANK;

  return (
    <Card
      aria-labelledby="bank-connection-title"
      data-testid="bank-connection"
      role="region"
    >
      <CardHeader>
        <CardTitle
          className="flex items-center gap-2"
          id="bank-connection-title"
        >
          <Landmark aria-hidden="true" className="size-4" />
          Bank connection
        </CardTitle>
        <CardDescription>
          {name}, read-only: balances and transactions sync into Finance. Life
          OS can never make payments.
        </CardDescription>
        {badge ? (
          <CardAction>
            <Badge variant={badge.variant}>{badge.label}</Badge>
          </CardAction>
        ) : null}
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {notice ? (
          <p
            className="bg-success-subtle border-success/30 flex items-start gap-2 rounded-lg border px-3 py-2"
            role="status"
          >
            <CheckCircle2
              aria-hidden="true"
              className="text-success mt-0.5 size-4 shrink-0"
            />
            {notice}
          </p>
        ) : null}

        {failed ? (
          <p className="text-destructive" role="alert">
            {BANKING_UNAVAILABLE}
          </p>
        ) : !list || !state ? (
          <p className="text-muted-foreground">Loading the bank connection…</p>
        ) : state === "not_configured" ? (
          <div className="grid gap-1">
            <p>
              Bank connections are not set up on this server yet. Its{" "}
              <code>.env</code> needs {BANKING_SETTINGS.join(", ")}; then
              restart banking. The steps are in docs/operations/banking.md.
            </p>
            {list.setupProblem ? (
              <p className="text-muted-foreground text-xs break-words">
                {list.setupProblem}
              </p>
            ) : null}
          </div>
        ) : state === "not_connected" || !connection ? (
          <p>
            No bank is connected. Connecting opens {DEFAULT_BANK}, where you
            allow Life OS to read your accounts (for up to 180 days); then the
            last 90 days of transactions are imported into Finance. Later syncs
            run {list.schedule.times.length} times a day (
            {list.schedule.times.join(", ")}).
          </p>
        ) : (
          <>
            {state === "expired" || state === "error" ? (
              <div
                className="bg-warning-subtle border-warning/30 grid gap-1 rounded-lg border px-3 py-2"
                role="alert"
              >
                <p className="flex items-start gap-2 font-medium">
                  <AlertTriangle
                    aria-hidden="true"
                    className="text-warning mt-0.5 size-4 shrink-0"
                  />
                  {state === "expired"
                    ? `${name} access expired. Reconnect to keep your transactions syncing; your accounts and history stay.`
                    : `${name} needs you to connect again.`}
                </p>
                {connection.lastError ? (
                  <p className="text-muted-foreground text-xs break-words">
                    {connection.lastError}
                  </p>
                ) : null}
              </div>
            ) : null}
            <ul
              className="divide-y rounded-lg border"
              data-testid="bank-accounts"
            >
              {connection.accounts.map((account) => (
                <li
                  className="flex items-center justify-between gap-3 px-3 py-2"
                  key={account.id}
                >
                  <span className="truncate font-medium">
                    {accountLabel(account)}
                  </span>
                  <span className="text-muted-foreground shrink-0 text-xs">
                    {account.currency}
                  </span>
                </li>
              ))}
            </ul>
            <dl className="text-muted-foreground grid gap-1 text-xs">
              {syncLine && state !== "expired" && state !== "error" ? (
                <dd
                  className={cn(
                    "break-words",
                    syncLine.failing && "text-destructive",
                  )}
                  data-testid="bank-sync"
                >
                  {syncLine.text}
                </dd>
              ) : null}
              {state !== "expired" &&
              state !== "error" &&
              nextSyncText(connection, timeZone) ? (
                <dd>{nextSyncText(connection, timeZone)}</dd>
              ) : null}
              {consentText(connection, timeZone, now) ? (
                <dd
                  className={cn(
                    state === "expiring" && "text-warning font-medium",
                  )}
                >
                  {consentText(connection, timeZone, now)}
                </dd>
              ) : null}
            </dl>
          </>
        )}

        {error ? (
          <p className="text-destructive" role="alert">
            {error}
          </p>
        ) : null}
      </CardContent>
      {list && state && state !== "not_configured" ? (
        <CardFooter className="flex flex-wrap gap-2">
          {confirming && connection ? (
            <>
              <p className="w-full text-sm">
                Disconnect {name}? Life OS ends its access at the bank and stops
                syncing. The accounts are archived in Finance and keep their
                history.
              </p>
              <Button
                disabled={pending}
                onClick={() => setConfirming(false)}
                variant="outline"
              >
                Keep it
              </Button>
              <Button
                disabled={pending}
                onClick={() => disconnect(connection)}
                variant="destructive"
              >
                {working ? "Disconnecting…" : `Disconnect ${name}`}
              </Button>
            </>
          ) : (
            <>
              {state === "not_connected" ? (
                <Button disabled={pending} onClick={connect}>
                  {opening
                    ? `Opening ${DEFAULT_BANK}…`
                    : `Connect ${DEFAULT_BANK}`}
                </Button>
              ) : null}
              {needsReconnect(state) ? (
                <Button
                  disabled={pending}
                  onClick={connect}
                  variant={state === "expiring" ? "outline" : "default"}
                >
                  {opening ? `Opening ${name}…` : "Reconnect"}
                </Button>
              ) : null}
              {connection &&
              (state === "connected" ||
                state === "pending" ||
                state === "expiring") ? (
                <Button
                  disabled={pending || !syncNow?.allowed}
                  onClick={() => sync(connection)}
                  title={syncNow?.reason}
                  variant="outline"
                >
                  {working ? "Syncing…" : "Sync now"}
                </Button>
              ) : null}
              {connection ? (
                <Button
                  className="ml-auto"
                  disabled={pending}
                  onClick={() => setConfirming(true)}
                  variant="destructive"
                >
                  Disconnect
                </Button>
              ) : null}
              {syncNow &&
              !syncNow.allowed &&
              syncNow.reason &&
              state !== "expired" &&
              state !== "error" ? (
                <p className="text-muted-foreground w-full text-xs">
                  {syncNow.reason}
                </p>
              ) : null}
            </>
          )}
        </CardFooter>
      ) : null}
    </Card>
  );
}
