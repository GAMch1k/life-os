"use client";

import { Trash2 } from "lucide-react";
import { type FormEvent, useId, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { foodClient } from "@/lib/api/food-client";
import {
  type PantryItem,
  type ShoppingItem,
  type Staple,
  type Store,
  activeStores,
  load,
  restockSentence,
  stapleDetails,
  upsert,
  writeHeader,
} from "@/lib/food";
import type { SyncedList } from "@/lib/use-synced-list";
import { cn } from "@/lib/utils";

/**
 * The staples: what the owner always wants at home. When one runs low, is
 * used up, or is thrown away, Food puts it on the shopping list by itself,
 * with the amount and store given here. A staple can be marked running low
 * from here too, for things the pantry does not track.
 */
export function StaplesDialog({
  open,
  staples,
  shopping,
  pantry,
  stores,
  onClose,
}: {
  open: boolean;
  staples: SyncedList<Staple>;
  shopping: SyncedList<ShoppingItem>;
  pantry: SyncedList<PantryItem>;
  stores: Store[];
  onClose: () => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Staples</DialogTitle>
          <DialogDescription>
            Things you always want at home. When one runs low, is used up, or is
            thrown away, it goes on the shopping list by itself.
          </DialogDescription>
        </DialogHeader>
        {open ? (
          <StaplesEditor
            pantry={pantry}
            shopping={shopping}
            staples={staples}
            stores={stores}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function StaplesEditor({
  staples,
  shopping,
  pantry,
  stores,
}: {
  staples: SyncedList<Staple>;
  shopping: SyncedList<ShoppingItem>;
  pantry: SyncedList<PantryItem>;
  stores: Store[];
}) {
  const id = useId();
  const [name, setName] = useState("");
  const [amount, setAmount] = useState("");
  const [store, setStore] = useState("");
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState<{
    kind: "info" | "error";
    text: string;
  }>();
  const choices = activeStores(stores);

  const add = (event: FormEvent) => {
    event.preventDefault();
    const typed = name.trim();
    if (!typed) return;
    setPending(true);
    setNotice(undefined);
    void staples.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/staples", {
            params: { header: writeHeader() },
            body: {
              name: typed,
              ...(amount.trim() ? { amount: amount.trim() } : {}),
              ...(store ? { storeId: store } : {}),
            },
          }),
        `Could not add “${typed}” to the staples.`,
      );
      setPending(false);
      if (result.error !== undefined)
        return setNotice({ kind: "error", text: result.error });
      staples.change((items) => upsert(items, result.data));
      setName("");
      setAmount("");
      setStore("");
      setNotice({
        kind: "info",
        text: `“${result.data.name}” is now a staple.`,
      });
    });
  };

  const remove = (staple: Staple) => {
    setPending(true);
    setNotice(undefined);
    void staples.write(async () => {
      const result = await load(
        () =>
          foodClient
            .DELETE("/v1/staples/{stapleId}", {
              params: { path: { stapleId: staple.id }, header: writeHeader() },
            })
            .then(({ response, error }) => ({
              data: response.ok ? true : undefined,
              error,
            })),
        `Could not remove “${staple.name}” from the staples.`,
      );
      setPending(false);
      if (result.error !== undefined)
        return setNotice({ kind: "error", text: result.error });
      staples.change((items) =>
        items.filter((entry) => entry.id !== staple.id),
      );
      setNotice({
        kind: "info",
        text: `“${staple.name}” is no longer a staple.`,
      });
    });
  };

  /** Marks a staple running low: Food puts it on the list. */
  const runningLow = (staple: Staple) => {
    setPending(true);
    setNotice(undefined);
    void staples.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/staples/running-low", {
            params: { header: writeHeader() },
            body: { name: staple.name, addToList: false },
          }),
        `Could not mark “${staple.name}” as running low.`,
      );
      setPending(false);
      if (result.error !== undefined)
        return setNotice({ kind: "error", text: result.error });
      const { restock, pantryItems } = result.data;
      if (restock)
        shopping.change((items) => upsert(items, restock.shoppingItem));
      pantry.change((items) =>
        pantryItems.reduce((list, item) => upsert(list, item), items),
      );
      if (result.data.staple) {
        const updated = result.data.staple;
        staples.change((items) => upsert(items, updated));
      }
      setNotice({
        kind: "info",
        text: restock
          ? restockSentence(restock)
          : `“${staple.name}” is running low.`,
      });
    });
  };

  return (
    <div className="grid gap-4">
      {staples.items.length ? (
        <ul className="divide-y rounded-lg border" data-testid="food-staples">
          {staples.items.map((staple) => (
            <li
              className="flex min-h-12 items-center gap-1 px-3 py-1.5"
              data-testid="food-staple"
              key={staple.id}
            >
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">
                  {staple.name}
                </span>
                <span className="text-muted-foreground block truncate text-xs">
                  {stapleDetails(staple)}
                </span>
              </span>
              <Button
                aria-label={`Running low: ${staple.name}`}
                disabled={pending || staple.onList}
                onClick={() => runningLow(staple)}
                size="sm"
                variant="outline"
              >
                Running low
              </Button>
              <Button
                aria-label={`Remove staple ${staple.name}`}
                disabled={pending}
                onClick={() => remove(staple)}
                size="icon-lg"
                variant="ghost"
              >
                <Trash2 aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground text-sm">
          No staples yet. Star a pantry item, or add one here.
        </p>
      )}

      <form className="grid gap-3" noValidate onSubmit={add}>
        <Field>
          <FieldLabel htmlFor={`${id}-name`}>New staple</FieldLabel>
          <Input
            className="h-10 text-base"
            id={`${id}-name`}
            maxLength={200}
            onChange={(event) => setName(event.target.value)}
            placeholder="Milch, Eier, Kaffee"
            value={name}
          />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${id}-amount`}>Amount to buy</FieldLabel>
            <Input
              className="h-10 text-base"
              id={`${id}-amount`}
              maxLength={50}
              onChange={(event) => setAmount(event.target.value)}
              placeholder="2 l, 6, 500 g"
              value={amount}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-store`}>Store</FieldLabel>
            <NativeSelect
              className="w-full [&>select]:h-10"
              id={`${id}-store`}
              onChange={(event) => setStore(event.target.value)}
              value={store}
            >
              <NativeSelectOption value="">No store</NativeSelectOption>
              {choices.map((entry) => (
                <NativeSelectOption key={entry.id} value={entry.id}>
                  {entry.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </Field>
        </div>
        <Button
          className="justify-self-start"
          disabled={pending || !name.trim()}
          size="lg"
          type="submit"
        >
          Add staple
        </Button>
      </form>

      <p
        aria-live="polite"
        className={cn(
          "text-sm",
          notice?.kind === "error"
            ? "text-destructive"
            : "text-muted-foreground",
          !notice && "sr-only",
        )}
        data-testid="food-staples-note"
        role="status"
      >
        {notice?.text}
      </p>
    </div>
  );
}
