"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { DatabaseBackup, LoaderCircle } from "lucide-react";

import { Button } from "@/components/ui/button";
import { backupClient } from "@/lib/api/backup-client";
import {
  afterPoll,
  BACKUP_POLL_MS,
  type BackupNowState,
  progressMessage,
  resultMessage,
  startError,
} from "@/lib/backup-now";
import { cn } from "@/lib/utils";

/**
 * The Backups card's "Back up now": starts a backup of every database as the
 * owner, follows it until it ends, and refreshes the page so the card shows
 * the new state. While a backup or restore check is already running (409),
 * it follows that one instead.
 */
export function BackupNow({ running }: { running: boolean }) {
  const router = useRouter();
  const [state, setState] = useState<BackupNowState>(
    running
      ? { kind: "running", message: "A backup or restore check is running…" }
      : { kind: "idle" },
  );
  // The run being followed: its id, or {} for whatever runs now.
  const [following, setFollowing] = useState<{ id?: string } | null>(
    running ? {} : null,
  );

  useEffect(() => {
    if (!following) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const { data } = await backupClient
        .GET("/v1/status")
        .catch(() => ({ data: undefined }));
      if (cancelled) return;
      if (data) {
        const next = afterPoll(data, following.id);
        setState(next);
        if (next.kind === "done") {
          setFollowing(null);
          router.refresh();
          return;
        }
      }
      timer = setTimeout(poll, BACKUP_POLL_MS);
    };
    timer = setTimeout(poll, BACKUP_POLL_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [following, router]);

  async function start() {
    setState({ kind: "starting" });
    const { data, error, response } = await backupClient
      .POST("/v1/backups")
      .catch(() => ({
        data: undefined,
        error: undefined,
        response: undefined,
      }));
    if (response?.status === 202 && data && "completed" in data) {
      setState({ kind: "running", message: progressMessage(data) });
      setFollowing({ id: data.id });
    } else if (response?.status === 200 && data && "result" in data) {
      setState({ kind: "done", ...resultMessage(data) });
      router.refresh();
    } else if (response?.status === 409) {
      setState({
        kind: "running",
        message: "A backup or restore check is already running; following it…",
      });
      setFollowing({});
    } else {
      setState({
        kind: "error",
        message: startError(response?.status, error?.error.message),
      });
    }
  }

  const busy = state.kind === "starting" || state.kind === "running";
  const message =
    state.kind === "running" || state.kind === "done" || state.kind === "error"
      ? state.message
      : "";
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t pt-3">
      <Button
        disabled={busy}
        onClick={start}
        size="sm"
        type="button"
        variant="outline"
      >
        {busy ? (
          <LoaderCircle
            aria-hidden="true"
            className="animate-spin"
            data-icon="inline-start"
          />
        ) : (
          <DatabaseBackup aria-hidden="true" data-icon="inline-start" />
        )}
        {busy ? "Backing up…" : "Back up now"}
      </Button>
      <p
        aria-live="polite"
        className={cn(
          "min-w-0 text-sm break-words",
          (state.kind === "error" || (state.kind === "done" && !state.ok)) &&
            "text-destructive",
        )}
      >
        {message}
      </p>
    </div>
  );
}
