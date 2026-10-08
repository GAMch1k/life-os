"use client";

import { FileUp, LoaderCircle } from "lucide-react";
import { type ChangeEvent, useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { financeClient } from "@/lib/api/finance-client";
import type { components } from "@/lib/api/generated/finance";

type CsvImport = components["schemas"]["CsvImport"];

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
    if (!file.name.toLowerCase().endsWith(".csv")) {
      setResult(undefined);
      setError("Choose a .csv file.");
      return;
    }

    setError(undefined);
    setResult(undefined);
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
          "The CSV could not be imported. Check the file and try again.",
        );
        return;
      }
      setResult(data);
      router.refresh();
    });
  }

  return (
    <section className="rounded-lg border p-4 sm:col-span-2 lg:col-span-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="text-sm font-medium">Import transactions</p>
          <p className="text-muted-foreground mt-1 text-xs">
            CSV: account_id, occurred_on, amount_minor, currency, external_id;
            category_id and note are optional.
          </p>
        </div>
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
          Select CSV
        </Button>
      </div>
      <Input
        accept=".csv,text/csv"
        className="sr-only"
        disabled={isPending}
        onChange={upload}
        ref={inputRef}
        type="file"
      />
      {result ? (
        <div className="mt-3 text-sm" role="status">
          Imported {result.created}; skipped {result.skipped}; failed{" "}
          {result.failed}.
          {result.errors.length ? (
            <ul className="text-destructive mt-2 list-disc pl-5">
              {result.errors.map((item) => (
                <li key={`${item.row}-${item.message}`}>
                  Row {item.row}: {item.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
      {error ? <p className="text-destructive mt-3 text-sm">{error}</p> : null}
    </section>
  );
}
