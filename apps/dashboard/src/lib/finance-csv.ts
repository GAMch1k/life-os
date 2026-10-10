// Shared by the Finance page (server) and the CSV import button (client); a
// constant exported from a "use client" module would reach server components
// only as a client reference, so it lives here.

export const CSV_COLUMNS =
  "account_id, occurred_on, amount_minor, currency, external_id; optional category_id, note";

/** The Finance service accepts request bodies up to 4 MiB. */
export const MAX_CSV_BYTES = 4 * 1024 * 1024;

/** Why a chosen file cannot be imported, or undefined when it can. */
export function csvFileProblem(file: { name: string; size: number }) {
  if (!file.name.toLowerCase().endsWith(".csv")) {
    return "Choose a .csv file.";
  }
  if (file.size > MAX_CSV_BYTES) {
    return "This CSV is over 4 MB. Split it into smaller files.";
  }
  return undefined;
}
