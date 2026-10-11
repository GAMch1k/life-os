"use client";

import { useEffect, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { foodClient } from "@/lib/api/food-client";
import { type ShoppingItem, load, upsert, writeHeader } from "@/lib/food";
import {
  type FromRecipesRequest,
  type FromRecipesResult,
  ingredientStatusLabel,
  missingCounts,
  missingNote,
} from "@/lib/food-recipes";
import type { SyncedList } from "@/lib/use-synced-list";
import { useClosingState } from "@/lib/use-closing-state";
import { cn } from "@/lib/utils";

/** What the dialog adds: the ingredients of some recipes, or of a week. */
export type MissingRequest = {
  /** The dialog's subject, such as "Pasta Bolognese" or "this week". */
  label: string;
  body: Omit<FromRecipesRequest, "dryRun">;
};

/**
 * Adds the missing ingredients of recipes to the shopping list: first a
 * preview (Food's dry run) of what would be added and what is skipped
 * because it is at home or already on the list, then one confirmation.
 */
export function MissingIngredientsDialog({
  request,
  shopping,
  onClose,
  onDone,
}: {
  request: MissingRequest | null;
  shopping: SyncedList<ShoppingItem>;
  onClose: () => void;
  /** The line to show afterwards. */
  onDone: (note: string) => void;
}) {
  const current = useClosingState(request);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={request !== null}
    >
      <DialogContent>
        {current ? (
          <Preview
            key={JSON.stringify(current.body)}
            onCancel={onClose}
            onDone={(note) => {
              onDone(note);
              onClose();
            }}
            request={current}
            shopping={shopping}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

const TONES = {
  added: "default",
  merged: "default",
  in_pantry: "secondary",
  on_list: "secondary",
} as const;

function Preview({
  request,
  shopping,
  onCancel,
  onDone,
}: {
  request: MissingRequest;
  shopping: SyncedList<ShoppingItem>;
  onCancel: () => void;
  onDone: (note: string) => void;
}) {
  const [preview, setPreview] = useState<FromRecipesResult>();
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);

  useEffect(() => {
    let live = true;
    void load(
      () =>
        // A dry run changes nothing, so it needs no Idempotency-Key.
        foodClient.POST("/v1/shopping-items/from-recipes", {
          body: { ...request.body, dryRun: true },
        }),
      "The ingredients could not be checked.",
    ).then((result) => {
      if (!live) return;
      if (result.error !== undefined) setError(result.error);
      else setPreview(result.data);
    });
    return () => {
      live = false;
    };
  }, [request]);

  const counts = preview ? missingCounts(preview.items) : undefined;

  const confirm = () => {
    setPending(true);
    setError(undefined);
    void shopping.write(async () => {
      const result = await load(
        () =>
          foodClient.POST("/v1/shopping-items/from-recipes", {
            params: { header: writeHeader() },
            body: { ...request.body, dryRun: false },
          }),
        "The ingredients could not be added.",
      );
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      const added = result.data.items.flatMap((item) =>
        (item.status === "added" || item.status === "merged") &&
        item.shoppingItem
          ? [item.shoppingItem]
          : [],
      );
      shopping.change((items) =>
        added.reduce((list, item) => upsert(list, item), items),
      );
      onDone(missingNote(result.data));
    });
  };

  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Add missing ingredients</DialogTitle>
        <DialogDescription>
          For {request.label}. What is at home or already on the list is
          skipped.
        </DialogDescription>
      </DialogHeader>

      {!preview && !error ? (
        <div className="space-y-2" aria-busy="true">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : null}
      {preview && !preview.items.length ? (
        <p className="text-muted-foreground text-sm">
          No ingredients to add: the recipes list none.
        </p>
      ) : null}
      {preview?.items.length ? (
        <ul
          className="divide-y rounded-lg border"
          data-testid="food-missing-preview"
        >
          {preview.items.map((item) => (
            <li
              className="flex min-h-11 items-center gap-2 px-3 py-2"
              data-status={item.status}
              data-testid="food-missing-item"
              key={item.normalizedName}
            >
              <span className="min-w-0 flex-1">
                <span
                  className={cn(
                    "wrap-anywhere",
                    item.status === "added" ? "font-medium" : "",
                  )}
                >
                  {item.name}
                </span>
                {item.amount ? (
                  <span className="text-muted-foreground ml-1.5 text-sm">
                    {item.amount}
                  </span>
                ) : null}
                {item.recipes.length > 1 ? (
                  <span className="text-muted-foreground block text-xs">
                    {item.recipes.map((recipe) => recipe.title).join(", ")}
                  </span>
                ) : null}
              </span>
              <Badge variant={TONES[item.status]}>
                {ingredientStatusLabel(item.status, true)}
              </Badge>
            </li>
          ))}
        </ul>
      ) : null}

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
        <Button
          disabled={pending || !counts?.toAdd}
          onClick={confirm}
          size="lg"
          type="button"
        >
          {pending
            ? "Adding…"
            : counts?.toAdd
              ? `Add ${counts.toAdd} to the list`
              : "Nothing to add"}
        </Button>
      </DialogFooter>
    </div>
  );
}
