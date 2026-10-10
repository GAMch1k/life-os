"use client";

import { Check, EyeOff, Pencil, Repeat } from "lucide-react";
import { useState } from "react";

import { Amount } from "@/components/finance-amount";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  type Subscription,
  type SubscriptionList,
  cadenceLabel,
  evidenceText,
  monthlyText,
  pricePerCadence,
  renewalText,
  subscriptionGroups,
  totalNote,
} from "@/lib/finance-subscriptions";
import { type Category, type Loaded, byId } from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { cn } from "@/lib/utils";

/**
 * The Subscriptions tab: the monthly total of the confirmed subscriptions,
 * recurring payments Finance found to confirm or ignore, the confirmed ones
 * with their next payment, the ones whose payment is more than a week
 * late, and the ignored ones. A subscription opens in a dialog to edit.
 */
export function FinanceSubscriptions({
  categories,
  list,
  loading,
  pending,
  actionError,
  today,
  onConfirm,
  onIgnore,
  onEdit,
}: {
  categories: Category[];
  list: Loaded<SubscriptionList>;
  loading: boolean;
  /** A confirm or ignore is being saved. */
  pending: boolean;
  /** Why the last confirm or ignore failed, if it did. */
  actionError?: string;
  /** The owner's today (YYYY-MM-DD). */
  today: string;
  onConfirm: (subscription: Subscription) => void;
  onIgnore: (subscription: Subscription) => void;
  onEdit: (subscription: Subscription) => void;
}) {
  const [showIgnored, setShowIgnored] = useState(false);
  if (list.error !== undefined) {
    return (
      <p className="text-destructive text-sm" role="alert">
        {list.error}
      </p>
    );
  }
  const data = list.data;
  const groups = subscriptionGroups(data.items);
  const categoriesById = byId(categories);
  const row = (subscription: Subscription) => (
    <SubscriptionRow
      category={
        subscription.categoryId
          ? categoriesById.get(subscription.categoryId)
          : undefined
      }
      key={subscription.id}
      onConfirm={onConfirm}
      onEdit={onEdit}
      onIgnore={onIgnore}
      pending={pending}
      subscription={subscription}
      today={today}
    />
  );
  return (
    <section
      aria-busy={loading}
      aria-labelledby="finance-subscriptions-title"
      className={cn("grid gap-3 transition-opacity", loading && "opacity-60")}
      data-testid="finance-subscriptions"
    >
      <h2 className="sr-only" id="finance-subscriptions-title">
        Subscriptions
      </h2>
      <Card size="sm">
        <CardContent className="grid gap-1">
          <p className="text-muted-foreground text-xs font-medium">
            Subscriptions per month
          </p>
          <Amount
            className="block text-lg font-semibold tracking-tight sm:text-xl"
            money={data.totals.monthly}
          />
          <p
            className="text-muted-foreground text-xs"
            data-testid="finance-subscriptions-total"
          >
            {totalNote(data)}
          </p>
        </CardContent>
      </Card>

      {actionError ? (
        <p className="text-destructive text-sm" role="alert">
          {actionError}
        </p>
      ) : null}

      {!data.items.length ? (
        <Card size="sm">
          <CardHeader>
            <CardTitle>No subscriptions yet</CardTitle>
            <CardDescription>
              Finance looks for payments that repeat (the same counterparty at a
              regular interval with a similar amount) after every import and
              bank sync, and suggests them here. You can also open a transaction
              and choose <strong>This is a subscription</strong>.
            </CardDescription>
          </CardHeader>
        </Card>
      ) : null}

      {groups.suggested.length ? (
        <Section
          description="Recurring payments Finance found. Confirm the ones you pay on purpose; ignored ones are not suggested again."
          testId="finance-subscriptions-suggested"
          title="Suggested"
        >
          {groups.suggested.map(row)}
        </Section>
      ) : null}

      {groups.confirmed.length ? (
        <Section testId="finance-subscriptions-confirmed" title="Confirmed">
          {groups.confirmed.map(row)}
        </Section>
      ) : null}

      {groups.missed.length ? (
        <Section
          description="No payment for more than a week after the expected date. Cancelled? Ignore it; a new price? Edit its amount."
          testId="finance-subscriptions-missed"
          title="Missed"
        >
          {groups.missed.map(row)}
        </Section>
      ) : null}

      {groups.ignored.length ? (
        <div className="grid gap-2">
          <Button
            aria-expanded={showIgnored}
            className="w-fit"
            onClick={() => setShowIgnored(!showIgnored)}
            size="sm"
            variant="ghost"
          >
            {showIgnored
              ? "Hide ignored"
              : `Ignored (${groups.ignored.length})`}
          </Button>
          {showIgnored ? (
            <Section testId="finance-subscriptions-ignored" title="Ignored">
              {groups.ignored.map(row)}
            </Section>
          ) : null}
        </div>
      ) : null}

      <p className="text-muted-foreground text-xs">
        Amounts are the typical payment (the median of the last three). The
        monthly total counts confirmed subscriptions: weekly ones times 52/12,
        every 3 months a third, yearly a twelfth.
      </p>
    </section>
  );
}

