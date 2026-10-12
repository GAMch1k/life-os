"use client";

import {
  Archive,
  ArchiveRestore,
  Pencil,
  Plus,
  Trash2,
  Wand2,
} from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
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
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  NativeSelect,
  NativeSelectOptGroup,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { financeClient } from "@/lib/api/finance-client";
import {
  type ApplyResult,
  type ApplyScope,
  CATEGORY_COLORS,
  CATEGORY_KINDS,
  type Category,
  type CategoryKind,
  type CategoryRule,
  RULE_FIELDS,
  RULE_OPERATORS,
  type RuleDraft,
  type RuleErrors,
  type RuleField,
  type RuleOperator,
  applyResultText,
  byId,
  categoryDraft,
  categoryGroups,
  createCategoryBody,
  createRuleBody,
  financeErrorMessage,
  ruleDraft,
  ruleText,
  updateCategoryBody,
  updateRuleBody,
} from "@/lib/finance-workspace";
import { useClosingState } from "@/lib/use-closing-state";
import { cn } from "@/lib/utils";

const EMPTY =
  "text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm";

/** The color picker's value for a category without a color. */
const NO_COLOR = "none";

type CategoryDialogState = { category?: Category } | null;
type RuleDialogState = { rule?: CategoryRule } | null;

/**
 * Categories (by kind, with a color) and the rules that give transactions a
 * category by their counterparty or description. Rules run on new
 * transactions by themselves; "Apply rules" runs them on existing ones. A
 * category chosen by hand is never changed by a rule.
 */
export function FinanceCategories({
  categories,
  categoriesError,
  rules,
  rulesError,
  onCategorySaved,
  onRuleSaved,
  onRuleDeleted,
  onRulesApplied,
}: {
  categories: Category[];
  categoriesError?: string;
  rules: CategoryRule[];
  rulesError?: string;
  onCategorySaved: (category: Category) => void;
  onRuleSaved: (rule: CategoryRule) => void;
  onRuleDeleted: (rule: CategoryRule) => void;
  /** Rules changed transactions: the list and summary are out of date. */
  onRulesApplied: () => void;
}) {
  const [categoryDialog, setCategoryDialog] =
    useState<CategoryDialogState>(null);
  const [ruleDialog, setRuleDialog] = useState<RuleDialogState>(null);
  return (
    <div className="grid gap-6 lg:grid-cols-2 lg:items-start">
      <CategoryList
        categories={categories}
        error={categoriesError}
        onEdit={setCategoryDialog}
        onSaved={onCategorySaved}
      />
      <RuleList
        categories={categories}
        error={rulesError}
        onApplied={onRulesApplied}
        onDeleted={onRuleDeleted}
        onEdit={setRuleDialog}
        rules={rules}
      />
      <CategoryDialog
        onClose={() => setCategoryDialog(null)}
        onSaved={onCategorySaved}
        state={categoryDialog}
      />
      <RuleDialog
        categories={categories}
        onClose={() => setRuleDialog(null)}
        onSaved={onRuleSaved}
        state={ruleDialog}
      />
    </div>
  );
}

function SectionHeading({
  id,
  title,
  count,
  children,
}: {
  id: string;
  title: string;
  count: number;
  children?: ReactNode;
}) {
  return (
    <div className="mb-1.5 flex min-h-7 flex-wrap items-center gap-2">
      <h2 className="text-sm font-semibold" id={id}>
        {title}
      </h2>
      <span className="text-muted-foreground text-xs tabular-nums">
        {count}
      </span>
      <div className="ml-auto flex gap-1">{children}</div>
    </div>
  );
}

function ColorDot({ color }: { color: string | null }) {
  return (
    <span
      aria-hidden="true"
      className="bg-muted-foreground/40 size-2.5 shrink-0 rounded-full"
      style={color ? { backgroundColor: color } : undefined}
    />
  );
}

