"use client";

import { Loader2, Sparkles } from "lucide-react";
import Link from "next/link";
import {
  type FormEvent,
  useEffect,
  useId,
  useRef,
  useState,
  useTransition,
} from "react";

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
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { financeClient } from "@/lib/api/finance-client";
import {
  type AiBudgetPlan,
  type AiBudgetSuggestion,
  type PlanDraft,
  type PlanDraftErrors,
  PLAN_POLL_MS,
  appliedText,
  applyBody,
  defaultPlanMonth,
  evidenceText,
  fetchAiPlans,
  goalText,
  isActive,
  planDraft,
  planMonthChoices,
  planTitle,
  progressText,
  roughText,
  savingText,
  triggerText,
  visiblePlan,
} from "@/lib/finance-ai-plan";
import type { BudgetMonth } from "@/lib/finance-budget";
import { formatMoney, minorDigits, parseAmount } from "@/lib/finance-money";
import { monthLabel } from "@/lib/finance-summary";
import { financeErrorMessage } from "@/lib/finance-workspace";
import { foodHref } from "@/lib/food";
import { useBrowserLocale } from "@/lib/use-browser-locale";

/** Marks what the AI made, so it never reads as the owner's own plan. */
function MadeByAI() {
  return (
    <Badge variant="secondary">
      <Sparkles aria-hidden="true" />
      Made by AI
    </Badge>
  );
}

/** Why there is a plan the owner did not ask for (the salary's). */
function TriggerNote({ plan }: { plan: AiBudgetPlan }) {
  const text = triggerText(plan);
  return text ? (
    <p
      className="text-muted-foreground text-xs"
      data-testid="finance-ai-plan-trigger"
    >
      {text}
    </p>
  ) : null;
}

/**
 * Asks the AI to plan a month: the month (this month in its first week,
 * else the next by default) and an optional savings goal. The plan is made
 * in the background by the processor; the card on the Budget tab follows
 * it.
 */
