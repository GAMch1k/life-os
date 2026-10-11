import assert from "node:assert/strict";
import test from "node:test";

import {
  BACKUP_DISK_LOW,
  BACKUP_STALLED,
  RESTORE_CHECK_FAILED,
  RESTORE_CHECK_OVERDUE,
  formatBytes,
  statusAlerts,
  toRuntimeStatus,
  type BackupStatus,
  type RuntimeSignals,
  type StatusCheck,
} from "./runtime-status";

const now = new Date("2026-10-11T10:00:00Z");
const timeZone = "Europe/Berlin";
const hours = (h: number) =>
  new Date(now.getTime() - h * 3_600_000).toISOString();

type Run = NonNullable<BackupStatus["lastRun"]>;
type Check = NonNullable<BackupStatus["lastRestoreCheck"]>;

const run = (startedAt: string, overrides: Partial<Run> = {}): Run => ({
  id: "20261011T011500Z",
  trigger: "schedule",
  scope: "all",
  result: "succeeded",
  startedAt,
  finishedAt: startedAt,
  durationSeconds: 7.4,
  serverVersion: "18.6",
  totalBytes: 2_400_000,
  databases: [
    {
      service: "finance",
      name: "finance_db",
      result: "succeeded",
      bytes: 1_200_000,
      tables: 9,
      rows: 1834,
    },
    {
      service: "tasks",
      name: "tasks_db",
      result: "succeeded",
      bytes: 1_200_000,
      tables: 5,
      rows: 120,
    },
  ],
  ...overrides,
});

const passed = (startedAt: string, overrides: Partial<Check> = {}): Check => ({
  id: "20261011T011600Z",
  runId: "20261011T011500Z",
  trigger: "schedule",
  startedAt,
  finishedAt: startedAt,
  durationSeconds: 9,
  ok: true,
  databases: [
    { name: "finance_db", ok: true, tables: 9, rows: 1834, mismatches: [] },
    { name: "tasks_db", ok: true, tables: 5, rows: 120, mismatches: [] },
  ],
  ...overrides,
});

/** Backups that are fine: last night's run, checked right after. */
const healthy = (overrides: Partial<BackupStatus> = {}): BackupStatus => ({
  lastRun: run(hours(8)),
  lastSuccess: run(hours(8)),
  oldestSuccessAt: hours(24 * 30),
  lastRestoreCheck: passed(hours(7.9)),
  schedule: {
    time: "03:15",
    timeZone,
    nextRunAt: "2026-10-12T01:15:00Z",
    nextTrigger: "schedule",
    restoreCheckDay: "sunday",
    nextRestoreCheckAt: "2026-10-18T01:15:00Z",
  },
  retention: { daily: 7, weekly: 4, monthly: 12, failedDays: 3 },
  storage: {
    runs: 20,
    totalBytes: 48_000_000,
    freeBytes: 120_000_000_000,
    sizeBytes: 230_000_000_000,
  },
  databases: ["finance_db", "tasks_db"],
  ...overrides,
});

const signals = (
  backups: BackupStatus | null | undefined,
  backupReady = true,
): RuntimeSignals => ({
  processor: null,
  inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
  voice: {
    queuedCount: 0,
    processingCount: 0,
    retryableCount: 0,
    workerStalled: false,
    whisperReady: true,
  },
  backups,
  services: [
    { name: "Tasks", ready: true },
    { name: "Backup", ready: backupReady },
  ],
});

function backupsCheck(
  backups: BackupStatus | null | undefined,
  backupReady = true,
): StatusCheck | undefined {
  return toRuntimeStatus(
    signals(backups, backupReady),
    now,
    timeZone,
  ).checks.find((check) => check.id === "backups");
}

const fact = (check: StatusCheck | undefined, label: string) =>
  check?.facts.find((item) => item.label === label)?.value;

