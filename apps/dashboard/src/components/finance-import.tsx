"use client";

import { FileUp, LoaderCircle } from "lucide-react";
import { type ChangeEvent, useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";

import { Button } from "@/components/ui/button";
import { financeClient } from "@/lib/api/finance-client";
import type { components } from "@/lib/api/generated/finance";
import { CSV_COLUMNS, csvFileProblem } from "@/lib/finance-csv";

type CsvImport = components["schemas"]["CsvImport"];

/**
 * The CSV import button and its feedback. It renders inside a wrapping flex
 * row: the button sits in the row, the feedback takes a full-width line.
 */
export function FinanceImport() {
  const router = useRouter();
  const [error, setError] = useState<string>();
  const [result, setResult] = useState<CsvImport>();
  const [isPending, startTransition] = useTransition();
  const inputRef = useRef<HTMLInputElement>(null);

  function upload(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    setResult(undefined);
    const problem = csvFileProblem(file);
    setError(problem);
    if (problem) return;

    startTransition(async () => {
      const { data, error: requestError } = await financeClient.POST(
        "/v1/imports/csv",
        {
          body: await file.text(),
          headers: { "content-type": "text/csv" },
          bodySerializer: (body) => body as string,
        },
      );
      if (requestError || !data) {
        setError(
          `The CSV could not be imported. Expected columns: ${CSV_COLUMNS}.`,
        );
        return;
      }
      setResult(data);
      router.refresh();
    });
  }

  return (
    <>
      <Button
        disabled={isPending}
        onClick={() => inputRef.current?.click()}
        size="sm"
        type="button"
        variant="outline"
      >
        {isPending ? (
          <LoaderCircle aria-hidden="true" className="animate-spin" />
        ) : (
          <FileUp aria-hidden="true" />
        )}
        Import CSV
      </Button>
      {/* A plain input: the styled Input is full width, which made the page
          scroll sideways even with sr-only (BUG-11). The button opens it. */}
      <input
        accept=".csv,text/csv"
        aria-hidden="true"
        className="sr-only"
        disabled={isPending}
        onChange={upload}
        ref={inputRef}
        tabIndex={-1}
        type="file"
      />
      {result ? (
        <div className="basis-full text-sm" role="status">
          Imported {result.created}; skipped {result.skipped}; failed{" "}
          {result.failed}.
          {result.errors.length ? (
            <ul className="text-destructive mt-1 list-disc pl-5 wrap-anywhere">
              {result.errors.map((item) => (
                <li key={`${item.row}-${item.message}`}>
                  Row {item.row}: {item.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
      {error ? (
        <p
          className="text-destructive basis-full text-sm wrap-anywhere"
          role="alert"
        >
          {error}
        </p>
      ) : null}
    </>
  );
}
