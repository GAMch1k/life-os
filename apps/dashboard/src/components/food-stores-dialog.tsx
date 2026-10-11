"use client";

import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowUp,
  ChevronRight,
  Pencil,
} from "lucide-react";
import { type FormEvent, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { foodClient } from "@/lib/api/food-client";
import {
  type Loaded,
  type Store,
  activeStores,
  load,
  storeMoves,
  writeHeader,
} from "@/lib/food";
import { cn } from "@/lib/utils";

/**
 * The stores items can be tagged with: add, rename, reorder (the filter's
 * order), and archive them. Stores are never deleted, so items keep
 * theirs; an archived one can be restored.
 */
export function StoresDialog({
  open,
  stores,
  onClose,
  onChanged,
}: {
  open: boolean;
  stores: Store[];
  onClose: () => void;
  /** Stores changed: read them, and the list's store names, again. */
  onChanged: () => void;
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
          <DialogTitle>Stores</DialogTitle>
          <DialogDescription>
            Tag items with the store to buy them at, then filter the list by
            store in the shop.
          </DialogDescription>
        </DialogHeader>
        {open ? <StoresEditor onChanged={onChanged} stores={stores} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function StoresEditor({
  stores,
  onChanged,
}: {
  stores: Store[];
  onChanged: () => void;
}) {
  const [name, setName] = useState("");
  const [renaming, setRenaming] = useState<{ id: string; name: string }>();
  const [showArchived, setShowArchived] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const active = activeStores(stores);
  const archived = stores.filter((store) => store.archived);

  /** Runs the requests one after another; stops at the first refusal. */
  const run = async (
    requests: (() => Promise<Loaded<unknown>>)[],
    then?: () => void,
  ) => {
    setPending(true);
    setError(undefined);
    for (const request of requests) {
      const result = await request();
      if (result.error !== undefined) {
        setError(result.error);
        break;
      }
    }
    if (requests.length) onChanged();
    setPending(false);
    then?.();
  };

  const patch = (
    store: Store,
    body: { name?: string; sortOrder?: number; archived?: boolean },
  ) =>
    load(
      () =>
        foodClient.PATCH("/v1/stores/{storeId}", {
          params: { path: { storeId: store.id }, header: writeHeader() },
          body,
        }),
      `Could not change ${store.name}.`,
    );

  const add = (event: FormEvent) => {
    event.preventDefault();
    const typed = name.trim();
    if (!typed) return;
    void run(
      [
        () =>
          load(
            () =>
              foodClient.POST("/v1/stores", {
                params: { header: writeHeader() },
                body: { name: typed },
              }),
            `Could not add ${typed}.`,
          ),
      ],
      () => setName(""),
    );
  };

  const rename = (event: FormEvent) => {
    event.preventDefault();
    const store = active.find((entry) => entry.id === renaming?.id);
    const typed = renaming?.name.trim();
    if (!store || !typed) return;
    if (typed === store.name) return setRenaming(undefined);
    void run([() => patch(store, { name: typed })], () =>
      setRenaming(undefined),
    );
  };

  const move = (index: number, delta: -1 | 1) =>
    void run(
      storeMoves(active, index, delta).map((change) => () => {
        const store = active.find((entry) => entry.id === change.id)!;
        return patch(store, { sortOrder: change.sortOrder });
      }),
    );

  return (
    <div className="grid gap-4">
      {active.length ? (
        <ul className="divide-y rounded-lg border" data-testid="food-stores">
          {active.map((store, index) => (
            <li
              className="flex min-h-12 items-center gap-1 px-3 py-1.5"
              key={store.id}
            >
              {renaming?.id === store.id ? (
                <form className="flex flex-1 gap-2" onSubmit={rename}>
                  <Input
                    aria-label={`New name for ${store.name}`}
                    autoFocus
                    className="h-9 text-base"
                    maxLength={100}
                    onChange={(event) =>
                      setRenaming({ id: store.id, name: event.target.value })
                    }
                    value={renaming.name}
                  />
                  <Button disabled={pending} size="lg" type="submit">
                    Save
                  </Button>
                  <Button
                    disabled={pending}
                    onClick={() => setRenaming(undefined)}
                    size="lg"
                    type="button"
                    variant="ghost"
                  >
                    Cancel
                  </Button>
                </form>
              ) : (
                <>
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {store.name}
                  </span>
                  <Button
                    aria-label={`Move ${store.name} up`}
                    disabled={pending || index === 0}
                    onClick={() => move(index, -1)}
                    size="icon-lg"
                    variant="ghost"
                  >
                    <ArrowUp aria-hidden="true" />
                  </Button>
                  <Button
                    aria-label={`Move ${store.name} down`}
                    disabled={pending || index === active.length - 1}
                    onClick={() => move(index, 1)}
                    size="icon-lg"
                    variant="ghost"
                  >
                    <ArrowDown aria-hidden="true" />
                  </Button>
                  <Button
                    aria-label={`Rename ${store.name}`}
                    disabled={pending}
                    onClick={() =>
                      setRenaming({ id: store.id, name: store.name })
                    }
                    size="icon-lg"
                    variant="ghost"
                  >
                    <Pencil aria-hidden="true" />
                  </Button>
                  <Button
                    aria-label={`Archive ${store.name}`}
                    disabled={pending}
                    onClick={() =>
                      void run([() => patch(store, { archived: true })])
                    }
                    size="icon-lg"
                    variant="ghost"
                  >
                    <Archive aria-hidden="true" />
                  </Button>
                </>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground text-sm">No stores yet.</p>
      )}

      <form className="flex gap-2" onSubmit={add}>
        <Input
          aria-label="New store"
          className="h-10 text-base"
          maxLength={100}
          onChange={(event) => setName(event.target.value)}
          placeholder="Add a store, such as Lidl"
          value={name}
        />
        <Button disabled={pending || !name.trim()} size="lg" type="submit">
          Add store
        </Button>
      </form>

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      {archived.length ? (
        <div className="space-y-1.5">
          <Button
            aria-controls="food-archived-stores"
            aria-expanded={showArchived}
            className="-ml-2 gap-2 aria-expanded:bg-transparent"
            onClick={() => setShowArchived(!showArchived)}
            size="sm"
            variant="ghost"
          >
            <ChevronRight
              aria-hidden="true"
              className={cn(
                "transition-transform",
                showArchived && "rotate-90",
              )}
            />
            Archived ({archived.length})
          </Button>
          <ul
            className="divide-y rounded-lg border"
            hidden={!showArchived}
            id="food-archived-stores"
          >
            {archived.map((store) => (
              <li
                className="flex min-h-12 items-center gap-2 px-3 py-1.5"
                key={store.id}
              >
                <span className="text-muted-foreground min-w-0 flex-1 truncate">
                  {store.name}
                </span>
                <Button
                  disabled={pending}
                  onClick={() =>
                    void run([() => patch(store, { archived: false })])
                  }
                  size="sm"
                  variant="outline"
                >
                  <ArchiveRestore aria-hidden="true" />
                  Restore
                </Button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