test("healthy backups are OK, with what the owner wants to know", () => {
  const check = backupsCheck(healthy());
  assert.equal(check?.state, "ok");
  assert.equal(check?.title, "Backups");
  assert.equal(
    check?.summary,
    "Last backup 11 Oct 2026, 04:00 CEST (8 h 0 min ago).",
  );
  assert.equal(
    fact(check, "Last backup"),
    "11 Oct 2026, 04:00 CEST (8 h 0 min ago)",
  );
  assert.equal(fact(check, "Databases"), "2");
  assert.equal(fact(check, "Total size"), "48.0 MB in 20 backups");
  assert.equal(fact(check, "Next backup"), "12 Oct 2026, 03:15 CEST");
  assert.equal(
    fact(check, "Last restore check"),
    "passed, 11 Oct 2026, 04:06 CEST (backup of 11 Oct 2026, 03:15 CEST)",
  );
  assert.equal(fact(check, "Retention"), "7 daily, 4 weekly, 12 monthly");
  assert.equal(fact(check, "Free disk"), "120 GB of 230 GB");
  assert.deepEqual(check?.control, { kind: "backup-now", running: false });
  assert.equal(check?.action, undefined);
});

test("without backups the check is left out", () => {
  assert.equal(backupsCheck(undefined), undefined);
});

test("an unreadable status is one alert: Services names a stopped backup service", () => {
  assert.equal(backupsCheck(null, false), undefined);
  const status = toRuntimeStatus(signals(null, false), now, timeZone);
  assert.equal(
    status.checks.find((check) => check.id === "services")?.summary,
    "Backup is not ready.",
  );
  const unreadable = backupsCheck(null, true);
  assert.equal(unreadable?.state, "problem");
  assert.equal(unreadable?.summary, "The backup status cannot be read.");
  assert.equal(unreadable?.control, undefined);
});

test("before the first backup, the check is not set up and says when it runs", () => {
  const check = backupsCheck(
    healthy({
      lastRun: undefined,
      lastSuccess: undefined,
      oldestSuccessAt: undefined,
      lastRestoreCheck: undefined,
      storage: {
        runs: 0,
        totalBytes: 0,
        freeBytes: 120_000_000_000,
        sizeBytes: 230_000_000_000,
      },
    }),
  );
  assert.equal(check?.state, "neutral");
  assert.equal(
    check?.summary,
    "No backup yet. The first one runs at 12 Oct 2026, 03:15 CEST.",
  );
  assert.equal(fact(check, "Last backup"), "none yet");
  assert.equal(
    fact(check, "Last restore check"),
    "not yet; after the backup of 18 Oct 2026, 03:15 CEST",
  );
  // Not set up needs nothing: it is not among the alerts.
  assert.ok(
    !statusAlerts(
      toRuntimeStatus(
        signals(
          healthy({
            lastRun: undefined,
            lastSuccess: undefined,
            lastRestoreCheck: undefined,
          }),
        ),
        now,
        timeZone,
      ),
    ).some((check) => check.id === "backups"),
  );

  const running = backupsCheck(
    healthy({
      lastRun: undefined,
      lastSuccess: undefined,
      lastRestoreCheck: undefined,
      running: {
        id: "20261011T095900Z",
        trigger: "catch-up",
        scope: "all",
        startedAt: hours(0.02),
        databases: ["finance_db", "tasks_db"],
        completed: 1,
        current: "tasks_db",
      },
    }),
  );
  assert.equal(running?.summary, "The first backup is running now.");
  assert.equal(fact(running, "Backing up now"), "tasks_db (1 of 2 done)");
  assert.deepEqual(running?.control, { kind: "backup-now", running: true });
});

test("a first backup that failed needs attention, saying what to do", () => {
  const failed = run(hours(1), {
    result: "failed",
    error: "1 of 2 databases failed",
    databases: [
      run(hours(1)).databases[0],
      {
        service: "food",
        name: "food_db",
        result: "failed",
        bytes: 0,
        tables: 0,
        rows: 0,
        error:
          "lifeos_backup cannot connect to food_db: run make db-provision-backup (permission denied for database food_db)",
      },
    ],
  });
  const check = backupsCheck(
    healthy({
      lastRun: failed,
      lastSuccess: undefined,
      lastRestoreCheck: undefined,
    }),
  );
  assert.equal(check?.state, "warning");
  assert.equal(
    check?.summary,
    "No backup has succeeded yet. The last backup of food_db failed.",
  );
  assert.equal(
    check?.action,
    "lifeos_backup cannot connect to food_db: run make db-provision-backup (permission denied for database food_db). Then use Back up now.",
  );
  assert.match(check?.detail ?? "", /^food_db: lifeos_backup cannot connect/);
});

