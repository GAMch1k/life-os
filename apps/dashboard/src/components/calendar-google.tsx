"use client";

import { AlertTriangle, CalendarSync, CheckCircle2 } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { useEffect, useState, useTransition } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { UNAVAILABLE } from "@/components/calendar-event-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { FieldLegend, FieldSet } from "@/components/ui/field";
import { Label } from "@/components/ui/label";
import { calendarClient } from "@/lib/api/calendar-client";
import {
  calendarDetail,
  connectFailure,
  GOOGLE_CONNECTED,
  GOOGLE_PARAM,
  GOOGLE_SETTINGS,
  type GoogleCalendar,
  type GoogleConnection,
  googleState,
  missingPermissions,
  replaceCalendar,
  syncSummary,
  withoutGoogleParam,
} from "@/lib/google-calendar";

/**
 * The Calendar toolbar's "Google Calendar" button and dialog: connect the
 * owner's Google account, choose which of its calendars will sync, refresh
 * the list, or disconnect. Back from Google's consent page
 * (`/calendar?google=connected`), the dialog opens by itself.
 */
export function GoogleCalendarControl() {
  const justConnected =
    useSearchParams().get(GOOGLE_PARAM) === GOOGLE_CONNECTED;
  const [open, setOpen] = useState(justConnected);
  const [connected, setConnected] = useState(justConnected);

  // The marker is shown once: a reload does not reopen the dialog.
  useEffect(() => {
    if (!justConnected) return;
    window.history.replaceState(
      window.history.state,
      "",
      `${window.location.pathname}${withoutGoogleParam(window.location.search)}`,
    );
  }, [justConnected]);

  const change = (next: boolean) => {
    setOpen(next);
    if (!next) setConnected(false);
  };

  return (
    <>
      <Button onClick={() => change(true)} size="sm" variant="outline">
        <CalendarSync />
        Google Calendar
      </Button>
      <Dialog onOpenChange={change} open={open}>
        <DialogContent className="sm:max-w-lg">
          <GooglePanel justConnected={connected} />
        </DialogContent>
      </Dialog>
    </>
  );
}

type Loaded = { connection: GoogleConnection; calendars: GoogleCalendar[] };

async function loadGoogle(): Promise<Loaded> {
  const [connection, calendars] = await Promise.all([
    calendarClient.GET("/v1/google/connection"),
    calendarClient.GET("/v1/google/calendars"),
  ]);
  if (!connection.data || !calendars.data) {
    throw new Error("Google connection unavailable");
  }
  return { connection: connection.data, calendars: calendars.data.items };
}

