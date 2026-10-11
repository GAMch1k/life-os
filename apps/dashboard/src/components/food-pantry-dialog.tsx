"use client";

import { Trash2 } from "lucide-react";
import { type FormEvent, useId, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { DatePicker } from "@/components/date-picker";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { foodClient } from "@/lib/api/food-client";
import {
  type DraftErrors,
  LOCATIONS,
  type PantryDraft,
  type PantryItem,
  load,
  newPantryItem,
  pantryDraft,
  pantryItemPatch,
  upsert,
  writeHeader,
} from "@/lib/food";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";

export type PantryDialogState =
  { mode: "new" } | { mode: "edit"; item: PantryItem } | null;

/**
 * Adds a pantry item (name, amount, place, expiry date; an empty place lets
 * Food choose it from the name) or edits one, with its opened date and
 * note too, or deletes it with its history.
 */
export function PantryItemDialog({
  state,
  pantry,
  today,
  onClose,
}: {
  state: PantryDialogState;
  pantry: SyncedList<PantryItem>;
  today: string;
  onClose: () => void;
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
          <PantryForm
            item={current.mode === "edit" ? current.item : undefined}
            key={
              current.mode === "edit"
                ? `${current.item.id}-${current.item.updatedAt}`
                : "new"
            }
            onDone={onClose}
            pantry={pantry}
            today={today}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function PantryForm({
  item,
  pantry,
  today,
  onDone,
}: {
  item?: PantryItem;
  pantry: SyncedList<PantryItem>;
  today: string;
  onDone: () => void;
}) {
  const id = useId();
  const [draft, setDraft] = useState<PantryDraft>(() => pantryDraft(item));
  const [errors, setErrors] = useState<DraftErrors & { form?: string }>({});
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pending, setPending] = useState(false);
  const update = (patch: Partial<PantryDraft>) =>
    setDraft((value) => ({ ...value, ...patch }));

  /** Sends one write; on success applies its item and closes. */
  const send = (
    request: () => Promise<{ data?: PantryItem; error?: unknown }>,
    fallback: string,
  ) => {
    setPending(true);
    setErrors({});
    void pantry.write(async () => {
      const result = await load(request, fallback);
      setPending(false);
      if (result.error !== undefined) return setErrors({ form: result.error });
      pantry.change((items) => upsert(items, result.data));
      onDone();
    });
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!item) {
      const { body, errors: found } = newPantryItem(draft);
      if (!body) return setErrors(found);
      return send(
        () =>
          foodClient
            .POST("/v1/pantry-items", {
              params: { header: writeHeader() },
              body: { items: [body] },
            })
            .then(({ data, error }) => ({ data: data?.items[0], error })),
        "Could not add this item.",
      );
    }
    const { body, errors: found } = pantryItemPatch(item, draft);
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onDone();
    send(
      () =>
        foodClient.PATCH("/v1/pantry-items/{itemId}", {
          params: { path: { itemId: item.id }, header: writeHeader() },
          body,
        }),
      "Could not save this item.",
    );
  };

  const remove = () => {
    if (!item) return;
    setPending(true);
    setErrors({});
    void pantry.write(async () => {
      const result = await load(
        () =>
          foodClient
            .DELETE("/v1/pantry-items/{itemId}", {
              params: { path: { itemId: item.id }, header: writeHeader() },
            })
            .then(({ response, error }) => ({
              data: response.ok ? true : undefined,
              error,
            })),
        "Could not delete this item.",
      );
      setPending(false);
      if (result.error !== undefined) {
        setConfirmDelete(false);
        return setErrors({ form: result.error });
      }
      pantry.change((items) => items.filter((entry) => entry.id !== item.id));
      onDone();
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {item ? "Edit pantry item" : "Add to the pantry"}
        </DialogTitle>
        <DialogDescription>
          {item
            ? "Changes show in the pantry right away."
            : "Leave the place empty and Food picks it from the name: dairy goes in the fridge, frozen food in the freezer."}
        </DialogDescription>
      </DialogHeader>

      <Field data-invalid={Boolean(errors.name)}>
        <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
        <Input
          aria-describedby={errors.name ? `${id}-name-error` : undefined}
          aria-invalid={Boolean(errors.name) || undefined}
          className="h-10 text-base"
          id={`${id}-name`}
          maxLength={200}
          onChange={(event) => update({ name: event.target.value })}
          value={draft.name}
        />
        <FieldError id={`${id}-name-error`}>{errors.name}</FieldError>
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-amount`}>Amount</FieldLabel>
          <Input
            className="h-10 text-base"
            id={`${id}-amount`}
            maxLength={50}
            onChange={(event) => update({ amount: event.target.value })}
            placeholder="1 l, 6, 500 g"
            value={draft.amount}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-location`}>Place</FieldLabel>
          <NativeSelect
            className="w-full [&>select]:h-10"
            id={`${id}-location`}
            onChange={(event) =>
              update({
                location: event.target.value as PantryDraft["location"],
              })
            }
            value={draft.location}
          >
            {item ? null : (
              <NativeSelectOption value="">Choose for me</NativeSelectOption>
            )}
            {LOCATIONS.map((location) => (
              <NativeSelectOption key={location.value} value={location.value}>
                {location.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-expires`}>Expires on</FieldLabel>
          <DatePicker
            className="h-10 w-full"
            id={`${id}-expires`}
            label="Expires on"
            onChange={(expiresOn) => update({ expiresOn })}
            placeholder="No date"
            today={today}
            value={draft.expiresOn}
          />
        </Field>
        {item ? (
          <Field>
            <FieldLabel htmlFor={`${id}-opened`}>Opened on</FieldLabel>
            <DatePicker
              className="h-10 w-full"
              id={`${id}-opened`}
              label="Opened on"
              onChange={(openedOn) => update({ openedOn })}
              placeholder="Not opened"
              today={today}
              value={draft.openedOn}
            />
          </Field>
        ) : null}
      </div>

      {item ? (
        <Field>
          <FieldLabel htmlFor={`${id}-note`}>Note</FieldLabel>
          <Textarea
            className="text-base"
            id={`${id}-note`}
            maxLength={1000}
            onChange={(event) => update({ note: event.target.value })}
            rows={2}
            value={draft.note}
          />
        </Field>
      ) : null}

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter className={item ? "sm:justify-between" : undefined}>
        {confirmDelete ? (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <p className="text-sm font-medium">
              Delete it and its history? To keep the history, mark it used up or
              thrown away.
            </p>
            <div className="flex gap-2">
              <Button
                disabled={pending}
                onClick={remove}
                size="lg"
                type="button"
                variant="destructive"
              >
                {pending ? "Deleting…" : "Delete"}
              </Button>
              <Button
                disabled={pending}
                onClick={() => setConfirmDelete(false)}
                size="lg"
                type="button"
                variant="outline"
              >
                Keep
              </Button>
            </div>
          </div>
        ) : (
          <>
            {item ? (
              <Button
                disabled={pending}
                onClick={() => setConfirmDelete(true)}
                size="lg"
                type="button"
                variant="ghost"
              >
                <Trash2 aria-hidden="true" />
                Delete
              </Button>
            ) : null}
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              <Button
                disabled={pending}
                onClick={onDone}
                size="lg"
                type="button"
                variant="outline"
              >
                Cancel
              </Button>
              <Button disabled={pending} size="lg" type="submit">
                {pending ? "Saving…" : item ? "Save" : "Add item"}
              </Button>
            </div>
          </>
        )}
      </DialogFooter>
    </form>
  );
}