test("a backup older than 26 hours needs attention, older than 50 is a problem", () => {
  const late = backupsCheck(
    healthy({
      lastRun: run(hours(30)),
      lastSuccess: run(hours(30)),
      lastRestoreCheck: passed(hours(29)),
    }),
  );
  assert.equal(late?.state, "warning");
  assert.equal(late?.summary, "The last backup is 30 h 0 min old.");
  assert.equal(late?.action, BACKUP_STALLED);
  const stale = backupsCheck(
    healthy({
      lastRun: run(hours(51)),
      lastSuccess: run(hours(51)),
      lastRestoreCheck: passed(hours(50)),
    }),
  );
  assert.equal(stale?.state, "problem");
  assert.equal(stale?.summary, "The last backup is 2 days old.");
  assert.equal(
    backupsCheck(
      healthy({ lastRun: run(hours(25.9)), lastSuccess: run(hours(25.9)) }),
    )?.state,
    "ok",
  );
});

test("a failed last run after a recent success needs attention", () => {
  const failed = run(hours(1), {
    id: "20261011T090000Z",
    trigger: "manual",
    result: "failed",
    error: "2 of 2 databases failed",
    databases: [
      {
        service: "finance",
        name: "finance_db",
        result: "failed",
        bytes: 0,
        tables: 0,
        rows: 0,
        error: "pg_dump failed: No space left on device",
      },
      {
        service: "tasks",
        name: "tasks_db",
        result: "failed",
        bytes: 0,
        tables: 0,
        rows: 0,
        error: "pg_dump failed: No space left on device",
      },
    ],
  });
  const check = backupsCheck(healthy({ lastRun: failed }));
  assert.equal(check?.state, "warning");
  assert.equal(
    check?.summary,
    "The last backup of 2 databases failed: finance_db, tasks_db.",
  );
  assert.equal(
    check?.action,
    "pg_dump failed: No space left on device (and 1 more below). Then use Back up now.",
  );
  assert.equal(
    check?.detail,
    "finance_db: pg_dump failed: No space left on device\ntasks_db: pg_dump failed: No space left on device",
  );

  const interrupted = backupsCheck(
    healthy({
      lastRun: run(hours(1), {
        id: "20261011T090000Z",
        result: "interrupted",
        error:
          "the backup service stopped during this run; its files were removed",
      }),
    }),
  );
  assert.equal(interrupted?.state, "warning");
  assert.equal(
    interrupted?.summary,
    "The last backup was interrupted: the backup service stopped during it.",
  );

  // A stale backup whose later runs fail says why it is stale.
  const stalled = backupsCheck(
    healthy({
      lastSuccess: run(hours(60)),
      lastRun: failed,
      lastRestoreCheck: passed(hours(59)),
    }),
  );
  assert.equal(stalled?.state, "problem");
  assert.equal(stalled?.summary, "The last backup is 2 days old.");
  assert.match(
    stalled?.action ?? "",
    /^pg_dump failed: No space left on device/,
  );
});

test("a failed restore check is a problem with each difference", () => {
  const check = backupsCheck(
    healthy({
      lastRestoreCheck: passed(hours(7.9), {
        ok: false,
        error: "1 of 2 databases did not restore as their manifest says",
        databases: [
          {
            name: "finance_db",
            ok: false,
            tables: 9,
            rows: 1833,
            mismatches: [
              {
                subject: "public.accounts",
                expected: "6 rows",
                actual: "5 rows",
              },
              {
                subject: "migration",
                expected: "version 14",
                actual: "version 13",
              },
            ],
          },
          {
            name: "tasks_db",
            ok: false,
            tables: 0,
            rows: 0,
            mismatches: [],
            error: "the dump's SHA-256 is ab…, the manifest says cd…",
          },
        ],
      }),
    }),
  );
  assert.equal(check?.state, "problem");
  assert.equal(
    check?.summary,
    "The last restore check failed: finance_db, tasks_db did not restore as their backup says.",
  );
  assert.equal(
    check?.detail,
    "finance_db: public.accounts: expected 6 rows, restored 5 rows; migration: expected version 14, restored version 13\ntasks_db: the dump's SHA-256 is ab…, the manifest says cd…",
  );
  assert.equal(check?.action, RESTORE_CHECK_FAILED);
  assert.match(fact(check, "Last restore check") ?? "", /^failed, /);
});

test("little free space where backups are kept needs attention", () => {
  const check = backupsCheck(
    healthy({
      storage: {
        runs: 20,
        totalBytes: 48_000_000,
        freeBytes: 4_000_000_000,
        sizeBytes: 230_000_000_000,
      },
    }),
  );
  assert.equal(check?.state, "warning");
  assert.equal(check?.summary, "Only 4.0 GB is free where backups are kept.");
  assert.equal(check?.action, BACKUP_DISK_LOW);
});

