"use client";

import { Trash2 } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useId,
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
import { DatePicker } from "@/components/date-picker";
import { Amount } from "@/components/finance-amount";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOptGroup,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { financeClient } from "@/lib/api/finance-client";
import {
  type Account,
  type Category,
  type DraftErrors,
  SOURCE_LABELS,
  type Transaction,
  type TransactionDraft,
  bookingDayLabel,
  byId,
  categoryGroups,
  createTransactionBody,
  financeErrorMessage,
  idempotencyKey,
  newTransactionDraft,
  readOnlyReason,
  splitAccounts,
  transactionDraft,
  updateTransactionBody,
} from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { useClosingState } from "@/lib/use-closing-state";

/** The transaction dialog: a new one (into an account), or one to open. */
export type TransactionDialogState =
  | { mode: "new"; accountId?: string }
  | { mode: "edit"; transaction: Transaction }
  | null;

type Shared = {
  accounts: Account[];
  categories: Category[];
  /** The owner's today (YYYY-MM-DD), a new transaction's date. */
  today: string;
};

/**
 * Adds a manual transaction, or opens one. A manual transaction changes in
 * full and can be deleted; an imported one (CSV now, a bank later) shows its
 * details read-only and changes only its category and notes.
 */
export function TransactionDialog({
  state,
  onClose,
  onCreated,
  onSaved,
  onDeleted,
  ...shared
}: Shared & {
  state: TransactionDialogState;
  onClose: () => void;
  onCreated: (transaction: Transaction) => void;
  onSaved: (transaction: Transaction) => void;
  onDeleted: (transaction: Transaction) => void;
}) {
  const current = useClosingState(state);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={state !== null}
    >
      <DialogContent className="sm:max-w-lg">
        {current ? (
          <TransactionForm
            accountId={current.mode === "new" ? current.accountId : undefined}
            key={
              current.mode === "edit"
                ? `${current.transaction.id}-${current.transaction.updatedAt}`
                : "new"
            }
            onCancel={onClose}
            onCreated={(transaction) => {
              onCreated(transaction);
              onClose();
            }}
            onDeleted={(transaction) => {
              onDeleted(transaction);
              onClose();
            }}
            onSaved={(transaction) => {
              onSaved(transaction);
              onClose();
            }}
            transaction={
              current.mode === "edit" ? current.transaction : undefined
            }
            {...shared}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function TransactionForm({
  transaction,
  accountId,
  accounts,
  categories,
  today,
  onCancel,
  onCreated,
  onSaved,
  onDeleted,
}: Shared & {
  transaction?: Transaction;
  accountId?: string;
  onCancel: () => void;
  onCreated: (transaction: Transaction) => void;
  onSaved: (transaction: Transaction) => void;
  onDeleted: (transaction: Transaction) => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const [draft, setDraft] = useState<TransactionDraft>(() =>
    transaction
      ? transactionDraft(transaction, locale)
      : newTransactionDraft(accounts, today, accountId),
  );
  const [errors, setErrors] = useState<DraftErrors & { form?: string }>({});
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pending, startTransition] = useTransition();
  // One key per dialog: sending the same transaction again (a retry after a
  // lost answer) returns the first one instead of a duplicate.
  const [key] = useState(idempotencyKey);
  const accountsById = byId(accounts);
  const account = accountsById.get(draft.accountId);
  const readOnly = Boolean(transaction?.readOnly);
  const update = (patch: Partial<TransactionDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  const invalid = (field: keyof DraftErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const send = (
    request: () => Promise<{ data?: Transaction; error?: unknown }>,
    fallback: string,
    done: (transaction: Transaction) => void,
  ) =>
    startTransition(async () => {
      setErrors({});
      const { data, error } = await request().catch(() => ({
        data: undefined,
        error: undefined,
      }));
      if (!data)
        return setErrors({ form: financeErrorMessage(error, fallback) });
      done(data);
    });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!transaction) {
      const { body, errors: found } = createTransactionBody(draft, account);
      if (!body) return setErrors(found);
      return send(
        () =>
          financeClient.POST("/v1/transactions", {
            body,
            headers: { "Idempotency-Key": key },
          }),
        "Could not add this transaction.",
        onCreated,
      );
    }
    const { body, errors: found } = updateTransactionBody(
      transaction,
      draft,
      account,
    );
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onCancel();
    send(
      () =>
        financeClient.PATCH("/v1/transactions/{transactionId}", {
          params: { path: { transactionId: transaction.id } },
          body,
        }),
      "Could not save this transaction.",
      onSaved,
    );
  };

  const remove = () =>
    transaction &&
    startTransition(async () => {
      setErrors({});
      const { response, error } = await financeClient
        .DELETE("/v1/transactions/{transactionId}", {
          params: { path: { transactionId: transaction.id } },
        })
        .catch(() => ({ response: undefined, error: undefined }));
      if (!response?.ok) {
        setConfirmDelete(false);
        return setErrors({
          form: financeErrorMessage(
            error,
            "Could not delete this transaction.",
          ),
        });
      }
      onDeleted(transaction);
    });

  // Archived accounts take no new transactions; one being edited keeps its
  // own in the list.
  const accountChoices = splitAccounts(accounts).active.concat(
    transaction && accountsById.get(transaction.accountId)?.archived
      ? [accountsById.get(transaction.accountId) as Account]
      : [],
  );
  // A rule chose the category shown (until the owner picks another).
  const ruleChosen =
    transaction?.categorizedBy === "rule" &&
    draft.categoryId === transaction.categoryId;
  const categoryField = (
    <Field>
      <FieldLabel htmlFor={`${id}-category`}>Category</FieldLabel>
      <NativeSelect
        aria-describedby={ruleChosen ? `${id}-category-note` : undefined}
        className="w-full"
        id={`${id}-category`}
        onChange={(event) => update({ categoryId: event.target.value })}
        value={draft.categoryId}
      >
        <NativeSelectOption value="">
          {transaction ? "No category" : "Let the rules choose"}
        </NativeSelectOption>
        {categoryGroups(categories, transaction?.categoryId).map((group) => (
          <NativeSelectOptGroup key={group.kind} label={group.label}>
            {group.items.map((category) => (
              <NativeSelectOption key={category.id} value={category.id}>
                {category.name}
                {category.archived ? " (archived)" : ""}
              </NativeSelectOption>
            ))}
          </NativeSelectOptGroup>
        ))}
      </NativeSelect>
      {ruleChosen ? (
        <FieldDescription className="text-xs" id={`${id}-category-note`}>
          Chosen by a rule. Picking one here keeps the rules away from it.
        </FieldDescription>
      ) : null}
    </Field>
  );
  const notesField = (
    <Field>
      <FieldLabel htmlFor={`${id}-notes`}>Notes</FieldLabel>
      <Textarea
        id={`${id}-notes`}
        maxLength={2000}
        onChange={(event) => update({ notes: event.target.value })}
        value={draft.notes}
      />
    </Field>
  );

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {!transaction
            ? "Add transaction"
            : readOnly
              ? "Imported transaction"
              : "Edit transaction"}
        </DialogTitle>
        {transaction && readOnly ? (
          <DialogDescription>{readOnlyReason(transaction)}</DialogDescription>
        ) : !transaction ? (
          <DialogDescription>
            Money you spent or received, such as cash at the bakery.
          </DialogDescription>
        ) : null}
      </DialogHeader>

      {transaction && readOnly ? (
        <ImportedDetails
          account={accountsById.get(transaction.accountId)}
          today={today}
          transaction={transaction}
        />
      ) : (
        <>
          <ToggleGroup
            aria-label="Direction"
            className="w-full"
            onValueChange={(chosen) => {
              // Pressing the chosen side again would leave none chosen.
              if (chosen[0] === "in" || chosen[0] === "out")
                update({ direction: chosen[0] });
            }}
            spacing={0}
            value={[draft.direction]}
            variant="outline"
          >
            <ToggleGroupItem
              className="aria-pressed:bg-primary aria-pressed:text-primary-foreground flex-1"
              value="out"
            >
              Money out
            </ToggleGroupItem>
            <ToggleGroupItem
              className="aria-pressed:bg-primary aria-pressed:text-primary-foreground flex-1"
              value="in"
            >
              Money in
            </ToggleGroupItem>
          </ToggleGroup>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field data-invalid={Boolean(errors.amount)}>
              <FieldLabel htmlFor={`${id}-amount`}>Amount</FieldLabel>
              <div className="relative">
                <Input
                  autoFocus={!transaction}
                  className="pr-12"
                  id={`${id}-amount`}
                  inputMode="decimal"
                  onChange={(event) => update({ amount: event.target.value })}
                  placeholder="0.00"
                  value={draft.amount}
                  {...invalid("amount")}
                />
                <span className="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs">
                  {account?.currency}
                </span>
              </div>
              <FieldError id={`${id}-amount-error`}>{errors.amount}</FieldError>
            </Field>
            <Field data-invalid={Boolean(errors.bookingDate)}>
              <FieldLabel htmlFor={`${id}-date`}>Date</FieldLabel>
              <DatePicker
                className="w-full"
                id={`${id}-date`}
                label="Date"
                onChange={(bookingDate) => update({ bookingDate })}
                required
                today={today}
                value={draft.bookingDate}
                {...invalid("bookingDate")}
              />
              <FieldError id={`${id}-bookingDate-error`}>
                {errors.bookingDate}
              </FieldError>
            </Field>
          </div>

          <Field data-invalid={Boolean(errors.accountId)}>
            <FieldLabel htmlFor={`${id}-account`}>Account</FieldLabel>
            <NativeSelect
              className="w-full"
              id={`${id}-account`}
              onChange={(event) => update({ accountId: event.target.value })}
              value={draft.accountId}
              {...invalid("accountId")}
            >
              {accountChoices.map((choice) => (
                <NativeSelectOption key={choice.id} value={choice.id}>
                  {choice.name} ({choice.currency})
                  {choice.archived ? ", archived" : ""}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <FieldError id={`${id}-accountId-error`}>
              {errors.accountId}
            </FieldError>
          </Field>

          <Label className="w-fit font-normal">
            <Checkbox
              checked={draft.pending}
              onCheckedChange={(checked) => update({ pending: checked })}
            />
            Pending (not settled yet, so not in the balance)
          </Label>

          <Field>
            <FieldLabel htmlFor={`${id}-counterparty`}>Counterparty</FieldLabel>
            <Input
              id={`${id}-counterparty`}
              maxLength={200}
              onChange={(event) => update({ counterparty: event.target.value })}
              placeholder="Who was paid, or who paid"
              value={draft.counterparty}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-description`}>Description</FieldLabel>
            <Input
              id={`${id}-description`}
              maxLength={1000}
              onChange={(event) => update({ description: event.target.value })}
              placeholder="What it was for"
              value={draft.description}
            />
          </Field>
        </>
      )}

      {categoryField}
      {notesField}

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter className="sm:justify-between">
        {confirmDelete ? (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <p className="text-sm font-medium">Delete this transaction?</p>
            <div className="flex gap-2">
              <Button
                disabled={pending}
                onClick={remove}
                type="button"
                variant="destructive"
              >
                {pending ? "Deleting…" : "Delete"}
              </Button>
              <Button
                disabled={pending}
                onClick={() => setConfirmDelete(false)}
                type="button"
                variant="outline"
              >
                Keep
              </Button>
            </div>
          </div>
        ) : (
          <>
            {transaction && !readOnly ? (
              <Button
                disabled={pending}
                onClick={() => setConfirmDelete(true)}
                type="button"
                variant="ghost"
              >
                <Trash2 aria-hidden="true" />
                Delete transaction
              </Button>
            ) : (
              <span className="hidden sm:block" />
            )}
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              <Button
                disabled={pending}
                onClick={onCancel}
                type="button"
                variant="outline"
              >
                Cancel
              </Button>
              <Button disabled={pending} type="submit">
                {pending ? "Saving…" : transaction ? "Save" : "Add transaction"}
              </Button>
            </div>
          </>
        )}
      </DialogFooter>
    </form>
  );
}

/** An imported transaction's details, which its source keeps. */
function ImportedDetails({
  transaction,
  account,
  today,
}: {
  transaction: Transaction;
  account?: Account;
  today: string;
}) {
  const locale = useBrowserLocale();
  const rows: [string, ReactNode][] = [
    ["Amount", <Amount key="amount" money={transaction.amount} signed />],
    ["Date", bookingDayLabel(transaction.bookingDate, today, locale)],
    ...(transaction.valueDate &&
    transaction.valueDate !== transaction.bookingDate
      ? ([
          ["Value date", bookingDayLabel(transaction.valueDate, today, locale)],
        ] as [string, ReactNode][])
      : []),
    ["Account", account?.name ?? "Unknown account"],
    ["Counterparty", transaction.counterparty || "—"],
    ["Description", transaction.description || "—"],
    [
      "Status",
      transaction.status === "pending"
        ? "Pending, not in the balance"
        : "Booked",
    ],
    ["Source", SOURCE_LABELS[transaction.source.kind]],
  ];
  return (
    <dl className="bg-muted/40 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 rounded-lg border p-3 text-sm">
      {rows.map(([name, value]) => (
        <div className="contents" key={name}>
          <dt className="text-muted-foreground">{name}</dt>
          <dd className="wrap-anywhere">{value}</dd>
        </div>
      ))}
    </dl>
  );
}
