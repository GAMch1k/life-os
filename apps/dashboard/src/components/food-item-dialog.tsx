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
  AISLES,
  type Aisle,
  type DraftErrors,
  NEW_STORE,
  type ShoppingDraft,
  type ShoppingItem,
  type Store,
  activeStores,
  load,
  shoppingDraft,
  shoppingItemPatch,
  upsert,
  writeHeader,
} from "@/lib/food";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";

/**
 * Edits a shopping item: its name, amount, note, aisle, and store (one of
 * the stores, or a new one typed by name), or deletes it.
 */
export function ShoppingItemDialog({
  item,
  stores,
  shopping,
  onClose,
  onStoresChanged,
}: {
  item: ShoppingItem | null;
  stores: Store[];
  shopping: SyncedList<ShoppingItem>;
  onClose: () => void;
  /** A store typed by name may have been created. */
  onStoresChanged: () => void;
}) {
  const current = useClosingState(item);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={item !== null}
    >
      <DialogContent>
        {current ? (
          <ItemForm
            item={current}
            key={`${current.id}-${current.updatedAt}`}
            onDone={onClose}
            onStoresChanged={onStoresChanged}
            shopping={shopping}
            stores={stores}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function ItemForm({
  item,
  stores,
  shopping,
  onDone,
  onStoresChanged,
}: {
  item: ShoppingItem;
  stores: Store[];
  shopping: SyncedList<ShoppingItem>;
  onDone: () => void;
  onStoresChanged: () => void;
}) {
  const id = useId();
  const [draft, setDraft] = useState<ShoppingDraft>(() => shoppingDraft(item));
  const [errors, setErrors] = useState<DraftErrors & { form?: string }>({});
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pending, setPending] = useState(false);
  const update = (patch: Partial<ShoppingDraft>) =>
    setDraft((value) => ({ ...value, ...patch }));
  // An archived store stays pickable for the item that has it.
  const choices = activeStores(stores);
  const archived = stores.find(
    (store) => store.id === item.storeId && store.archived,
  );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { body, errors: found } = shoppingItemPatch(item, draft);
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onDone();
    setPending(true);
    setErrors({});
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient.PATCH("/v1/shopping-items/{itemId}", {
            params: { path: { itemId: item.id }, header: writeHeader() },
            body,
          }),
        "Could not save this item.",
      );
      setPending(false);
      if (result.error !== undefined) return setErrors({ form: result.error });
      shopping.change((items) => upsert(items, result.data));
      if (body.storeName) onStoresChanged();
      onDone();
    });
  };

  const remove = () => {
    setPending(true);
    setErrors({});
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient
            .DELETE("/v1/shopping-items/{itemId}", {
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
      shopping.change((items) => items.filter((entry) => entry.id !== item.id));
      onDone();
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Edit item</DialogTitle>
        <DialogDescription>
          Changes show on the list right away.
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
            placeholder="2 l, 6, 500 g"
            value={draft.amount}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-aisle`}>Aisle</FieldLabel>
          <NativeSelect
            className="w-full [&>select]:h-10"
            id={`${id}-aisle`}
            onChange={(event) => update({ aisle: event.target.value as Aisle })}
            value={draft.aisle}
          >
            {AISLES.map((aisle) => (
              <NativeSelectOption key={aisle.value} value={aisle.value}>
                {aisle.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      </div>

      <Field>
        <FieldLabel htmlFor={`${id}-store`}>Store</FieldLabel>
        <NativeSelect
          className="w-full [&>select]:h-10"
          id={`${id}-store`}
          onChange={(event) => update({ store: event.target.value })}
          value={draft.store}
        >
          <NativeSelectOption value="">No store</NativeSelectOption>
          {choices.map((store) => (
            <NativeSelectOption key={store.id} value={store.id}>
              {store.name}
            </NativeSelectOption>
          ))}
          {archived ? (
            <NativeSelectOption value={archived.id}>
              {archived.name} (archived)
            </NativeSelectOption>
          ) : null}
          <NativeSelectOption value={NEW_STORE}>New store…</NativeSelectOption>
        </NativeSelect>
      </Field>
      {draft.store === NEW_STORE ? (
        <Field data-invalid={Boolean(errors.newStore)}>
          <FieldLabel htmlFor={`${id}-new-store`}>New store</FieldLabel>
          <Input
            aria-describedby={
              errors.newStore ? `${id}-new-store-error` : undefined
            }
            aria-invalid={Boolean(errors.newStore) || undefined}
            autoFocus
            className="h-10 text-base"
            id={`${id}-new-store`}
            maxLength={100}
            onChange={(event) => update({ newStore: event.target.value })}
            placeholder="Its name, such as Lidl"
            value={draft.newStore}
          />
          <FieldError id={`${id}-new-store-error`}>
            {errors.newStore}
          </FieldError>
        </Field>
      ) : null}

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

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter className="sm:justify-between">
        {confirmDelete ? (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <p className="text-sm font-medium">Delete it from the list?</p>
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
                {pending ? "Saving…" : "Save"}
              </Button>
            </div>
          </>
        )}
      </DialogFooter>
    </form>
  );
}