function CategoryList({
  categories,
  error,
  onEdit,
  onSaved,
}: {
  categories: Category[];
  error?: string;
  onEdit: (state: NonNullable<CategoryDialogState>) => void;
  onSaved: (category: Category) => void;
}) {
  const [showArchived, setShowArchived] = useState(false);
  const [failure, setFailure] = useState<string>();
  const [pending, startTransition] = useTransition();
  const active = categories.filter((category) => !category.archived);
  const archived = categories.filter((category) => category.archived);
  const setArchived = (category: Category, value: boolean) =>
    startTransition(async () => {
      setFailure(undefined);
      const { data, error: requestError } = await financeClient
        .PATCH("/v1/categories/{categoryId}", {
          params: { path: { categoryId: category.id } },
          body: { archived: value },
        })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setFailure(
          financeErrorMessage(
            requestError,
            `Could not ${value ? "archive" : "restore"} ${category.name}.`,
          ),
        );
      }
      onSaved(data);
    });

  const row = (category: Category) => (
    <li
      className="flex min-w-0 items-center gap-2 border-b py-1.5 last:border-0"
      data-testid="finance-category"
      key={category.id}
    >
      <ColorDot color={category.color} />
      <span
        className={cn(
          "min-w-0 flex-1 truncate",
          category.archived && "text-muted-foreground line-through",
        )}
      >
        {category.name}
      </span>
      <Button
        aria-label={`Edit category ${category.name}`}
        disabled={pending}
        onClick={() => onEdit({ category })}
        size="icon-xs"
        variant="ghost"
      >
        <Pencil aria-hidden="true" />
      </Button>
      <Button
        aria-label={`${category.archived ? "Restore" : "Archive"} category ${category.name}`}
        disabled={pending}
        onClick={() => setArchived(category, !category.archived)}
        size="icon-xs"
        title={
          category.archived
            ? "Restore"
            : "Archive: transactions keep it, but it can't be chosen and its rules are skipped"
        }
        variant="ghost"
      >
        {category.archived ? (
          <ArchiveRestore aria-hidden="true" />
        ) : (
          <Archive aria-hidden="true" />
        )}
      </Button>
    </li>
  );

  return (
    <section aria-labelledby="finance-categories-title">
      <SectionHeading
        count={active.length}
        id="finance-categories-title"
        title="Categories"
      >
        {archived.length ? (
          <Button
            aria-pressed={showArchived}
            className="aria-pressed:bg-muted"
            onClick={() => setShowArchived(!showArchived)}
            size="sm"
            variant="ghost"
          >
            {showArchived ? "Hide archived" : `Archived (${archived.length})`}
          </Button>
        ) : null}
        <Button onClick={() => onEdit({})} size="sm" variant="outline">
          <Plus aria-hidden="true" />
          Add category
        </Button>
      </SectionHeading>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : active.length === 0 && !showArchived ? (
        <p className={EMPTY}>
          No categories yet. Add one, such as Groceries or Salary.
        </p>
      ) : (
        <div className="grid gap-3">
          {categoryGroups(active).map((group) => (
            <div key={group.kind}>
              <h3 className="text-muted-foreground mb-0.5 text-xs font-medium">
                {group.label}
              </h3>
              <ul className="bg-card rounded-lg border px-3">
                {group.items.map(row)}
              </ul>
            </div>
          ))}
          {showArchived && archived.length ? (
            <div>
              <h3 className="text-muted-foreground mb-0.5 text-xs font-medium">
                Archived
              </h3>
              <ul className="bg-card rounded-lg border px-3">
                {archived.map(row)}
              </ul>
            </div>
          ) : null}
        </div>
      )}
      {failure ? (
        <p className="text-destructive mt-2 text-sm" role="alert">
          {failure}
        </p>
      ) : null}
    </section>
  );
}