function GooglePanel({ justConnected }: { justConnected: boolean }) {
  const [loaded, setLoaded] = useState<Loaded>();
  const [failed, setFailed] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState(
    justConnected
      ? "Google Calendar is connected. Choose the calendars to sync."
      : undefined,
  );
  const [confirming, setConfirming] = useState(false);
  const [saving, setSaving] = useState<string>();
  const [opening, setOpening] = useState(false);
  const [working, startTransition] = useTransition();
  const pending = working || opening;

  // Back from Google with the browser's back button, the page may be
  // restored as it was left: ready to connect again.
  useEffect(() => {
    const restored = (event: PageTransitionEvent) => {
      if (event.persisted) setOpening(false);
    };
    window.addEventListener("pageshow", restored);
    return () => window.removeEventListener("pageshow", restored);
  }, []);

  useEffect(() => {
    let current = true;
    loadGoogle()
      .then((result) => {
        if (current) setLoaded(result);
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
      setLoaded(await loadGoogle());
    } catch {
      setError(UNAVAILABLE);
    }
  };

  // Google's consent page; it returns to /calendar/google/callback.
  const connect = () =>
    startTransition(async () => {
      setError(undefined);
      try {
        const { data, error, response } = await calendarClient.POST(
          "/v1/google/connection/authorize",
        );
        if (!data) {
          setError(connectFailure(response.status, error).message);
          return;
        }
        setOpening(true);
        window.location.assign(data.url);
      } catch {
        setError(UNAVAILABLE);
      }
    });

  const refresh = () =>
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      try {
        const { data, error, response } = await calendarClient.POST(
          "/v1/google/calendars/refresh",
        );
        if (!data) {
          setError(connectFailure(response.status, error).message);
          // An expired authorization shows as such.
          await reload();
          return;
        }
        setLoaded((current) =>
          current ? { ...current, calendars: data.items } : current,
        );
        setNotice("The calendar list is up to date.");
      } catch {
        setError(UNAVAILABLE);
      }
    });

  const disconnect = () =>
    startTransition(async () => {
      setError(undefined);
      setNotice(undefined);
      try {
        const { response } = await calendarClient.DELETE(
          "/v1/google/connection",
        );
        if (!response.ok) {
          setError("Google Calendar could not be disconnected. Try again.");
          return;
        }
        setConfirming(false);
        await reload();
        setNotice("Google Calendar is disconnected.");
      } catch {
        setError(UNAVAILABLE);
      }
    });

  const choose = async (calendar: GoogleCalendar, syncEnabled: boolean) => {
    setError(undefined);
    setSaving(calendar.id);
    const update = (next: GoogleCalendar) =>
      setLoaded((current) =>
        current
          ? { ...current, calendars: replaceCalendar(current.calendars, next) }
          : current,
      );
    update({ ...calendar, syncEnabled });
    try {
      const { data } = await calendarClient.PATCH(
        "/v1/google/calendars/{calendarId}",
        {
          params: { path: { calendarId: calendar.id } },
          body: { syncEnabled },
        },
      );
      if (data) update(data);
      else {
        update(calendar);
        setError(`Could not save the choice for “${calendar.summary}”.`);
      }
    } catch {
      update(calendar);
      setError(UNAVAILABLE);
    } finally {
      setSaving(undefined);
    }
  };

  const state = loaded ? googleState(loaded.connection) : undefined;
  const connection = loaded?.connection;
  const linked = Boolean(connection && connection.status !== "not_connected");
  const account = connection?.accountEmail ?? "your Google account";

  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Google Calendar</DialogTitle>
        <DialogDescription>
          Connect your Google account and choose which of its calendars sync
          into Life OS.
        </DialogDescription>
      </DialogHeader>

      {notice ? (
        <p
          className="bg-success-subtle border-success/30 flex items-start gap-2 rounded-lg border px-3 py-2 text-sm"
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
        <p className="text-destructive text-sm" role="alert">
          {UNAVAILABLE}
        </p>
      ) : !loaded || !connection ? (
        <p className="text-muted-foreground text-sm">
          Loading the Google connection…
        </p>
      ) : state === "not_configured" ? (
        <div className="grid gap-2 text-sm">
          <p>
            Google Calendar is not set up on this server yet. Its{" "}
            <code>.env</code> needs {GOOGLE_SETTINGS.join(", ")} from your
            Google Cloud project; then restart Calendar. The steps are in
            docs/operations/google-calendar.md.
          </p>
          {linked ? (
            <p className="text-muted-foreground">
              {account} is still connected but cannot be used until then.
            </p>
          ) : null}
        </div>
      ) : state === "not_connected" ? (
        <p className="text-sm">
          No Google account is connected. Connecting opens Google, where you
          allow Life OS to see your calendars and edit their events; then you
          come back here to choose which calendars sync.
        </p>
      ) : state === "expired" || state === "error" ? (
        <div
          className="bg-warning-subtle border-warning/30 grid gap-1 rounded-lg border px-3 py-2 text-sm"
          role="alert"
        >
          <p className="flex items-start gap-2 font-medium">
            <AlertTriangle
              aria-hidden="true"
              className="text-warning mt-0.5 size-4 shrink-0"
            />
            {state === "expired"
              ? `Google access for ${account} expired or was revoked. Reconnect Google to keep it in sync.`
              : `Google refused the connection for ${account}. Reconnect Google to fix it.`}
          </p>
          {connection.lastError ? (
            <p className="text-muted-foreground text-xs break-words">
              {connection.lastError}
            </p>
          ) : null}
        </div>
      ) : (
        <p className="text-sm">
          Connected as <span className="font-medium">{account}</span>.
        </p>
      )}

      {loaded && state === "connected" && connection ? (
        missingPermissions(connection).length ? (
          <p
            className="bg-warning-subtle border-warning/30 rounded-lg border px-3 py-2 text-sm"
            role="note"
          >
            Google did not grant permission to{" "}
            {missingPermissions(connection).join(", ")}. Reconnect Google and
            allow every permission Life OS asks for.
          </p>
        ) : null
      ) : null}

      {loaded && linked && state !== "not_configured" ? (
        <CalendarChoices
          calendars={loaded.calendars}
          disabled={pending}
          onChoose={choose}
          saving={saving}
        />
      ) : null}

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      {loaded ? (
        <DialogFooter>
          {confirming ? (
            <>
              <Button
                disabled={pending}
                onClick={() => setConfirming(false)}
                variant="outline"
              >
                Keep it
              </Button>
              <Button
                disabled={pending}
                onClick={disconnect}
                variant="destructive"
              >
                {pending ? "Disconnecting…" : "Disconnect Google"}
              </Button>
              <p className="text-sm sm:order-first sm:mr-auto sm:self-center">
                Disconnect {account}? Life OS withdraws its access at Google and
                forgets your calendar choices.
              </p>
            </>
          ) : (
            <>
              {linked ? (
                <Button
                  className="sm:mr-auto"
                  disabled={pending}
                  onClick={() => setConfirming(true)}
                  variant="destructive"
                >
                  Disconnect
                </Button>
              ) : null}
              {state === "connected" ? (
                <Button disabled={pending} onClick={refresh} variant="outline">
                  {working ? "Refreshing…" : "Refresh list"}
                </Button>
              ) : null}
              {state === "not_connected" ||
              state === "expired" ||
              state === "error" ? (
                <Button disabled={pending} onClick={connect}>
                  {opening
                    ? "Opening Google…"
                    : state === "not_connected"
                      ? "Connect Google"
                      : "Reconnect Google"}
                </Button>
              ) : null}
            </>
          )}
        </DialogFooter>
      ) : null}
    </div>
  );
}

