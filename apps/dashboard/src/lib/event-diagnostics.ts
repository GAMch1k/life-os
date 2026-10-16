import type { ActivityEvent } from "@/lib/activity-log";

/**
 * The Event Log's Diagnostics fields of one event, as the detail view lists
 * them: raw values, times in UTC as recorded.
 */
export function diagnosticFields(event: ActivityEvent): [string, string][] {
  return [
    ["Event id", event.id],
    ["Type", event.type],
    ["Source", event.source],
    ["Subject", `${event.subject.type}/${event.subject.id}`],
    ["Correlation", event.correlationId],
    ["Caused by", event.causationId ?? "—"],
    [
      "Actor",
      `${event.actor.kind}${event.actor.name ? `:${event.actor.name}` : ""}`,
    ],
    ["Occurred", event.occurredAt],
    ["Recorded", event.recordedAt],
  ];
}

/**
 * The whole diagnostics of one event as plain text, for Copy: each field on
 * its own line (the Diagnostics fields, then the outcome and summary the view
 * shows above them), a blank line, and the event data as formatted JSON.
 */
export function diagnosticsText(event: ActivityEvent): string {
  const fields: [string, string][] = [
    ...diagnosticFields(event),
    ["Outcome", event.outcome],
    // One line per field: a summary never breaks the list.
    ["Summary", event.summary.replace(/\s+/g, " ").trim()],
  ];
  return [
    ...fields.map(([label, value]) => `${label}: ${value}`),
    "",
    "Event data:",
    eventDataText(event),
  ].join("\n");
}

/** The event data as the view shows it: JSON, indented by two spaces. */
export function eventDataText(event: Pick<ActivityEvent, "data">): string {
  return JSON.stringify(event.data, null, 2);
}
