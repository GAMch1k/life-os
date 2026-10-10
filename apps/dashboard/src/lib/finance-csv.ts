// Shared by the Finance page (server) and the CSV import button (client); a
// constant exported from a "use client" module would reach server components
// only as a client reference, so it lives here.

/** The columns a CSV needs, as the import dialog lists them. */
export const CSV_COLUMNS =
  "date and amount; optional counterparty, description, category, notes, external_id";

/** How the import reads a file, in one sentence. */
export const CSV_FORMAT =
  "Comma- or semicolon-separated, UTF-8, with a header row; dates as 2026-10-09 or 09.10.2026, amounts as -12.50 or -12,50 (negative is money out); the category is a category's name.";

/** Bank exports use their own headers until the bank connection arrives. */
export const CSV_BANK_HINT =
  "A Deutsche Bank CSV export may need its columns renamed to these before importing.";

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