function RuleList({
  categories,
  error,
  rules,
  onApplied,
  onDeleted,
  onEdit,
}: {
  categories: Category[];
  error?: string;
  rules: CategoryRule[];
  onApplied: () => void;
  onDeleted: (rule: CategoryRule) => void;
  onEdit: (state: NonNullable<RuleDialogState>) => void;
}) {
  const [scope, setScope] = useState<ApplyScope>("uncategorized");
  const [result, setResult] = useState<ApplyResult>();
  const [failure, setFailure] = useState<string>();
  const [pending, startTransition] = useTransition();
  const categoriesById = byId(categories);
  const canAdd = categories.some((category) => !category.archived);

  const remove = (rule: CategoryRule) =>
    startTransition(async () => {
      setFailure(undefined);
      const { response, error: requestError } = await financeClient
        .DELETE("/v1/categories/rules/{ruleId}", {
          params: { path: { ruleId: rule.id } },
        })
        .catch(() => ({ response: undefined, error: undefined }));
      if (!response?.ok) {
        return setFailure(
          financeErrorMessage(requestError, "Could not delete this rule."),
        );
      }
      onDeleted(rule);
    });

  const apply = () =>
    startTransition(async () => {
      setFailure(undefined);
      setResult(undefined);
      const { data, error: requestError } = await financeClient
        .POST("/v1/categories/rules/apply", { body: { scope } })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setFailure(
          financeErrorMessage(requestError, "Could not apply the rules."),
        );
      }
      setResult(data);
      onApplied();
    });

  return (
    <section aria-labelledby="finance-rules-title">
      <SectionHeading
        count={rules.length}
        id="finance-rules-title"
        title="Rules"
      >
        <Button
          disabled={!canAdd}
          onClick={() => onEdit({})}
          size="sm"
          title={canAdd ? undefined : "Add a category first"}
          variant="outline"
        >
          <Plus aria-hidden="true" />
          Add rule
        </Button>
      </SectionHeading>
      <p className="text-muted-foreground mb-2 text-xs">
        A rule gives a category to transactions without one, by their
        counterparty or description. Rules are tried in order and the first
        match wins; a category you chose by hand is never changed. A card
        payment no rule matches gets a default category from its merchant
        category code. What is still left, the AI categorizes (marked “by AI”),
        and a rule that matches replaces the AI’s choice.
      </p>
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : rules.length === 0 ? (
        <p className={EMPTY}>
          No rules yet. For example: counterparty contains “REWE” → Groceries.
        </p>
      ) : (
        <ol className="bg-card rounded-lg border px-3">
          {rules.map((rule) => {
            const category = categoriesById.get(rule.categoryId);
            const text = ruleText(rule);
            return (
              <li
                className="flex min-w-0 items-center gap-2 border-b py-1.5 last:border-0"
                data-testid="finance-rule"
                key={rule.id}
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{text}</span>
                  <span className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
                    → <ColorDot color={category?.color ?? null} />
                    <span className="truncate">
                      {category?.name ?? "Unknown category"}
                      {category?.archived ? " (archived, skipped)" : ""}
                    </span>
                  </span>
                </span>
                <Button
                  aria-label={`Edit rule ${text}`}
                  disabled={pending}
                  onClick={() => onEdit({ rule })}
                  size="icon-xs"
                  variant="ghost"
                >
                  <Pencil aria-hidden="true" />
                </Button>
                <Button
                  aria-label={`Delete rule ${text}`}
                  disabled={pending}
                  onClick={() => remove(rule)}
                  size="icon-xs"
                  variant="ghost"
                >
                  <Trash2 aria-hidden="true" />
                </Button>
              </li>
            );
          })}
        </ol>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <NativeSelect
          aria-label="Apply the rules to"
          onChange={(event) => setScope(event.target.value as ApplyScope)}
          size="sm"
          value={scope}
        >
          <NativeSelectOption value="uncategorized">
            Transactions without a category, or with the AI’s
          </NativeSelectOption>
          <NativeSelectOption value="all">
            Also re-check categories rules or codes chose
          </NativeSelectOption>
        </NativeSelect>
        <Button
          disabled={pending || rules.length === 0}
          onClick={apply}
          size="sm"
        >
          <Wand2 aria-hidden="true" />
          {pending ? "Applying…" : "Apply rules"}
        </Button>
      </div>
      {result ? (
        <p className="mt-2 text-sm" role="status">
          {applyResultText(result)}
        </p>
      ) : null}
      {failure ? (
        <p className="text-destructive mt-2 text-sm" role="alert">
          {failure}
        </p>
      ) : null}
    </section>
  );
}