export function AiPlanRequestDialog({
  currency,
  open,
  today,
  onClose,
  onRequested,
}: {
  /** The budget currency, for the goal. */
  currency: string;
  open: boolean;
  /** The owner's today (YYYY-MM-DD). */
  today: string;
  onClose: () => void;
  onRequested: (plan: AiBudgetPlan) => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent>
        {open ? (
          <RequestForm
            currency={currency}
            onCancel={onClose}
            onRequested={onRequested}
            today={today}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function RequestForm({
  currency,
  today,
  onCancel,
  onRequested,
}: {
  currency: string;
  today: string;
  onCancel: () => void;
  onRequested: (plan: AiBudgetPlan) => void;
}) {
  const id = useId();
  const [month, setMonth] = useState(() => defaultPlanMonth(today));
  const [goal, setGoal] = useState("");
  const [errors, setErrors] = useState<{ goal?: string; form?: string }>({});
  const [pending, startTransition] = useTransition();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    let savingsGoalMinor: number | undefined;
    if (goal.trim()) {
      savingsGoalMinor = parseAmount(goal, currency);
      if (savingsGoalMinor === undefined || savingsGoalMinor < 0) {
        return setErrors({
          goal: minorDigits(currency)
            ? `Enter an amount like 100.00, with at most ${minorDigits(currency)} decimals, or leave it empty.`
            : "Enter a whole amount, like 100, or leave it empty.",
        });
      }
    }
    startTransition(async () => {
      setErrors({});
      const { data, error } = await financeClient
        .POST("/v1/ai-budget-plans", {
          body: { month, savingsGoalMinor },
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(error, "Could not ask the AI for a plan."),
        });
      }
      onRequested(data);
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Plan my budget</DialogTitle>
        <DialogDescription>
          The AI proposes an amount for each category and ways to spend less,
          from your spending per category and merchant, your income, and your
          subscriptions (totals only, never single transactions). Nothing
          changes until you apply it.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup className="gap-4">
        <Field>
          <FieldLabel htmlFor={`${id}-month`}>Month</FieldLabel>
          <NativeSelect
            className="w-full"
            id={`${id}-month`}
            onChange={(event) => setMonth(event.target.value)}
            value={month}
          >
            {planMonthChoices(today).map((choice) => (
              <NativeSelectOption key={choice} value={choice}>
                {monthLabel(choice)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
        <Field data-invalid={Boolean(errors.goal)}>
          <FieldLabel htmlFor={`${id}-goal`}>
            Savings goal per month (optional)
          </FieldLabel>
          <Input
            aria-describedby={
              errors.goal ? `${id}-goal-error` : `${id}-goal-note`
            }
            aria-invalid={errors.goal ? true : undefined}
            id={`${id}-goal`}
            inputMode="decimal"
            onChange={(event) => setGoal(event.target.value)}
            placeholder="0.00"
            value={goal}
          />
          <FieldDescription className="text-xs" id={`${id}-goal-note`}>
            How much you want to keep each month, in {currency}.
          </FieldDescription>
          <FieldError id={`${id}-goal-error`}>{errors.goal}</FieldError>
        </Field>
      </FieldGroup>
      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}
      <DialogFooter>
        <Button
          disabled={pending}
          onClick={onCancel}
          type="button"
          variant="outline"
        >
          Cancel
        </Button>
        <Button disabled={pending} type="submit">
          <Sparkles aria-hidden="true" />
          {pending ? "Asking…" : "Plan my budget"}
        </Button>
      </DialogFooter>
    </form>
  );
}

/**
 * The AI's plan for the month shown, from the request until the owner
 * applies or dismisses it: progress while the AI plans (checked every few
 * seconds), then the proposal to apply line by line, or why it failed.
 * Mount it with a key per month and request, so it reads afresh.
 */
export function AiPlanCard({
  budget,
  month,
  timeZone,
  onApplied,
}: {
  /** The month's budget, for the current plan's amounts and income. */
  budget: BudgetMonth | undefined;
  /** YYYY-MM. */
  month: string;
  timeZone: string;
  /** The month's budget after the plan was applied. */
  onApplied: (budget: BudgetMonth) => void;
}) {
  const [plans, setPlans] = useState<AiBudgetPlan[]>();
  const [loadError, setLoadError] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const loaded = useRef(false);
  const plan = plans ? visiblePlan(plans) : undefined;
  const active = isActive(plan);

  useEffect(() => {
    let cancelled = false;
    const read = () =>
      void fetchAiPlans(month).then((result) => {
        if (cancelled) return;
        if (result.error !== undefined) return setLoadError(result.error);
        setLoadError(undefined);
        setPlans(result.data.items);
      });
    // Read once, then every few seconds while the AI plans.
    if (!loaded.current || active) {
      loaded.current = true;
      read();
    }
    const timer = active ? window.setInterval(read, PLAN_POLL_MS) : undefined;
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [month, active]);

  /** Keeps a changed plan in place of the old one. */
  const replace = (next: AiBudgetPlan) =>
    setPlans((current) => [
      next,
      ...(current ?? []).filter((item) => item.id !== next.id),
    ]);

  const dismiss = (target: AiBudgetPlan) =>
    startTransition(async () => {
      setActionError(undefined);
      const { data, error } = await financeClient
        .POST("/v1/ai-budget-plans/{planId}/dismiss", {
          params: { path: { planId: target.id } },
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setActionError(
          financeErrorMessage(error, "Could not dismiss the plan."),
        );
      }
      replace(data);
    });

  const askAgain = (target: AiBudgetPlan) =>
    startTransition(async () => {
      setActionError(undefined);
      const { data, error } = await financeClient
        .POST("/v1/ai-budget-plans", {
          body: {
            month: target.month,
            savingsGoalMinor: target.savingsGoal?.amount,
          },
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setActionError(
          financeErrorMessage(error, "Could not ask the AI again."),
        );
      }
      replace(data);
    });

  if (loadError && !plans) {
    return (
      <p className="text-destructive text-sm" role="alert">
        {loadError}
      </p>
    );
  }
  if (!plan) return null;
  const error = actionError ? (
    <p className="text-destructive text-sm" role="alert">
      {actionError}
    </p>
  ) : null;

  if (active) {
    return (
      <Card data-testid="finance-ai-plan" size="sm">
        <CardContent className="flex flex-wrap items-center gap-3">
          <Loader2
            aria-hidden="true"
            className="text-muted-foreground size-5 animate-spin"
          />
          <div className="grid min-w-0 flex-1 gap-0.5">
            <p className="font-medium">{planTitle(plan)}</p>
            <TriggerNote plan={plan} />
            <p className="text-muted-foreground text-sm" role="status">
              {progressText(plan, timeZone)}
            </p>
          </div>
          <Button
            disabled={pending}
            onClick={() => dismiss(plan)}
            size="sm"
            variant="ghost"
          >
            Cancel
          </Button>
          {error}
        </CardContent>
      </Card>
    );
  }

  if (plan.state === "failed") {
    return (
      <Card data-testid="finance-ai-plan" size="sm">
        <CardHeader>
          <CardTitle>The AI could not plan {monthLabel(plan.month)}.</CardTitle>
          {plan.error ? (
            <CardDescription className="wrap-anywhere">
              {plan.error}
            </CardDescription>
          ) : null}
        </CardHeader>
        <CardFooter className="flex-wrap justify-end gap-2">
          {error}
          <Button
            disabled={pending}
            onClick={() => dismiss(plan)}
            size="sm"
            variant="outline"
          >
            Dismiss
          </Button>
          <Button disabled={pending} onClick={() => askAgain(plan)} size="sm">
            <Sparkles aria-hidden="true" />
            Try again
          </Button>
        </CardFooter>
      </Card>
    );
  }

  if (plan.state === "applied" || plan.state === "partly_applied") {
    return (
      <Card data-testid="finance-ai-plan" size="sm">
        <CardContent className="flex flex-wrap items-center gap-2">
          <MadeByAI />
          <div className="grid min-w-0 flex-1 gap-0.5">
            <p>
              {planTitle(plan)}: {appliedText(plan)}
            </p>
            <TriggerNote plan={plan} />
          </div>
          <Button
            disabled={pending}
            onClick={() => dismiss(plan)}
            size="sm"
            variant="ghost"
          >
            Hide
          </Button>
          {error}
        </CardContent>
      </Card>
    );
  }

  if (!plan.result) return null;
  return (
    <ReadyPlan
      budget={budget}
      error={actionError}
      key={plan.id}
      onApplied={(next, view) => {
        replace(next);
        onApplied(view);
      }}
      onDismiss={() => dismiss(plan)}
      pending={pending}
      plan={plan}
    />
  );
}

function ReadyPlan({
  budget,
  error,
  pending: dismissing,
  plan,
  onApplied,
  onDismiss,
}: {
  budget: BudgetMonth | undefined;
  error?: string;
  pending: boolean;
  plan: AiBudgetPlan;
  onApplied: (plan: AiBudgetPlan, budget: BudgetMonth) => void;
  onDismiss: () => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const result = plan.result!;
  const label = monthLabel(plan.month);
  const [draft, setDraft] = useState<PlanDraft>(() => planDraft(plan, locale));
  const [regular, setRegular] = useState(false);
  const [errors, setErrors] = useState<PlanDraftErrors>({});
  const [pending, startTransition] = useTransition();
  const busy = pending || dismissing;
  // What the budget plans now, which may have changed since the AI looked.
  const planned = new Map(
    budget?.lines.map((line) => [line.categoryId, line.planned]) ?? [],
  );

  const update = (categoryId: string, change: Partial<PlanDraft[string]>) =>
    setDraft((current) => ({
      ...current,
      [categoryId]: { ...current[categoryId], ...change },
    }));

  const apply = () => {
    const { body, errors: problems } = applyBody(draft, plan, budget, regular);
    if (!body) return setErrors(problems);
    startTransition(async () => {
      setErrors({});
      const { data, error: failure } = await financeClient
        .POST("/v1/ai-budget-plans/{planId}/apply", {
          params: { path: { planId: plan.id } },
          body,
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(failure, "Could not apply the plan."),
        });
      }
      onApplied(data.plan, data.budget);
    });
  };

  return (
    <Card
      aria-labelledby={`${id}-title`}
      data-testid="finance-ai-plan"
      role="region"
    >
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <span id={`${id}-title`}>{planTitle(plan)}</span>
          <MadeByAI />
        </CardTitle>
        <CardDescription className="text-foreground text-sm">
          {result.headline}
        </CardDescription>
        <TriggerNote plan={plan} />
        {result.rough ? (
          <p className="text-muted-foreground text-xs" role="note">
            {roughText(result)} Check the amounts before you apply them.
          </p>
        ) : null}
      </CardHeader>
      <CardContent className="grid gap-4">
        {result.suggestions.length ? (
          <section aria-labelledby={`${id}-less`} className="grid gap-2">
            <h3 className="text-sm font-medium" id={`${id}-less`}>
              Ways to spend less
            </h3>
            <ul className="grid gap-2">
              {result.suggestions.map((suggestion, index) => (
                <Suggestion
                  key={`${index}-${suggestion.title}`}
                  locale={locale}
                  suggestion={suggestion}
                />
              ))}
            </ul>
          </section>
        ) : null}

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8">
                <span className="sr-only">Apply</span>
              </TableHead>
              <TableHead>Category</TableHead>
              <TableHead className="text-right">Current plan</TableHead>
              <TableHead>Proposed</TableHead>
              <TableHead>Reason</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {result.lines.map((line) => {
              const field = draft[line.categoryId];
              const current = planned.get(line.categoryId) ?? line.current;
              const evidence = evidenceText(line, locale);
              const problem = errors[line.categoryId];
              return (
                <TableRow
                  data-testid="finance-ai-plan-line"
                  key={line.categoryId}
                >
                  <TableCell>
                    <Checkbox
                      aria-label={`Apply ${line.categoryName}`}
                      checked={field?.checked ?? false}
                      disabled={busy}
                      onCheckedChange={(checked) =>
                        update(line.categoryId, { checked })
                      }
                    />
                  </TableCell>
                  <TableCell className="font-medium whitespace-normal">
                    {line.categoryName}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {current.amount > 0 ? formatMoney(current, locale) : "—"}
                  </TableCell>
                  <TableCell>
                    <Input
                      aria-describedby={
                        problem ? `${id}-${line.categoryId}-error` : undefined
                      }
                      aria-invalid={problem ? true : undefined}
                      aria-label={`Proposed for ${line.categoryName}`}
                      className="h-8 w-28 tabular-nums"
                      disabled={busy || !field?.checked}
                      inputMode="decimal"
                      onChange={(event) =>
                        update(line.categoryId, { amount: event.target.value })
                      }
                      value={field?.amount ?? ""}
                    />
                    {problem ? (
                      <p
                        className="text-destructive mt-1 max-w-48 text-xs whitespace-normal"
                        id={`${id}-${line.categoryId}-error`}
                      >
                        {problem}
                      </p>
                    ) : null}
                  </TableCell>
                  <TableCell className="min-w-48 whitespace-normal">
                    <p>{line.reason}</p>
                    {evidence ? (
                      <p className="text-muted-foreground text-xs">
                        {evidence}
                      </p>
                    ) : null}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>

        <p
          className="bg-muted/50 rounded-lg px-3 py-2 text-sm"
          data-testid="finance-ai-plan-totals"
        >
          Planned {formatMoney(result.projectedTotal, locale)} · Income about{" "}
          {formatMoney(result.incomeEstimate, locale)} ·{" "}
          {goalText(result, locale)}
        </p>
        <Label className="w-fit font-normal">
          <Checkbox
            checked={regular}
            disabled={busy}
            onCheckedChange={(checked) => setRegular(checked)}
          />
          Also use as the regular plan from {label} on
        </Label>
        {(errors.form ?? error) ? (
          <p className="text-destructive text-sm" role="alert">
            {errors.form ?? error}
          </p>
        ) : null}
      </CardContent>
      <CardFooter className="flex-wrap justify-end gap-2 border-t pt-3 pb-3">
        <Button disabled={busy} onClick={onDismiss} variant="outline">
          Dismiss
        </Button>
        <Button disabled={busy} onClick={apply}>
          {pending ? "Applying…" : "Apply plan"}
        </Button>
      </CardFooter>
    </Card>
  );
}

function Suggestion({
  locale,
  suggestion,
}: {
  locale: string;
  suggestion: AiBudgetSuggestion;
}) {
  const where = [suggestion.categoryName, ...suggestion.merchants]
    .filter(Boolean)
    .join(" · ");
  return (
    <li
      className="grid gap-0.5 rounded-lg border px-3 py-2"
      data-testid="finance-ai-plan-suggestion"
    >
      <div className="flex flex-wrap items-baseline gap-x-2">
        <span className="font-medium">{suggestion.title}</span>
        <span className="ml-auto text-sm font-medium tabular-nums">
          {savingText(suggestion.monthlySaving, locale)}
        </span>
      </div>
      <p className="text-sm">{suggestion.detail}</p>
      {where || suggestion.link ? (
        <p className="text-muted-foreground flex flex-wrap gap-x-2 text-xs">
          {where ? <span>{where}</span> : null}
          {suggestion.link === "food_recipes" ? (
            <Link
              className="text-primary underline-offset-4 hover:underline"
              href={foodHref("recipes")}
            >
              Find recipes
            </Link>
          ) : null}
        </p>
      ) : null}
    </li>
  );
}
