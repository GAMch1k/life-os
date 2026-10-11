"use client";

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
import {
  Field,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { foodClient } from "@/lib/api/food-client";
import {
  LOCATIONS,
  type PantryItem,
  type PantryLocation,
  type PutAwayDraft,
  type ShoppingItem,
  load,
  putAwayDrafts,
  putAwayItems,
  upsert,
  writeHeader,
} from "@/lib/food";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";

/**
 * Puts bought items away: each with its place at home (the usual place of
 * its aisle to start with) and an optional expiry date, then one Put away
 * moves them all into the pantry and off the list.
 */
export function PutAwayDialog({
  items,
  shopping,
  pantry,
  today,
  onClose,
  onDone,
}: {
  items: ShoppingItem[] | null;
  shopping: SyncedList<ShoppingItem>;
  pantry: SyncedList<PantryItem>;
  today: string;
  onClose: () => void;
  /** They are in the pantry now. */
  onDone: (added: PantryItem[]) => void;
}) {
  const current = useClosingState(items);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={items !== null}
    >
      <DialogContent>
        {current ? (
          <PutAwayForm
            items={current}
            key={current.map((item) => item.id).join()}
            onCancel={onClose}
            onDone={(added) => {
              onDone(added);
              onClose();
            }}
            pantry={pantry}
            shopping={shopping}
            today={today}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function PutAwayForm({
  items,
  shopping,
  pantry,
  today,
  onCancel,
  onDone,
}: {
  items: ShoppingItem[];
  shopping: SyncedList<ShoppingItem>;
  pantry: SyncedList<PantryItem>;
  today: string;
  onCancel: () => void;
  onDone: (added: PantryItem[]) => void;
}) {
  const id = useId();
  const [drafts, setDrafts] = useState(() => putAwayDrafts(items));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const update = (index: number, patch: Partial<PutAwayDraft>) =>
    setDrafts((current) =>
      current.map((draft, at) =>
        at === index ? { ...draft, ...patch } : draft,
      ),
    );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    setError(undefined);
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/shopping-items/put-away", {
            params: { header: writeHeader() },
            body: { items: putAwayItems(drafts) },
          }),
        items.length === 1
          ? `Could not put “${items[0].name}” away.`
          : "Could not put these items away.",
      );
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      const gone = new Set(drafts.map((draft) => draft.id));
      shopping.change((current) =>
        current.filter((item) => !gone.has(item.id)),
      );
      pantry.change((current) =>
        result.data.items.reduce((list, item) => upsert(list, item), current),
      );
      pantry.reload();
      onDone(result.data.items);
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Put away</DialogTitle>
        <DialogDescription>
          {items.length === 1
            ? "It moves to the pantry and leaves the list."
            : `These ${items.length} items move to the pantry and leave the list.`}
        </DialogDescription>
      </DialogHeader>

      {drafts.map((draft, index) => (
        <FieldSet
          className="gap-2 rounded-lg border p-3"
          data-testid="food-put-away-item"
          key={draft.id}
        >
          <FieldLegend className="mb-0 text-base font-medium" variant="label">
            {draft.name}
            {draft.amount ? (
              <span className="text-muted-foreground ml-1.5 text-sm font-normal">
                {draft.amount}
              </span>
            ) : null}
          </FieldLegend>
          <div className="grid grid-cols-2 gap-2">
            <Field className="gap-1">
              <FieldLabel htmlFor={`${id}-${index}-place`}>Place</FieldLabel>
              <NativeSelect
                className="w-full [&>select]:h-10"
                id={`${id}-${index}-place`}
                onChange={(event) =>
                  update(index, {
                    location: event.target.value as PantryLocation,
                  })
                }
                value={draft.location}
              >
                {LOCATIONS.map((location) => (
                  <NativeSelectOption
                    key={location.value}
                    value={location.value}
                  >
                    {location.label}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </Field>
            <Field className="gap-1">
              <FieldLabel htmlFor={`${id}-${index}-expires`}>
                Expires on
              </FieldLabel>
              <DatePicker
                className="h-10 w-full"
                id={`${id}-${index}-expires`}
                label="Expires on"
                onChange={(expiresOn) => update(index, { expiresOn })}
                placeholder="No date"
                today={today}
                value={draft.expiresOn}
              />
            </Field>
          </div>
        </FieldSet>
      ))}

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      <DialogFooter>
        <Button
          disabled={pending}
          onClick={onCancel}
          size="lg"
          type="button"
          variant="outline"
        >
          Cancel
        </Button>
        <Button disabled={pending} size="lg" type="submit">
          {pending ? "Putting away…" : "Put away"}
        </Button>
      </DialogFooter>
    </form>
  );
}
