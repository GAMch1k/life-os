"use client";

import { FileUp, LoaderCircle } from "lucide-react";
import {
  type ChangeEvent,
  useId,
  useRef,
  useState,
  useTransition,
} from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { financeClient } from "@/lib/api/finance-client";
import {
  CSV_BANK_HINT,
  CSV_COLUMNS,
  CSV_FORMAT,
  csvFileProblem,
} from "@/lib/finance-csv";
import {
  type Account,
  type CsvImport,
  financeErrorMessage,
  importResultText,
  splitAccounts,
} from "@/lib/finance-workspace";

/** The account an import goes to first: a bank account, else Cash. */
function importAccount(accounts: Account[]) {
  const { active } = splitAccounts(accounts);
  return (
    active.find((account) => account.kind === "bank" && !account.builtIn) ??
    active.find((account) => !account.builtIn) ??
    active[0]
  );
}

/**
 * Imports a CSV file into the account the owner picks. Imported rows are
 * read-only afterwards (only category and notes change, and they can't be
 * deleted), so the file is chosen first and imported only on "Import".
 * Re-importing a file skips the rows the account already has.
 */
export function FinanceImport({
  accounts,
  onImported,
}: {
  accounts: Account[];
  /** Transactions were imported: balances and lists are out of date. */
  onImported: (result: CsvImport) => void;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [accountId, setAccountId] = useState("");
  const [file, setFile] = useState<File>();
  const [error, setError] = useState<string>();
  const [result, setResult] = useState<CsvImport>();
  const [pending, startTransition] = useTransition();
  const inputRef = useRef<HTMLInputElement>(null);
  const { active } = splitAccounts(accounts);
  const chosen =
    active.find((account) => account.id === accountId) ??
    importAccount(accounts);

  const show = (next: boolean) => {
    setOpen(next);
    if (next) {
      setAccountId(importAccount(accounts)?.id ?? "");
      setFile(undefined);
      setError(undefined);
      setResult(undefined);
    }
  };

  const choose = (event: ChangeEvent<HTMLInputElement>) => {
    const picked = event.target.files?.[0];
    event.target.value = "";
    if (!picked) return;
    setResult(undefined);
    const problem = csvFileProblem(picked);
    setError(problem);
    setFile(problem ? undefined : picked);
  };

  const upload = () =>
    file &&
    chosen &&
    startTransition(async () => {
      setError(undefined);
      setResult(undefined);
      const { data, error: requestError } = await financeClient
        .POST("/v1/imports/csv", {
          params: { query: { accountId: chosen.id } },
          body: await file.text(),
          headers: { "content-type": "text/csv" },
          bodySerializer: (body) => body as string,
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setError(
          financeErrorMessage(
            requestError,
            `The CSV could not be imported. It needs the columns ${CSV_COLUMNS}.`,
          ),
        );
      }
      setResult(data);
      setFile(undefined);
      if (data.imported > 0) onImported(data);
    });

  return (
    <>
      <Button
        disabled={active.length === 0}
        onClick={() => show(true)}
        variant="outline"
      >
        <FileUp aria-hidden="true" />
        Import CSV
      </Button>
      <Dialog onOpenChange={show} open={open}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Import CSV</DialogTitle>
            <DialogDescription>
              Adds the file&apos;s transactions to the account you choose.
              Imported transactions stay as the file has them: later only their
              category and notes change, and they can&apos;t be deleted.
            </DialogDescription>
          </DialogHeader>

          <Field>
            <FieldLabel htmlFor={`${id}-account`}>Account</FieldLabel>
            <NativeSelect
              className="w-full"
              disabled={pending}
              id={`${id}-account`}
              onChange={(event) => setAccountId(event.target.value)}
              value={chosen?.id ?? ""}
            >
              {active.map((account) => (
                <NativeSelectOption key={account.id} value={account.id}>
                  {account.name} ({account.currency})
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </Field>

          <Field>
            <FieldLabel htmlFor={`${id}-file`}>File</FieldLabel>
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <Button
                disabled={pending}
                id={`${id}-file`}
                onClick={() => inputRef.current?.click()}
                type="button"
                variant="outline"
              >
                Choose file
              </Button>
              <span
                className="text-muted-foreground min-w-0 truncate text-sm"
                data-testid="finance-import-file"
              >
                {file ? file.name : "No file chosen"}
              </span>
            </div>
            {/* A plain input: the styled Input is full width, which made the
                page scroll sideways even with sr-only (BUG-11). The button
                opens it. */}
            <input
              accept=".csv,text/csv"
              aria-hidden="true"
              className="sr-only"
              disabled={pending}
              onChange={choose}
              ref={inputRef}
              tabIndex={-1}
              type="file"
            />
            <FieldDescription className="text-xs wrap-anywhere">
              Columns: {CSV_COLUMNS}. {CSV_FORMAT} {CSV_BANK_HINT}
            </FieldDescription>
          </Field>

          {result ? (
            <div className="text-sm" role="status">
              <p>{importResultText(result)}</p>
              {result.errors.length ? (
                <ul className="text-destructive mt-1 max-h-40 list-disc overflow-y-auto pl-5 wrap-anywhere">
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
            <p className="text-destructive text-sm wrap-anywhere" role="alert">
              {error}
            </p>
          ) : null}

          <DialogFooter>
            <Button
              disabled={pending}
              onClick={() => show(false)}
              type="button"
              variant="outline"
            >
              {result ? "Close" : "Cancel"}
            </Button>
            <Button
              disabled={pending || !file || !chosen}
              onClick={upload}
              type="button"
            >
              {pending ? (
                <LoaderCircle aria-hidden="true" className="animate-spin" />
              ) : null}
              {chosen ? `Import into ${chosen.name}` : "Import"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