test("restore checks are owed within 8 days of the first backup, and every 8 days", () => {
  const fresh = backupsCheck(
    healthy({ lastRestoreCheck: undefined, oldestSuccessAt: hours(24 * 2) }),
  );
  assert.equal(fresh?.state, "ok");
  const never = backupsCheck(
    healthy({ lastRestoreCheck: undefined, oldestSuccessAt: hours(24 * 9) }),
  );
  assert.equal(never?.state, "warning");
  assert.equal(
    never?.summary,
    "No restore check has run in the 9 days since backups began.",
  );
  assert.equal(never?.action, RESTORE_CHECK_OVERDUE);
  const old = backupsCheck(
    healthy({ lastRestoreCheck: passed(hours(24 * 9)) }),
  );
  assert.equal(old?.summary, "No restore check for 9 days.");
  // Turned off on purpose: nothing is owed.
  const off = healthy({
    lastRestoreCheck: undefined,
    oldestSuccessAt: hours(24 * 30),
  });
  off.schedule = {
    ...off.schedule,
    restoreCheckDay: "off",
    nextRestoreCheckAt: undefined,
  };
  const check = backupsCheck(off);
  assert.equal(check?.state, "ok");
  assert.equal(fact(check, "Last restore check"), "off");
});

test("several issues: the worst leads, the others are listed", () => {
  const check = backupsCheck(
    healthy({
      lastRun: run(hours(30)),
      lastSuccess: run(hours(30)),
      lastRestoreCheck: passed(hours(29), {
        ok: false,
        databases: [
          {
            name: "tasks_db",
            ok: false,
            tables: 5,
            rows: 119,
            mismatches: [
              {
                subject: "public.tasks",
                expected: "120 rows",
                actual: "119 rows",
              },
            ],
          },
        ],
      }),
      storage: {
        runs: 20,
        totalBytes: 48_000_000,
        freeBytes: 1_000_000_000,
        sizeBytes: 230_000_000_000,
      },
    }),
  );
  assert.equal(check?.state, "problem");
  assert.equal(
    check?.summary,
    "The last restore check failed: tasks_db did not restore as its backup says.",
  );
  assert.equal(
    fact(check, "Also"),
    "The last backup is 30 h 0 min old. Only 1.0 GB is free where backups are kept.",
  );
  assert.equal(check?.facts[0].label, "Also");
  assert.ok(
    statusAlerts(
      toRuntimeStatus(
        signals(
          healthy({ lastRestoreCheck: passed(hours(7.9), { ok: false }) }),
        ),
        now,
        timeZone,
      ),
    ).some((check) => check.id === "backups" && check.state === "problem"),
  );
});

test("a database the last backup does not have yet is named", () => {
  const check = backupsCheck(
    healthy({ databases: ["finance_db", "tasks_db", "food_db"] }),
  );
  assert.equal(check?.state, "ok");
  assert.equal(fact(check, "Databases"), "2 of 3 (not in it yet: food_db)");
});

test("a backup or restore check in progress is shown", () => {
  const check = backupsCheck(
    healthy({
      runningRestoreCheck: {
        id: "20261011T095900Z",
        runId: "20261011T011500Z",
        trigger: "manual",
        startedAt: hours(0.01),
        databases: ["finance_db", "tasks_db"],
        completed: 0,
        current: "finance_db",
      },
      schedule: { ...healthy().schedule, nextTrigger: "catch-up" },
    }),
  );
  assert.equal(fact(check, "Restore check now"), "finance_db (0 of 2 done)");
  assert.equal(
    fact(check, "Next backup"),
    "12 Oct 2026, 03:15 CEST (catching up)",
  );
  assert.deepEqual(check?.control, { kind: "backup-now", running: true });
});

test("formatBytes counts in powers of 1000", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(999), "999 B");
  assert.equal(formatBytes(1_536), "1.5 kB");
  assert.equal(formatBytes(48_000_000), "48.0 MB");
  assert.equal(formatBytes(5_000_000_000), "5.0 GB");
  assert.equal(formatBytes(230_000_000_000), "230 GB");
  assert.equal(formatBytes(2_500_000_000_000), "2.5 TB");
});
