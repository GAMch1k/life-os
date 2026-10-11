import type { components } from "@/lib/api/generated/backup";

type BackupProgress = components["schemas"]["BackupProgress"];
type BackupRun = components["schemas"]["BackupRun"];
type BackupStatus = components["schemas"]["BackupStatus"];

/** How often "Back up now" looks at a backup in progress. */
export const BACKUP_POLL_MS = 2_000;

/** What the Status card's "Back up now" shows. */
export type BackupNowState =
  | { kind: "idle" }
  | { kind: "starting" }
  | { kind: "running"; message: string }
  | { kind: "done"; ok: boolean; message: string }
  | { kind: "error"; message: string };

/** For example "Backing up tasks_db (3 of 11 done)…". */
export function progressMessage(progress: BackupProgress) {
  const done = `${progress.completed} of ${progress.databases.length} done`;
  return progress.current
    ? `Backing up ${progress.current} (${done})…`
    : `Backing up (${done})…`;
}

/** A finished run in one sentence. */
export function resultMessage(run: BackupRun): {
  ok: boolean;
  message: string;
} {
  const seconds = run.durationSeconds.toFixed(1);
  if (run.result === "succeeded") {
    const count = run.databases.length;
    return {
      ok: true,
      message: `Backed up ${count} ${count === 1 ? "database" : "databases"} in ${seconds} s.`,
    };
  }
  if (run.result === "interrupted") {
    return { ok: false, message: "The backup was interrupted." };
  }
  return {
    ok: false,
    message: `The backup failed: ${run.error ?? "see the card for why"}.`,
  };
}

/**
 * The state after reading the status while following the run id (or, after
 * a 409, whatever was running then, when id is undefined).
 */
export function afterPoll(
  status: BackupStatus,
  id: string | undefined,
): BackupNowState {
  if (status.running && (id === undefined || status.running.id === id)) {
    return { kind: "running", message: progressMessage(status.running) };
  }
  if (id === undefined && status.runningRestoreCheck) {
    return {
      kind: "running",
      message: "A restore check is running; a backup can start when it ends.",
    };
  }
  if (id !== undefined && status.lastRun?.id === id) {
    return { kind: "done", ...resultMessage(status.lastRun) };
  }
  return {
    kind: "done",
    ok: true,
    message: "Finished; the card shows the result.",
  };
}

/** What to say when a backup could not start. */
export function startError(status: number | undefined, message?: string) {
  if (status === undefined || status >= 500) {
    return "The backup service could not be reached.";
  }
  return message ?? `The backup could not start (${status}).`;
}