function CalendarChoices({
  calendars,
  disabled,
  onChoose,
  saving,
}: {
  calendars: GoogleCalendar[];
  disabled: boolean;
  onChoose: (calendar: GoogleCalendar, syncEnabled: boolean) => void;
  saving?: string;
}) {
  return (
    <FieldSet className="min-w-0 gap-2">
      <FieldLegend className="mb-1" variant="label">
        Calendars to sync
      </FieldLegend>
      <p className="text-muted-foreground text-xs">
        {syncSummary(calendars)} Syncing starts in a later release; your choices
        are kept until then.
      </p>
      {calendars.length ? (
        <ul
          className="divide-y rounded-lg border"
          data-testid="google-calendar-list"
        >
          {calendars.map((calendar) => (
            <li key={calendar.id}>
              <Label className="cursor-pointer gap-3 px-3 py-2 leading-normal font-normal">
                <Checkbox
                  checked={calendar.syncEnabled}
                  disabled={disabled || saving === calendar.id}
                  onCheckedChange={(syncEnabled) =>
                    onChoose(calendar, syncEnabled)
                  }
                />
                <span
                  aria-hidden="true"
                  className="bg-muted size-3 shrink-0 rounded-full border"
                  style={
                    calendar.color
                      ? { backgroundColor: calendar.color }
                      : undefined
                  }
                />
                <span className="grid min-w-0">
                  <span className="truncate font-medium">
                    {calendar.summary}
                  </span>
                  <span className="text-muted-foreground text-xs">
                    {calendarDetail(calendar)}
                  </span>
                </span>
              </Label>
            </li>
          ))}
        </ul>
      ) : null}
    </FieldSet>
  );
}
