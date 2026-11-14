"use client";

import { QrCode as QrCodeIcon, Smartphone, Unlock, Watch } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useId, useState, useTransition } from "react";

import { CopyButton } from "@/components/copy-button";
import { QrCode } from "@/components/qr-code";
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
import type { DeviceInfo } from "@/lib/auth/account-credentials";
import { dateTimeFormat } from "@/lib/time-zone";

function when(iso: string | null, timeZone: string) {
  if (!iso) return "";
  return dateTimeFormat(timeZone, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(iso));
}

type Pairing = { code: string; expiresAt: string; link: string };

/** Settings → Devices (src/app/settings/devices/page.tsx). */
export function DevicesSettings({
  devices,
  legacyAccess,
  nav,
  publicUrl,
  timeZone,
}: {
  devices: DeviceInfo[];
  /** The owner, while legacy access is on: a pointer to turn it off. */
  legacyAccess: boolean;
  nav?: React.ReactNode;
  publicUrl: string;
  timeZone: string;
}) {
  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Devices</h1>
        <p className="text-muted-foreground text-sm">
          The phones and watches signed in to your account with the Life OS app.
          Each has a key of its own: signing one out ends only that one.
        </p>
      </header>
      {nav}
      {legacyAccess ? (
        <Alert>
          <Unlock aria-hidden="true" />
          <AlertTitle>Legacy access is on</AlertTitle>
          <AlertDescription>
            Once your phone and watch are paired here, turn legacy access off in{" "}
            <Link className="underline" href="/settings/security">
              Settings → Security
            </Link>
            : until then, anyone who reaches Life OS acts as you.
          </AlertDescription>
        </Alert>
      ) : null}
      <PairCard publicUrl={publicUrl} timeZone={timeZone} />
      <DeviceList devices={devices} timeZone={timeZone} />
    </div>
  );
}

function PairCard({
  publicUrl,
  timeZone,
}: {
  publicUrl: string;
  timeZone: string;
}) {
  const id = useId();
  const [pairing, setPairing] = useState<Pairing>();
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();

  const makeCode = () =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch("/api/account/devices/pairing-codes", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: "{}",
      }).catch(() => null);
      const body = (await response?.json().catch(() => null)) as
        (Pairing & { error?: { message?: string } }) | null;
      if (!response?.ok || !body?.link) {
        setPairing(undefined);
        return setError(
          body?.error?.message ?? "Could not make a pairing code. Try again.",
        );
      }
      setPairing({
        code: body.code,
        expiresAt: body.expiresAt,
        link: body.link,
      });
    });

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <QrCodeIcon aria-hidden="true" className="size-4" />
          Pair a phone
        </CardTitle>
        <CardDescription>
          In the Life OS app, choose to pair with a QR code and scan the code
          shown here. It works once, for 5 minutes. A phone without a second
          screen can sign in from the app instead. The watch is paired by its
          phone.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {pairing ? (
          <div className="grid gap-3 sm:grid-cols-[12rem_1fr] sm:items-center">
            <QrCode
              className="size-48 rounded-md border"
              label="Pairing QR code"
              text={pairing.link}
            />
            <div className="grid gap-2">
              <p>
                Or type this code in the app, with the address{" "}
                <strong>{publicUrl}</strong>:
              </p>
              <p className="flex items-center gap-2">
                <code
                  className="bg-muted rounded px-2 py-1 font-mono text-base tracking-wider"
                  id={`${id}-code`}
                >
                  {pairing.code}
                </code>
                <CopyButton label="Copy the code" targetId={`${id}-code`} />
              </p>
              <p className="text-muted-foreground">
                Valid until {when(pairing.expiresAt, timeZone)}, once.
              </p>
              <div>
                <Button
                  disabled={busy}
                  onClick={makeCode}
                  size="sm"
                  type="button"
                  variant="outline"
                >
                  New code
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div>
            <Button disabled={busy} onClick={makeCode} type="button">
              <QrCodeIcon aria-hidden="true" />
              Show a pairing code
            </Button>
          </div>
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

function DeviceList({
  devices,
  timeZone,
}: {
  devices: DeviceInfo[];
  timeZone: string;
}) {
  const id = useId();
  const router = useRouter();
  const [error, setError] = useState<string>();
  const [busy, startTransition] = useTransition();
  const phones = devices.filter((device) => device.kind === "phone");
  const watchesOf = (phone: DeviceInfo) =>
    devices.filter((device) => device.parentId === phone.id);
  // A watch whose phone is gone is still listed (it cannot be, but the list
  // must not hide a key that works).
  const orphans = devices.filter(
    (device) =>
      device.kind === "watch" &&
      !phones.some((phone) => phone.id === device.parentId),
  );

  const revoke = (device: DeviceInfo) =>
    startTransition(async () => {
      setError(undefined);
      const response = await fetch(`/api/account/devices/${device.id}`, {
        method: "DELETE",
      }).catch(() => null);
      if (!response?.ok && response?.status !== 404) {
        return setError("Could not sign that device out.");
      }
      router.refresh();
    });

  const row = (device: DeviceInfo, nested = false) => (
    <li
      className={`flex items-center justify-between gap-2 px-3 py-2 ${nested ? "pl-8" : ""}`}
      key={device.id}
    >
      <span className="min-w-0">
        <span className="flex items-center gap-2 font-medium">
          {device.kind === "watch" ? (
            <Watch aria-hidden="true" className="size-4" />
          ) : (
            <Smartphone aria-hidden="true" className="size-4" />
          )}
          <span className="truncate">{device.name}</span>
          <Badge variant="outline">
            {device.kind === "watch" ? "Watch" : "Phone"}
          </Badge>
        </span>
        <span className="text-muted-foreground block text-xs">
          Paired {when(device.createdAt, timeZone)} ·{" "}
          {device.lastUsedAt
            ? `last used ${when(device.lastUsedAt, timeZone)}`
            : "not used yet"}
        </span>
      </span>
      <Button
        aria-label={`Sign out ${device.name}`}
        disabled={busy}
        onClick={() => revoke(device)}
        size="sm"
        type="button"
        variant="outline"
      >
        Sign out
      </Button>
    </li>
  );

  return (
    <Card aria-labelledby={`${id}-title`} role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id={`${id}-title`}>
          <Smartphone aria-hidden="true" className="size-4" />
          Paired devices
        </CardTitle>
        <CardDescription>
          Signing a phone out signs its watch out too, and stops their push
          notifications.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        {devices.length === 0 ? (
          <p className="text-muted-foreground">No phone is paired yet.</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {phones.flatMap((phone) => [
              row(phone),
              ...watchesOf(phone).map((watch) => row(watch, true)),
            ])}
            {orphans.map((watch) => row(watch))}
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