function CategoryDialog({
  state,
  onClose,
  onSaved,
}: {
  state: CategoryDialogState;
  onClose: () => void;
  onSaved: (category: Category) => void;
}) {
  const current = useClosingState(state);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={state !== null}
    >
      <DialogContent>
        {current ? (
          <CategoryForm
            category={current.category}
            key={current.category?.updatedAt ?? "new"}
            onCancel={onClose}
            onSaved={(category) => {
              onSaved(category);
              onClose();
            }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function CategoryForm({
  category,
  onCancel,
  onSaved,
}: {
  category?: Category;
  onCancel: () => void;
  onSaved: (category: Category) => void;
}) {
  const id = useId();
  const [draft, setDraft] = useState(() => categoryDraft(category));
  const [error, setError] = useState<{ name?: string; form?: string }>({});
  const [pending, startTransition] = useTransition();

  const send = (request: () => Promise<{ data?: Category; error?: unknown }>) =>
    startTransition(async () => {
      setError({});
      const { data, error: requestError } = await request().catch(() => ({
        data: undefined,
        error: undefined,
      }));
      if (!data) {
        return setError({
          form: financeErrorMessage(
            requestError,
            "Could not save this category.",
          ),
        });
      }
      onSaved(data);
    });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!category) {
      const { body, error: problem } = createCategoryBody(draft);
      if (!body) return setError({ name: problem });
      return send(() => financeClient.POST("/v1/categories", { body }));
    }
    const { body, error: problem } = updateCategoryBody(category, draft);
    if (problem) return setError({ name: problem });
    if (!body) return onCancel();
    send(() =>
      financeClient.PATCH("/v1/categories/{categoryId}", {
        params: { path: { categoryId: category.id } },
        body,
      }),
    );
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {category ? `Edit ${category.name}` : "Add category"}
        </DialogTitle>
        <DialogDescription>
          Expenses and income count in the monthly summary; transfers (money
          moved between your own accounts) don&apos;t.
        </DialogDescription>
      </DialogHeader>
      <Field data-invalid={Boolean(error.name)}>
        <FieldLabel htmlFor={`${id}-name`}>Name</FieldLabel>
        <Input
          aria-describedby={error.name ? `${id}-name-error` : undefined}
          aria-invalid={error.name ? true : undefined}
          autoFocus={!category}
          id={`${id}-name`}
          maxLength={100}
          onChange={(event) =>
            setDraft((current) => ({ ...current, name: event.target.value }))
          }
          value={draft.name}
        />
        <FieldError id={`${id}-name-error`}>{error.name}</FieldError>
      </Field>
      <Field>
        <FieldLabel htmlFor={`${id}-kind`}>Kind</FieldLabel>
        <NativeSelect
          className="w-full"
          id={`${id}-kind`}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              kind: event.target.value as CategoryKind,
            }))
          }
          value={draft.kind}
        >
          {CATEGORY_KINDS.map((kind) => (
            <NativeSelectOption key={kind.value} value={kind.value}>
              {kind.label}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </Field>
      <div className="grid gap-1.5">
        <p className="text-sm font-medium" id={`${id}-color`}>
          Color
        </p>
        <ToggleGroup
          aria-labelledby={`${id}-color`}
          className="flex-wrap"
          onValueChange={(chosen) =>
            setDraft((current) => ({
              ...current,
              color: chosen[0] && chosen[0] !== NO_COLOR ? chosen[0] : "",
            }))
          }
          size="sm"
          spacing={1}
          value={[draft.color || NO_COLOR]}
          variant="outline"
        >
          <ToggleGroupItem
            aria-label="No color"
            className="aria-pressed:ring-ring aria-pressed:ring-2"
            value={NO_COLOR}
          >
            None
          </ToggleGroupItem>
          {CATEGORY_COLORS.map((color) => (
            <ToggleGroupItem
              aria-label={color.label}
              className="aria-pressed:ring-ring w-8 aria-pressed:ring-2"
              key={color.value}
              value={color.value}
            >
              <span
                aria-hidden="true"
                className="size-3.5 rounded-full"
                style={{ backgroundColor: color.value }}
              />
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </div>
      {error.form ? (
        <p className="text-destructive text-sm" role="alert">
          {error.form}
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
          {pending ? "Saving…" : category ? "Save" : "Add category"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function RuleDialog({
  categories,
  state,
  onClose,
  onSaved,
}: {
  categories: Category[];
  state: RuleDialogState;
  onClose: () => void;
  onSaved: (rule: CategoryRule) => void;
}) {
  const current = useClosingState(state);
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open={state !== null}
    >
      <DialogContent>
        {current ? (
          <RuleForm
            categories={categories}
            key={current.rule?.updatedAt ?? "new"}
            onCancel={onClose}
            onSaved={(rule) => {
              onSaved(rule);
              onClose();
            }}
            rule={current.rule}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function RuleForm({
  categories,
  rule,
  onCancel,
  onSaved,
}: {
  categories: Category[];
  rule?: CategoryRule;
  onCancel: () => void;
  onSaved: (rule: CategoryRule) => void;
}) {
  const id = useId();
  const [draft, setDraft] = useState<RuleDraft>(() => ruleDraft(rule));
  const [errors, setErrors] = useState<RuleErrors & { form?: string }>({});
  const [pending, startTransition] = useTransition();
  const update = (patch: Partial<RuleDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  const invalid = (field: keyof RuleErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const send = (
    request: () => Promise<{ data?: CategoryRule; error?: unknown }>,
  ) =>
    startTransition(async () => {
      setErrors({});
      const { data, error } = await request().catch(() => ({
        data: undefined,
        error: undefined,
      }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(error, "Could not save this rule."),
        });
      }
      onSaved(data);
    });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!rule) {
      const { body, errors: found } = createRuleBody(draft);
      if (!body) return setErrors(found);
      return send(() => financeClient.POST("/v1/categories/rules", { body }));
    }
    const { body, errors: found } = updateRuleBody(rule, draft);
    if (Object.keys(found).length) return setErrors(found);
    if (!body) return onCancel();
    send(() =>
      financeClient.PATCH("/v1/categories/rules/{ruleId}", {
        params: { path: { ruleId: rule.id } },
        body,
      }),
    );
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>{rule ? "Edit rule" : "Add rule"}</DialogTitle>
        <DialogDescription>
          Matches ignore upper and lower case. New transactions get the category
          by themselves; use Apply rules for existing ones.
        </DialogDescription>
      </DialogHeader>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-field`}>When</FieldLabel>
          <NativeSelect
            className="w-full"
            id={`${id}-field`}
            onChange={(event) =>
              update({ field: event.target.value as RuleField })
            }
            value={draft.field}
          >
            {RULE_FIELDS.map((field) => (
              <NativeSelectOption key={field.value} value={field.value}>
                {field.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-operator`}>Match</FieldLabel>
          <NativeSelect
            className="w-full"
            id={`${id}-operator`}
            onChange={(event) =>
              update({ operator: event.target.value as RuleOperator })
            }
            value={draft.operator}
          >
            {RULE_OPERATORS.map((operator) => (
              <NativeSelectOption key={operator.value} value={operator.value}>
                {operator.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      </div>
      <Field data-invalid={Boolean(errors.text)}>
        <FieldLabel htmlFor={`${id}-text`}>Text</FieldLabel>
        <Input
          autoFocus={!rule}
          id={`${id}-text`}
          maxLength={200}
          onChange={(event) => update({ text: event.target.value })}
          placeholder="REWE"
          value={draft.text}
          {...invalid("text")}
        />
        <FieldError id={`${id}-text-error`}>{errors.text}</FieldError>
      </Field>
      <Field data-invalid={Boolean(errors.categoryId)}>
        <FieldLabel htmlFor={`${id}-category`}>Category</FieldLabel>
        <NativeSelect
          className="w-full"
          id={`${id}-category`}
          onChange={(event) => update({ categoryId: event.target.value })}
          value={draft.categoryId}
          {...invalid("categoryId")}
        >
          <NativeSelectOption disabled value="">
            Choose a category
          </NativeSelectOption>
          {categoryGroups(categories, rule?.categoryId).map((group) => (
            <NativeSelectOptGroup key={group.kind} label={group.label}>
              {group.items.map((category) => (
                <NativeSelectOption key={category.id} value={category.id}>
                  {category.name}
                </NativeSelectOption>
              ))}
            </NativeSelectOptGroup>
          ))}
        </NativeSelect>
        <FieldError id={`${id}-categoryId-error`}>
          {errors.categoryId}
        </FieldError>
      </Field>
      <Field data-invalid={Boolean(errors.priority)}>
        <FieldLabel htmlFor={`${id}-priority`}>Priority (optional)</FieldLabel>
        <Input
          id={`${id}-priority`}
          inputMode="numeric"
          onChange={(event) => update({ priority: event.target.value })}
          value={draft.priority}
          {...invalid("priority")}
          aria-describedby={
            errors.priority ? `${id}-priority-error` : `${id}-priority-note`
          }
        />
        <FieldDescription className="text-xs" id={`${id}-priority-note`}>
          Lower numbers are tried first. Empty puts a new rule after your other
          rules, before the default merchant rules (100000 and up).
        </FieldDescription>
        <FieldError id={`${id}-priority-error`}>{errors.priority}</FieldError>
      </Field>
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
          {pending ? "Saving…" : rule ? "Save" : "Add rule"}
        </Button>
      </DialogFooter>
    </form>
  );
}
