"use client";

import { Wand2 } from "lucide-react";
import {
  type FormEvent,
  useEffect,
  useId,
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
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { financeClient } from "@/lib/api/finance-client";
import {
  type BudgetDraft,
  type BudgetErrors,
  type BudgetMonth,
  type BudgetScope,
  type BudgetSuggestions,
  budgetBody,
  budgetDraft,
  draftTotals,
  fetchBudgetSuggestions,
  planCategories,
  suggestionText,
  withSuggestions,
} from "@/lib/finance-budget";
import { formatMoney } from "@/lib/finance-money";
import { monthLabel } from "@/lib/finance-summary";
import {
  type Category,
  type Loaded,
  financeErrorMessage,
} from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { useClosingState } from "@/lib/use-closing-state";

export type BudgetEditorState = { budget: BudgetMonth } | null;

/**
 * Plans a month: the expected income, an amount per expense category, and a
 * savings goal, with starting amounts suggested from the last three
 * complete months. It saves for this month only, or as the regular plan for
 * this and later months.
 */
export function BudgetEditor({
  state,
  categories,
  onClose,
  onSaved,
}: {
  state: BudgetEditorState;
  categories: Category[];
  onClose: () => void;
  onSaved: (budget: BudgetMonth) => void;
}) {
  const current = useClosingState(state);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={state !== null}
    >
      <DialogContent className="sm:max-w-xl">
        {current ? (
          <BudgetForm
            budget={current.budget}
            categories={categories}
            key={`${current.budget.month}-${current.budget.plan?.updatedAt ?? "new"}`}
            onCancel={onClose}
            onSaved={(budget) => {
              onSaved(budget);
              onClose();
            }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function BudgetForm({
  budget,
  categories,
  onCancel,
  onSaved,
}: {
  budget: BudgetMonth;
  categories: Category[];
  onCancel: () => void;
  onSaved: (budget: BudgetMonth) => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const label = monthLabel(budget.month);
  const currency = budget.currency;
  const choices = planCategories(categories, budget);
  const [draft, setDraft] = useState<BudgetDraft>(() =>
    budgetDraft(budget, locale),
  );
  const [suggestions, setSuggestions] = useState<Loaded<BudgetSuggestions>>();
  const [errors, setErrors] = useState<BudgetErrors & { form?: string }>({});
  const [pending, startTransition] = useTransition();

  // A first plan starts from the suggestions; an existing one keeps its
  // amounts and offers them.
  useEffect(() => {
    let cancelled = false;
    void fetchBudgetSuggestions(budget.month).then((result) => {
      if (cancelled) return;
      setSuggestions(result);
      if (result.data && !budget.plan) {
        const found = result.data;
        setDraft((current) => withSuggestions(current, found, locale));
      }
    });
    return () => {
      cancelled = true;
    };
  }, [budget.month, budget.plan, locale]);

  const found = suggestions?.data;
  const byCategory = new Map(
    found?.categories.map((item) => [item.categoryId, item]) ?? [],
  );
  const totals = draftTotals(draft, currency);
  const money = (amount: number) => formatMoney({ amount, currency }, locale);
  const invalid = (field: string) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { body, errors: problems } = budgetBody(
      draft,
      currency,
      choices.map((category) => category.id),
    );
    if (!body) return setErrors(problems);
    startTransition(async () => {
      setErrors({});
      const { data, error } = await financeClient
        .PUT("/v1/budgets/{month}", {
          params: { path: { month: budget.month } },
          body,
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(error, "Could not save the plan."),
        });
      }
      onSaved(data);
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {budget.plan ? `Edit the plan for ${label}` : `Plan ${label}`}
        </DialogTitle>
        <DialogDescription>
          Amounts in {currency}. Leave a category empty to keep it out of the
          plan.
        </DialogDescription>
      </DialogHeader>

      <FieldGroup className="gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field data-invalid={Boolean(errors.income)}>
            <FieldLabel htmlFor={`${id}-income`}>Expected income</FieldLabel>
            <Input
              id={`${id}-income`}
              inputMode="decimal"
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  income: event.target.value,
                }))
              }
              placeholder="0.00"
              value={draft.income}
              {...invalid("income")}
              aria-describedby={
                errors.income ? `${id}-income-error` : `${id}-income-note`
              }
            />
            <FieldDescription className="text-xs" id={`${id}-income-note`}>
              {found?.income
                ? `Usually ${formatMoney(found.income, locale)} (median)`
                : "What you expect to come in this month."}
            </FieldDescription>
            <FieldError id={`${id}-income-error`}>{errors.income}</FieldError>
          </Field>
          <Field data-invalid={Boolean(errors.savings)}>
            <FieldLabel htmlFor={`${id}-savings`}>Savings goal</FieldLabel>
            <Input
              id={`${id}-savings`}
              inputMode="decimal"
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  savings: event.target.value,
                }))
              }
              placeholder="0.00"
              value={draft.savings}
              {...invalid("savings")}
              aria-describedby={
                errors.savings ? `${id}-savings-error` : `${id}-savings-note`
              }
            />
            <FieldDescription className="text-xs" id={`${id}-savings-note`}>
              {found?.saved && found.saved.amount > 0
                ? `You kept ${formatMoney(found.saved, locale)} a month (median)`
                : "Moved to Savings plus what is left over."}
            </FieldDescription>
            <FieldError id={`${id}-savings-error`}>{errors.savings}</FieldError>
          </Field>
        </div>

        <FieldSet className="gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FieldLegend className="mb-0" variant="label">
              Categories
            </FieldLegend>
            <Button
              disabled={!found?.categories.some((item) => item.suggested)}
              onClick={() =>
                found &&
                setDraft((current) =>
                  withSuggestions(current, found, locale, true),
                )
              }
              size="sm"
              type="button"
              variant="outline"
            >
              <Wand2 aria-hidden="true" />
              Use suggestions
            </Button>
          </div>
          <p className="text-muted-foreground text-xs">
            {suggestions === undefined
              ? "Looking at your last three months…"
              : suggestions.error !== undefined
                ? suggestions.error
                : found?.months.length
                  ? `Suggestions are the median of ${found.months.length === 1 ? "1 complete month" : `${found.months.length} complete months`} of booked spending.`
                  : "No history yet: suggestions come once a complete month has booked transactions."}
          </p>
          {choices.length ? (
            <div className="grid gap-3 sm:grid-cols-2">
              {choices.map((category) => (
                <Field
                  data-invalid={Boolean(errors[category.id])}
                  key={category.id}
                >
                  <FieldLabel htmlFor={`${id}-${category.id}`}>
                    {category.name}
                    {category.archived ? " (archived)" : ""}
                  </FieldLabel>
                  <Input
                    id={`${id}-${category.id}`}
                    inputMode="decimal"
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        amounts: {
                          ...current.amounts,
                          [category.id]: event.target.value,
                        },
                      }))
                    }
                    placeholder="0.00"
                    value={draft.amounts[category.id] ?? ""}
                    {...invalid(category.id)}
                    aria-describedby={
                      errors[category.id]
                        ? `${id}-${category.id}-error`
                        : `${id}-${category.id}-note`
                    }
                  />
                  <FieldDescription
                    className="text-xs"
                    id={`${id}-${category.id}-note`}
                  >
                    {suggestions === undefined
                      ? " "
                      : suggestionText(
                          byCategory.get(category.id),
                          found?.months.length ?? 0,
                          locale,
                        )}
                  </FieldDescription>
                  <FieldError id={`${id}-${category.id}-error`}>
                    {errors[category.id]}
                  </FieldError>
                </Field>
              ))}
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">
              There are no expense categories yet. Add some under Categories
              &amp; rules.
            </p>
          )}
        </FieldSet>

        <p
          aria-live="polite"
          className="bg-muted/50 rounded-lg px-3 py-2 text-sm"
          data-testid="budget-editor-totals"
        >
          Planned {money(totals.planned)} · Savings {money(totals.savings)} ·{" "}
          {totals.unallocated < 0
            ? `${money(-totals.unallocated)} more than the income`
            : `${money(totals.unallocated)} not planned`}
        </p>

        <FieldSet className="gap-2">
          <FieldLegend
            className="text-muted-foreground text-xs"
            id={`${id}-scope`}
            variant="label"
          >
            Save for
          </FieldLegend>
          <RadioGroup
            aria-labelledby={`${id}-scope`}
            onValueChange={(scope: BudgetScope) =>
              setDraft((current) => ({ ...current, scope }))
            }
            value={draft.scope}
          >
            <Label className="w-fit font-normal">
              <RadioGroupItem value="month" />
              Only {label}
            </Label>
            <Label className="w-fit font-normal">
              <RadioGroupItem value="following" />
              {label} and later months
            </Label>
          </RadioGroup>
          <FieldDescription className="text-xs">
            {draft.scope === "month"
              ? "The regular plan stays as it is for the other months."
              : "This becomes the regular plan from this month on; later months with their own plan follow it too, earlier months keep theirs."}
          </FieldDescription>
        </FieldSet>
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
          {pending ? "Saving…" : "Save plan"}
        </Button>
      </DialogFooter>
    </form>
  );
}
