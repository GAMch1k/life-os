"use client";

import { Archive, ArchiveRestore, Plus } from "lucide-react";
import { type FormEvent, useId, useState, useTransition } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Amount, BalanceBreakdown } from "@/components/finance-amount";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { financeClient } from "@/lib/api/finance-client";
import {
  ACCOUNT_KINDS,
  type Account,
  type AccountErrors,
  type AccountKind,
  accountDraft,
  createAccountBody,
  financeErrorMessage,
  label,
  splitAccounts,
  updateAccountBody,
} from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { useClosingState } from "@/lib/use-closing-state";
import { cn } from "@/lib/utils";

/** The account dialog: a new account, or one to edit. */
export type AccountDialogState = { account?: Account } | null;

/**
 * The accounts with their balances, the built-in Cash account first. A
 * balance is the money there is to spend, pending card payments included;
 * a quiet line under it says what is booked and what is pending. A card's
 * main part shows only that account's transactions (pressed again, all of
 * them); Edit opens the account, where it is archived or restored.
 */
export function FinanceAccounts({
  accounts,
  selectedId,
  onSelect,
  onEdit,
}: {
  accounts: Account[];
  /** The account the transaction list is filtered to, or "". */
  selectedId: string;
  onSelect: (accountId: string) => void;
  onEdit: (state: NonNullable<AccountDialogState>) => void;
}) {
  const [showArchived, setShowArchived] = useState(false);
  const { active, archived } = splitAccounts(accounts);
  return (
    <section
      aria-labelledby="finance-accounts-title"
      data-testid="finance-accounts"
    >
      <div className="mb-1.5 flex min-h-7 flex-wrap items-center gap-2">
        <h2 className="text-sm font-semibold" id="finance-accounts-title">
          Accounts
        </h2>
        <span className="text-muted-foreground text-xs tabular-nums">
          {active.length}
        </span>
        <div className="ml-auto flex gap-1">
          {archived.length ? (
            <Button
              aria-pressed={showArchived}
              className="aria-pressed:bg-muted"
              onClick={() => setShowArchived(!showArchived)}
              size="sm"
              variant="ghost"
            >
              {showArchived ? "Hide archived" : `Archived (${archived.length})`}
            </Button>
          ) : null}
          <Button onClick={() => onEdit({})} size="sm" variant="outline">
            <Plus aria-hidden="true" />
            Add account
          </Button>
        </div>
      </div>
      <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        {[...active, ...(showArchived ? archived : [])].map((account) => (
          <AccountCard
            account={account}
            key={account.id}
            onEdit={() => onEdit({ account })}
            onSelect={() =>
              onSelect(selectedId === account.id ? "" : account.id)
            }
            selected={selectedId === account.id}
          />
        ))}
      </ul>
    </section>
  );
}

function AccountCard({
  account,
  selected,
  onSelect,
  onEdit,
}: {
  account: Account;
  selected: boolean;
  onSelect: () => void;
  onEdit: () => void;
}) {
  return (
    <li
      className={cn(
        "bg-card flex min-w-0 items-start gap-1 rounded-lg border p-1 transition-colors",
        selected && "border-primary ring-primary/30 ring-2",
        account.archived && "opacity-70",
      )}
      data-testid="finance-account"
    >
      <button
        aria-pressed={selected}
        className="hover:bg-muted/60 focus-visible:ring-ring/50 min-w-0 flex-1 rounded-md px-2 py-1.5 text-left outline-none focus-visible:ring-3"
        onClick={onSelect}
        title={
          selected
            ? "Show every account's transactions"
            : `Show only ${account.name}'s transactions`
        }
        type="button"
      >
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-sm font-medium">{account.name}</span>
          {account.archived ? (
            <Badge variant="outline">Archived</Badge>
          ) : account.source.kind === "bank" ? (
            <Badge variant="secondary">Bank</Badge>
          ) : null}
          {account.lowBalance ? (
            <Badge variant="destructive">Low balance</Badge>
          ) : null}
        </span>
        <span className="text-muted-foreground block truncate text-xs">
          {account.builtIn
            ? "Built-in cash"
            : label(ACCOUNT_KINDS, account.kind)}{" "}
          · {account.transactionCount}{" "}
          {account.transactionCount === 1 ? "transaction" : "transactions"}
        </span>
        <Amount
          className="mt-1 block text-lg font-semibold tracking-tight"
          money={account.balance}
        />
        <BalanceBreakdown
          booked={account.bookedBalance}
          pending={account.pending}
        />
      </button>
      <Button
        aria-label={`Edit account ${account.name}`}
        onClick={onEdit}
        size="xs"
        variant="ghost"
      >
        Edit
      </Button>
    </li>
  );
}

/**
 * Adds an account or edits one: name, kind, currency, and opening balance.
 * Here an account is archived (it leaves the list and takes no new
 * transactions, but keeps its history) or restored. The built-in Cash
 * account can be renamed, but keeps its kind and is never archived.
 */
