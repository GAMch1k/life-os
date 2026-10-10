"use client";

import { Repeat } from "lucide-react";
import { useId, useState, useTransition } from "react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { financeClient } from "@/lib/api/finance-client";
import {
  CADENCES,
  type Subscription,
  type SubscriptionCadence,
  cadenceLabel,
  subscriptionOf,
} from "@/lib/finance-subscriptions";
import { type Transaction, financeErrorMessage } from "@/lib/finance-workspace";

/**
 * "This is a subscription", in a money-out transaction's dialog: picks how
 * often it repeats and makes it a confirmed subscription (later payments to
 * the counterparty with a similar amount keep it up to date). A transaction
 * that already belongs to one says so instead.
 */
export function SubscriptionAction({
  transaction,
  subscriptions,
  onSaved,
}: {
  transaction: Transaction;
  subscriptions: Subscription[];
  onSaved: (subscription: Subscription) => void;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [cadence, setCadence] = useState<SubscriptionCadence>("monthly");
  const [added, setAdded] = useState<Subscription>();
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const existing = subscriptionOf(subscriptions, transaction.id);

  const add = () =>
    startTransition(async () => {
      setError(undefined);
      const { data, error: failure } = await financeClient
        .POST("/v1/subscriptions", {
          body: { transactionId: transaction.id, cadence },
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        setError(
          financeErrorMessage(failure, "Could not add this subscription."),
        );
        return;
      }
      setAdded(data);
      setOpen(false);
      onSaved(data);
    });

  if (added) {
    return (
      <p className="text-sm" role="status">
        Added “{added.name}” to your subscriptions (
        {cadenceLabel(added.cadence).toLowerCase()}).
      </p>
    );
  }
  if (existing) {
    return (
      <p className="text-muted-foreground flex items-center gap-1.5 text-sm">
        <Repeat aria-hidden="true" className="size-3.5" />
        {existing.state === "confirmed"
          ? `Part of the subscription “${existing.name}” (${cadenceLabel(existing.cadence).toLowerCase()}).`
          : `Finance suggests this is “${existing.name}” (${cadenceLabel(existing.cadence).toLowerCase()}); confirm it under Subscriptions.`}
      </p>
    );
  }
  return (
    <div className="grid gap-2" data-testid="subscription-action">
      {open ? (
        <div className="flex flex-wrap items-end gap-2">
          <div className="grid gap-1.5">
            <Label htmlFor={`${id}-cadence`}>Repeats</Label>
            <NativeSelect
              id={`${id}-cadence`}
              onChange={(event) =>
                setCadence(event.target.value as SubscriptionCadence)
              }
              value={cadence}
            >
              {CADENCES.map((item) => (
                <NativeSelectOption key={item.value} value={item.value}>
                  {item.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
          <Button disabled={pending} onClick={add} size="sm" type="button">
            {pending ? "Adding…" : "Add subscription"}
          </Button>
          <Button
            disabled={pending}
            onClick={() => setOpen(false)}
            size="sm"
            type="button"
            variant="ghost"
          >
            Cancel
          </Button>
        </div>
      ) : (
        <Button
          className="w-fit"
          onClick={() => setOpen(true)}
          size="sm"
          type="button"
          variant="outline"
        >
          <Repeat aria-hidden="true" />
          This is a subscription
        </Button>
      )}
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  );
}
