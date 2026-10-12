"use client";

import { Sparkles, X } from "lucide-react";
import { useState, useTransition } from "react";

import { Button } from "@/components/ui/button";
import { financeClient } from "@/lib/api/finance-client";
import {
  type CategoryRule,
  type RuleOffer,
  financeErrorMessage,
  ruleCreatedText,
  ruleOfferText,
  ruleText,
} from "@/lib/finance-workspace";

/**
 * After the owner corrects a category the AI chose, the offer to make the
 * correction stick: "Always put “Kneipe41” in Eating out?". Create rule
 * adds the rule and applies the rules, which also moves the other
 * transactions the AI put elsewhere; Not now dismisses it. Nothing happens
 * without the owner's click.
 */
export function RuleOfferBanner({
  offer,
  onCreated,
  onDismiss,
}: {
  offer: RuleOffer;
  /** The rule exists and the rules were applied. */
  onCreated: (rule: CategoryRule) => void;
  onDismiss: () => void;
}) {
  const [pending, startTransition] = useTransition();
  const [failure, setFailure] = useState<string>();
  const [done, setDone] = useState<string>();

  const create = () =>
    startTransition(async () => {
      setFailure(undefined);
      const { data: rule, error } = await financeClient
        .POST("/v1/categories/rules", {
          body: { categoryId: offer.categoryId, match: offer.match },
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!rule) {
        return setFailure(
          financeErrorMessage(error, "Could not create the rule."),
        );
      }
      // Scope uncategorized also lets the rule replace the AI's choices.
      const { data: applied } = await financeClient
        .POST("/v1/categories/rules/apply", {
          body: { scope: "uncategorized" },
        })
        .catch(() => ({ data: undefined }));
      setDone(
        applied
          ? ruleCreatedText(offer, applied)
          : `Rule created: ${ruleText(offer)} → ${offer.categoryName}. Apply the rules under Categories & rules to move other transactions.`,
      );
      onCreated(rule);
    });

  return (
    <div
      aria-label="Make a rule"
      className="bg-muted/40 mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border px-3 py-2 text-sm"
      data-testid="finance-rule-offer"
      role="region"
    >
      <Sparkles
        aria-hidden="true"
        className="text-muted-foreground size-4 shrink-0"
      />
      {done ? (
        <p className="min-w-0 flex-1" role="status">
          {done}
        </p>
      ) : (
        <div className="min-w-0 flex-1">
          <p className="font-medium">{ruleOfferText(offer)}</p>
          <p className="text-muted-foreground text-xs">
            A rule ({ruleText(offer)}) puts it there from now on, also where the
            AI chose another category.
          </p>
          {failure ? (
            <p className="text-destructive text-xs" role="alert">
              {failure}
            </p>
          ) : null}
        </div>
      )}
      {done ? (
        <Button
          aria-label="Close"
          onClick={onDismiss}
          size="icon-xs"
          variant="ghost"
        >
          <X aria-hidden="true" />
        </Button>
      ) : (
        <div className="flex gap-2">
          <Button disabled={pending} onClick={create} size="sm">
            {pending ? "Creating…" : "Create rule"}
          </Button>
          <Button
            disabled={pending}
            onClick={onDismiss}
            size="sm"
            variant="ghost"
          >
            Not now
          </Button>
        </div>
      )}
    </div>
  );
}
