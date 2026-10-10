"use client";

import { Trash2 } from "lucide-react";
import { type FormEvent, useId, useState, useTransition } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  NativeSelect,
  NativeSelectOptGroup,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { financeClient } from "@/lib/api/finance-client";
import {
  CADENCES,
  type Subscription,
  type SubscriptionDraft,
  type SubscriptionErrors,
  subscriptionDraft,
  updateSubscriptionBody,
} from "@/lib/finance-subscriptions";
import {
  type Category,
  categoryGroups,
  financeErrorMessage,
} from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { useClosingState } from "@/lib/use-closing-state";

/**
 * Edits a subscription: name, how often it repeats, the typical amount, and
 * the category; or deletes it. What the owner sets here stays: detection
 * keeps following the payments but no longer changes it.
 */
export function SubscriptionDialog({
  subscription,
  categories,
  onClose,
  onSaved,
  onDeleted,
}: {
  subscription: Subscription | null;
  categories: Category[];
  onClose: () => void;
  onSaved: (subscription: Subscription) => void;
  onDeleted: (subscription: Subscription) => void;
}) {
  const current = useClosingState(subscription);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={subscription !== null}
    >
      <DialogContent className="sm:max-w-md">
        {current ? (
          <SubscriptionForm
            categories={categories}
            key={`${current.id}-${current.updatedAt}`}
            onCancel={onClose}
            onDeleted={(deleted) => {
              onDeleted(deleted);
              onClose();
            }}
            onSaved={(saved) => {
              onSaved(saved);
              onClose();
            }}
            subscription={current}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function SubscriptionForm({
  subscription,
  categories,
  onCancel,
  onSaved,
  onDeleted,
}: {
  subscription: Subscription;
  categories: Category[];
  onCancel: () => void;
  onSaved: (subscription: Subscription) => void;
  onDeleted: (subscription: Subscription) => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const [draft, setDraft] = useState<SubscriptionDraft>(() =>
    subscriptionDraft(subscription, locale),
  );
  const [errors, setErrors] = useState<SubscriptionErrors & { form?: string }>(
    {},
  );
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pending, startTransition] = useTransition();
  const update = (patch: Partial<SubscriptionDraft>) =>
    setDraft((value) => ({ ...value, ...patch }));
  const invalid = (field: keyof SubscriptionErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { body, errors: found } = updateSubscriptionBody(subscription, draft);
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onCancel();
    startTransition(async () => {
      setErrors({});
      const { data, error } = await financeClient
        .PATCH("/v1/subscriptions/{subscriptionId}", {
          params: { path: { subscriptionId: subscription.id } },
          body,
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(error, "Could not save this subscription."),
        });
      }
      onSaved(data);
    });
  };

  const remove = () =>
    startTransition(async () => {
      setErrors({});
      const { response, error } = await financeClient
        .DELETE("/v1/subscriptions/{subscriptionId}", {
          params: { path: { subscriptionId: subscription.id } },
        })
        .catch(() => ({ response: undefined, error: undefined }));
      if (!response?.ok) {
        setConfirmDelete(false);
        return setErrors({
          form: financeErrorMessage(
            error,
            "Could not delete this subscription.",
          ),
        });
      }
      onDeleted(subscription);
    });

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Edit subscription</DialogTitle>
        <DialogDescription>
          What you set here stays: Finance keeps following the payments but
          won&apos;t change it.
        </DialogDescription>
      </DialogHeader>

      <Field data-invalid={Boolean(errors.name)}>
        <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
        <Input
          id={`${id}-name`}
          maxLength={100}
          onChange={(event) => update({ name: event.target.value })}
          value={draft.name}
          {...invalid("name")}
        />
        <FieldError id={`${id}-name-error`}>{errors.name}</FieldError>
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-cadence`}>Repeats</FieldLabel>
          <NativeSelect
            className="w-full"
            id={`${id}-cadence`}
            onChange={(event) =>
              update({
                cadence: event.target.value as SubscriptionDraft["cadence"],
              })
            }
            value={draft.cadence}
          >
            {CADENCES.map((cadence) => (
              <NativeSelectOption key={cadence.value} value={cadence.value}>
                {cadence.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
        <Field data-invalid={Boolean(errors.amount)}>
          <FieldLabel htmlFor={`${id}-amount`}>Amount</FieldLabel>
          <div className="relative">
            <Input
              className="pr-12"
              id={`${id}-amount`}
              inputMode="decimal"
              onChange={(event) => update({ amount: event.target.value })}
              value={draft.amount}
              {...invalid("amount")}
            />
            <span className="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs">
              {subscription.amount.currency}
            </span>
          </div>
          <FieldError id={`${id}-amount-error`}>{errors.amount}</FieldError>
        </Field>
      </div>

      <Field>
        <FieldLabel htmlFor={`${id}-category`}>Category</FieldLabel>
        <NativeSelect
          className="w-full"
          id={`${id}-category`}
          onChange={(event) => update({ categoryId: event.target.value })}
          value={draft.categoryId}
        >
          <NativeSelectOption value="">No category</NativeSelectOption>
          {categoryGroups(categories, subscription.categoryId).map((group) => (
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
      </Field>

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter className="sm:justify-between">
        {confirmDelete ? (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <p className="text-sm font-medium">
              Delete it? Finance may suggest it again; ignore it to keep it
              away.
            </p>
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
            <Button
              disabled={pending}
              onClick={() => setConfirmDelete(true)}
              type="button"
              variant="ghost"
            >
              <Trash2 aria-hidden="true" />
              Delete subscription
            </Button>
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
                {pending ? "Saving…" : "Save"}
              </Button>
            </div>
          </>
        )}
      </DialogFooter>
    </form>
  );
}
