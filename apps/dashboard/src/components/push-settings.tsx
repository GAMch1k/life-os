"use client";

import { BellOff, BellRing, Send } from "lucide-react";
import { useEffect, useState, useSyncExternalStore } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import {
  currentSubscription,
  deviceStatus,
  disablePush,
  enablePush,
  fetchPushConfig,
  fetchPushDevices,
  PUSH_EXPLAINER,
  pushState,
  pushStateView,
  readPushSupport,
  removeDevice,
  sendPushTest,
  testResultText,
  type PushConfig,
  type PushDevice,
  type PushTestDeviceResult,
} from "@/lib/web-push";

const noSubscription = () => () => {};

const STATUS_TONES = {
  success: "text-success",
  warning: "text-warning",
  muted: "text-muted-foreground",
} as const;

type Busy = "enable" | "disable" | "test" | { remove: string };

/** What the setting shows: the server's side and this browser's. */
type Snapshot = {
  config: PushConfig | null;
  devices: PushDevice[] | null;
  endpoint: string | null;
  permission: NotificationPermission | undefined;
  loadedAt: Date;
};

async function readSnapshot(): Promise<Snapshot> {
  const supported = readPushSupport() === "supported";
  const [config, devices, subscription] = await Promise.all([
    fetchPushConfig(),
    fetchPushDevices(),
    supported ? currentSubscription().catch(() => null) : null,
  ]);
  return {
    config,
    devices,
    endpoint: subscription?.endpoint ?? null,
    permission: supported ? Notification.permission : undefined,
    loadedAt: new Date(),
  };
}

/**
 * "Notifications on this device": turns web push on or off for this browser
 * (above all the installed app on the owner's phone), lists every device that
 * has it on with its latest push, and sends a test push to all of them. The
 * server renders it as loading; the browser reads its state.
 */
export function PushSettings({ timeZone }: { timeZone: string }) {
  const support = useSyncExternalStore(
    noSubscription,
    readPushSupport,
    () => undefined,
  );
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [busy, setBusy] = useState<Busy>();
  const [message, setMessage] = useState<{ ok: boolean; text: string }>();
  const [results, setResults] = useState<PushTestDeviceResult[]>();

  useEffect(() => {
    let current = true;
    void readSnapshot().then((next) => {
      if (current) setSnapshot(next);
    });
    return () => {
      current = false;
    };
  }, []);

  async function refresh() {
    setSnapshot(await readSnapshot());
  }

  const { config, devices, endpoint, permission } = snapshot ?? {};
  const loadedAt = snapshot?.loadedAt ?? new Date(0);
  const state = pushState({ support, config, permission, endpoint, devices });
  const view = pushStateView(state, support, config);

  async function enable() {
    if (!config?.publicKey) return;
    setBusy("enable");
    setMessage(undefined);
    const result = await enablePush(config.publicKey);
    if (result.ok) {
      setMessage({ ok: true, text: "Notifications are on for this device." });
    } else {
      setMessage({ ok: false, text: result.message });
    }
    await refresh();
    setBusy(undefined);
  }

  async function disable() {
    setBusy("disable");
    setMessage(undefined);
    const done = await disablePush(devices ?? []).catch(() => false);
    setMessage(
      done
        ? { ok: true, text: "Notifications are off for this device." }
        : {
            ok: false,
            text: "Couldn't turn notifications off. Try again.",
          },
    );
    await refresh();
    setBusy(undefined);
  }

  async function remove(device: PushDevice) {
    setBusy({ remove: device.id });
    setMessage(undefined);
    // This browser's own device: unsubscribe it too, as Turn off does.
    const done =
      device.endpoint === endpoint
        ? await disablePush([device]).catch(() => false)
        : await removeDevice(device.id);
    if (!done) {
      setMessage({
        ok: false,
        text: `Couldn't remove ${device.deviceLabel}. Try again.`,
      });
    }
    await refresh();
    setBusy(undefined);
  }

  async function test() {
    setBusy("test");
    setMessage(undefined);
    setResults(undefined);
    const outcome = await sendPushTest();
    if (outcome === null) {
      setMessage({
        ok: false,
        text: "Couldn't send the test. Notifications may be unavailable.",
      });
    } else {
      setResults(outcome);
    }
    await refresh();
    setBusy(undefined);
  }

  const working = busy !== undefined;
  return (
    <Card aria-labelledby="push-settings-title" role="region" size="sm">
      <CardContent className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold" id="push-settings-title">
            Notifications on this device
          </h2>
          <p className="text-muted-foreground text-xs">{PUSH_EXPLAINER}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <p className="mr-auto text-sm" data-state={state} id="push-state">
            {view.summary}
          </p>
          {view.action === "enable" ? (
            <Button disabled={working} onClick={() => void enable()} size="lg">
              <BellRing aria-hidden="true" />
              {busy === "enable" ? "Turning on…" : "Turn on"}
            </Button>
          ) : null}
          {view.action === "disable" ? (
            <Button
              disabled={working}
              onClick={() => void disable()}
              size="lg"
              variant="outline"
            >
              <BellOff aria-hidden="true" />
              {busy === "disable" ? "Turning off…" : "Turn off"}
            </Button>
          ) : null}
        </div>
        {view.hint ? (
          <p className="text-muted-foreground text-xs">{view.hint}</p>
        ) : null}
        <p
          className={cn(
            "text-sm empty:hidden",
            message?.ok ? "text-success" : "text-destructive",
          )}
          role="status"
        >
          {message?.text}
        </p>
        {devices?.length ? (
          <div className="space-y-2">
            <ul aria-label="Devices with notifications on" className="divide-y">
              {devices.map((device) => {
                const status = deviceStatus(device, timeZone, loadedAt);
                const label = device.deviceLabel;
                return (
                  <li
                    className="flex items-start gap-2 py-2 first:pt-0"
                    key={device.id}
                  >
                    <div className="min-w-0 flex-1">
                      <p className="flex flex-wrap items-center gap-1.5 text-sm font-medium">
                        {label}
                        {device.endpoint === endpoint ? (
                          <Badge variant="secondary">This device</Badge>
                        ) : null}
                      </p>
                      <p className="text-xs wrap-anywhere">
                        <span className={STATUS_TONES[status.tone]}>
                          {status.text}
                        </span>
                        {status.error ? (
                          <span className="text-muted-foreground">
                            {" "}
                            — {status.error}
                          </span>
                        ) : null}
                      </p>
                    </div>
                    <Button
                      aria-label={`Remove ${label}`}
                      disabled={working}
                      onClick={() => void remove(device)}
                      size="xs"
                      variant="ghost"
                    >
                      Remove
                    </Button>
                  </li>
                );
              })}
            </ul>
            <Button
              disabled={working || !config?.configured}
              onClick={() => void test()}
              size="sm"
              variant="outline"
            >
              <Send aria-hidden="true" />
              {busy === "test" ? "Sending…" : "Send test"}
            </Button>
            {results ? (
              <ul aria-label="Test results" className="space-y-0.5 text-xs">
                {results.map((result) => (
                  <li
                    className={cn(
                      "wrap-anywhere",
                      result.outcome === "sent"
                        ? "text-success"
                        : "text-destructive",
                    )}
                    key={result.subscriptionId}
                  >
                    {testResultText(result)}
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : config?.configured && devices ? (
          <p className="text-muted-foreground text-xs">
            No device has notifications on yet.
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}