export function AccountDialog({
  state,
  accounts,
  onClose,
  onSaved,
}: {
  state: AccountDialogState;
  accounts: Account[];
  onClose: () => void;
  onSaved: (account: Account) => void;
}) {
  const current = useClosingState(state);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={state !== null}
    >
      <DialogContent>
        {current ? (
          <AccountForm
            account={current.account}
            accounts={accounts}
            key={current.account?.updatedAt ?? "new"}
            onCancel={onClose}
            onSaved={(account) => {
              onSaved(account);
              onClose();
            }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function AccountForm({
  account,
  accounts,
  onCancel,
  onSaved,
}: {
  account?: Account;
  accounts: Account[];
  onCancel: () => void;
  onSaved: (account: Account) => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const [draft, setDraft] = useState(() =>
    accountDraft(account, accounts, locale),
  );
  const [errors, setErrors] = useState<AccountErrors & { form?: string }>({});
  const [pending, startTransition] = useTransition();
  const currencyLocked = Boolean(account && account.transactionCount > 0);
  const invalid = (field: keyof AccountErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const write = (
    request: () => Promise<{ data?: Account; error?: unknown }>,
    fallback: string,
  ) =>
    startTransition(async () => {
      setErrors({});
      const { data, error } = await request().catch(() => ({
        data: undefined,
        error: undefined,
      }));
      if (!data)
        return setErrors({ form: financeErrorMessage(error, fallback) });
      onSaved(data);
    });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!account) {
      const { body, errors: found } = createAccountBody(draft);
      if (!body) return setErrors(found);
      return write(
        () => financeClient.POST("/v1/accounts", { body }),
        "Could not add this account.",
      );
    }
    const { body, errors: found } = updateAccountBody(account, draft);
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onCancel();
    write(
      () =>
        financeClient.PATCH("/v1/accounts/{accountId}", {
          params: { path: { accountId: account.id } },
          body,
        }),
      "Could not save this account.",
    );
  };

  const archive = (archived: boolean) =>
    account &&
    write(
      () =>
        financeClient.PATCH("/v1/accounts/{accountId}", {
          params: { path: { accountId: account.id } },
          body: { archived },
        }),
      archived
        ? "Could not archive this account."
        : "Could not restore this account.",
    );

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {account ? `Edit ${account.name}` : "Add account"}
        </DialogTitle>
        <DialogDescription>
          {account?.builtIn
            ? "The built-in account for cash spending. It can be renamed, but not archived."
            : account?.archived
              ? "Archived: it takes no new transactions, and its history stays."
              : "An account kept in Life OS, such as a bank account you import CSV files into."}
        </DialogDescription>
      </DialogHeader>

      <FieldGroup className="gap-4">
        <Field data-invalid={Boolean(errors.name)}>
          <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
          <Input
            autoFocus={!account}
            id={`${id}-name`}
            maxLength={100}
            onChange={(event) =>
              setDraft((current) => ({ ...current, name: event.target.value }))
            }
            value={draft.name}
            {...invalid("name")}
          />
          <FieldError id={`${id}-name-error`}>{errors.name}</FieldError>
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${id}-kind`}>Kind</FieldLabel>
            <NativeSelect
              className="w-full"
              disabled={account?.builtIn}
              id={`${id}-kind`}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  kind: event.target.value as AccountKind,
                }))
              }
              value={draft.kind}
            >
              {ACCOUNT_KINDS.map((kind) => (
                <NativeSelectOption key={kind.value} value={kind.value}>
                  {kind.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </Field>
          <Field data-invalid={Boolean(errors.currency)}>
            <FieldLabel htmlFor={`${id}-currency`}>Currency</FieldLabel>
            <Input
              autoCapitalize="characters"
              disabled={currencyLocked}
              id={`${id}-currency`}
              maxLength={3}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  currency: event.target.value.toUpperCase(),
                }))
              }
              placeholder="EUR"
              value={draft.currency}
              {...invalid("currency")}
              aria-describedby={
                errors.currency
                  ? `${id}-currency-error`
                  : currencyLocked
                    ? `${id}-currency-note`
                    : undefined
              }
            />
            {currencyLocked ? (
              <FieldDescription className="text-xs" id={`${id}-currency-note`}>
                It has transactions, so the currency stays.
              </FieldDescription>
            ) : null}
            <FieldError id={`${id}-currency-error`}>
              {errors.currency}
            </FieldError>
          </Field>
        </div>

        <Field data-invalid={Boolean(errors.openingBalance)}>
          <FieldLabel htmlFor={`${id}-opening`}>Opening balance</FieldLabel>
          <Input
            id={`${id}-opening`}
            inputMode="decimal"
            onChange={(event) =>
              setDraft((current) => ({
                ...current,
                openingBalance: event.target.value,
              }))
            }
            placeholder="0.00"
            value={draft.openingBalance}
            {...invalid("openingBalance")}
            aria-describedby={
              errors.openingBalance
                ? `${id}-openingBalance-error`
                : `${id}-opening-note`
            }
          />
          <FieldDescription className="text-xs" id={`${id}-opening-note`}>
            What it held before its first transaction here; negative when
            overdrawn.
          </FieldDescription>
          <FieldError id={`${id}-openingBalance-error`}>
            {errors.openingBalance}
          </FieldError>
        </Field>
      </FieldGroup>

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter className="sm:justify-between">
        {account && !account.builtIn ? (
          <Button
            disabled={pending}
            onClick={() => archive(!account.archived)}
            type="button"
            variant="ghost"
          >
            {account.archived ? (
              <ArchiveRestore aria-hidden="true" />
            ) : (
              <Archive aria-hidden="true" />
            )}
            {account.archived ? "Restore account" : "Archive account"}
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
            {pending ? "Saving…" : account ? "Save" : "Add account"}
          </Button>
        </div>
      </DialogFooter>
    </form>
  );
}