function Section({
  children,
  description,
  testId,
  title,
}: {
  children: React.ReactNode;
  description?: string;
  testId: string;
  title: string;
}) {
  return (
    <Card data-testid={testId} size="sm">
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        {description ? <CardDescription>{description}</CardDescription> : null}
      </CardHeader>
      <CardContent>
        <ul className="divide-y">{children}</ul>
      </CardContent>
    </Card>
  );
}

function SubscriptionRow({
  subscription,
  category,
  pending,
  today,
  onConfirm,
  onIgnore,
  onEdit,
}: {
  subscription: Subscription;
  category?: Category;
  pending: boolean;
  today: string;
  onConfirm: (subscription: Subscription) => void;
  onIgnore: (subscription: Subscription) => void;
  onEdit: (subscription: Subscription) => void;
}) {
  const locale = useBrowserLocale();
  const { state, renewal } = subscription;
  const monthly = monthlyText(subscription, locale);
  const next =
    state === "ignored" ? "" : renewalText(subscription, today, locale);
  return (
    <li
      className="flex flex-wrap items-center gap-x-3 gap-y-1.5 py-2 first:pt-0 last:pb-0"
      data-testid="finance-subscription"
    >
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <Repeat
            aria-hidden="true"
            className="text-muted-foreground size-3.5 shrink-0"
          />
          <span className="truncate font-medium">{subscription.name}</span>
          {renewal === "upcoming" ? (
            <Badge variant="secondary">Soon</Badge>
          ) : null}
          {renewal === "missed" ? (
            <Badge variant="destructive">Missed</Badge>
          ) : null}
          {subscription.confidence === "low" && state === "suggested" ? (
            <Badge variant="outline">Less sure</Badge>
          ) : null}
        </div>
        <p className="text-muted-foreground flex flex-wrap gap-x-2 text-xs">
          <span>{cadenceLabel(subscription.cadence)}</span>
          {category ? (
            <span className="inline-flex items-center gap-1">
              <span
                aria-hidden="true"
                className="bg-muted-foreground/40 size-2 rounded-full"
                style={
                  category.color
                    ? { backgroundColor: category.color }
                    : undefined
                }
              />
              {category.name}
            </span>
          ) : null}
          {next ? (
            <span className={cn(renewal === "missed" && "text-destructive")}>
              {next}
            </span>
          ) : null}
          {state === "suggested" ? (
            <span>{evidenceText(subscription, today, locale)}</span>
          ) : null}
        </p>
      </div>
      <div className="ml-auto text-right">
        <p className="text-sm font-medium tabular-nums">
          {pricePerCadence(subscription, locale)}
        </p>
        {monthly ? (
          <p className="text-muted-foreground text-xs tabular-nums">
            {monthly}
          </p>
        ) : null}
      </div>
      <div className="flex gap-1">
        {state !== "confirmed" ? (
          <Button
            aria-label={`Confirm ${subscription.name}`}
            disabled={pending}
            onClick={() => onConfirm(subscription)}
            size="sm"
            variant={state === "suggested" ? "default" : "outline"}
          >
            <Check aria-hidden="true" />
            Confirm
          </Button>
        ) : null}
        {state !== "ignored" ? (
          <Button
            aria-label={`Ignore ${subscription.name}`}
            disabled={pending}
            onClick={() => onIgnore(subscription)}
            size="sm"
            variant="ghost"
          >
            <EyeOff aria-hidden="true" />
            Ignore
          </Button>
        ) : null}
        <Button
          aria-label={`Edit ${subscription.name}`}
          disabled={pending}
          onClick={() => onEdit(subscription)}
          size="sm"
          variant="ghost"
        >
          <Pencil aria-hidden="true" />
          <span className="sr-only sm:not-sr-only">Edit</span>
        </Button>
      </div>
    </li>
  );
}
