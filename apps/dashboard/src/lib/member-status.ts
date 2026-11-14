import type { RuntimeStatus, StatusCheck } from "@/lib/runtime-status";

/**
 * Status for members (docs/architecture/multi-user.md#roles-and-checks):
 * one line for the modules they use, "Everything works." or "Calendar has
 * a problem.", without detail. The details (containers, the host's memory
 * and disks, backups, the AI plan's usage, errors) are the server's
 * admins'. Until modules can be allowed per person (P21-12), every module
 * that is on is everyone's.
 *
 * A problem is a check in the problem state. A warning (a backlog, a bank
 * consent that expires soon) is the admins' to act on and leaves the line
 * at "Everything works.".
 */

export type MemberStatus = {
  view: "member";
  checkedAt: string;
  timeZone: string;
  overall: "ok" | "problem";
  /** The modules with a problem, in Status's order, each once. */
  problems: string[];
  /**
   * Why someone who is an admin sees this view: admin pages need
   * two-factor sign-in first.
   */
  reason?: "second_factor_required";
  /** Empty: a member gets no checks (clients of the full view expect it). */
  checks: [];
};

/** The module a check is about; null for the server's own checks. */
const CHECK_MODULES: Record<StatusCheck["id"], string | null> = {
  processing: "AI processing",
  inbox: "Inbox",
  voice: "Voice notes",
  calendar: "Calendar",
  notifications: "Notifications",
  banking: "Bank sync",
  backups: null,
  server: null,
  services: null,
};

/** The module a service under Services belongs to (src/lib/runtime-status.ts). */
const SERVICE_MODULES: Record<string, string | null> = {
  Tasks: "Tasks",
  Inbox: "Inbox",
  Calendar: "Calendar",
  Finance: "Finance",
  Voice: "Voice notes",
  Activity: "Activity",
  Notifications: "Notifications",
  Banking: "Bank sync",
  "Calendar feeds": "Calendar",
  "Google Calendar": "Calendar",
  Food: "Food",
  "Recipe import": "Food",
  Backup: null,
  "Host health": null,
  Monitor: null,
};

export function memberStatus(
  status: RuntimeStatus,
  reason?: MemberStatus["reason"],
): MemberStatus {
  const problems: string[] = [];
  const add = (module: string | null | undefined) => {
    if (module && !problems.includes(module)) problems.push(module);
  };
  for (const check of status.checks) {
    if (check.id === "services") {
      for (const fact of check.facts) {
        if (fact.value !== "ready") add(SERVICE_MODULES[fact.label]);
      }
    } else if (check.state === "problem") {
      add(CHECK_MODULES[check.id]);
    }
  }
  return {
    view: "member",
    checkedAt: status.checkedAt,
    timeZone: status.timeZone,
    overall: problems.length ? "problem" : "ok",
    problems,
    ...(reason ? { reason } : {}),
    checks: [],
  };
}

/** Whether a view of Status is a member's (the full one has checks). */
export function isMemberStatus(
  status: RuntimeStatus | MemberStatus,
): status is MemberStatus {
  return (status as MemberStatus).view === "member";
}

/** "Everything works." or "Calendar and Food have a problem." */
export function memberLine(problems: readonly string[]): string {
  if (problems.length === 0) return "Everything works.";
  if (problems.length === 1) return `${problems[0]} has a problem.`;
  const names =
    problems.length === 2
      ? `${problems[0]} and ${problems[1]}`
      : `${problems.slice(0, -1).join(", ")}, and ${problems.at(-1)}`;
  return `${names} have a problem.`;
}
