"use client";

import { Search, Sparkles, X } from "lucide-react";

import { DatePicker } from "@/components/date-picker";
import { Amount } from "@/components/finance-amount";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  NativeSelect,
  NativeSelectOptGroup,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  type Account,
  BY_AI,
  type Category,
  NO_FILTERS,
  SOURCE_LABELS,
  type Transaction,
  type TransactionFilters,
  UNCATEGORIZED,
  activeFilterCount,
  bookingDayLabel,
  byId,
  categoryGroups,
  chosenByAI,
  sourceBadge,
  transactionSubtitle,
  transactionTitle,
} from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { cn } from "@/lib/utils";

const EMPTY =
  "text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm";

/** The transaction list as the workspace holds it. */
export type TransactionList = {
  items: Transaction[];
  nextCursor: string | null;
  /** Why the list could not be loaded. */
  error?: string;
  /** The first page is loading (new filters, or after a change). */
  loading: boolean;
  /** "Show more" is loading the next page. */
  loadingMore: boolean;
  /** Why the next page could not be loaded. */
  moreError?: string;
};

/**
 * Every transaction, latest first, searchable and filterable by account,
 * category, dates, direction, status, and source. A row opens the
 * transaction; "Show more" loads the next page.
 */
export function FinanceTransactions({
  accounts,
  categories,
  filters,
  list,
  today,
  onFiltersChange,
  onOpen,
  onRetry,
  onShowMore,
}: {
  accounts: Account[];
  categories: Category[];
  filters: TransactionFilters;
  list: TransactionList;
  today: string;
  onFiltersChange: (patch: Partial<TransactionFilters>) => void;
  onOpen: (transaction: Transaction) => void;
  onRetry: () => void;
  onShowMore: () => void;
}) {
  const filtered = activeFilterCount(filters) > 0;
  return (
    <section aria-label="Transactions" className="grid gap-3">
      <TransactionFiltersBar
        accounts={accounts}
        categories={categories}
        filters={filters}
        onChange={onFiltersChange}
        today={today}
      />
      {list.error ? (
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-destructive text-sm" role="alert">
            {list.error}
          </p>
          <Button onClick={onRetry} size="xs" variant="outline">
            Try again
          </Button>
        </div>
      ) : list.loading && list.items.length === 0 ? (
        <div
          aria-busy="true"
          aria-label="Loading transactions"
          className="grid gap-2"
        >
          {[0, 1, 2].map((row) => (
            <Skeleton className="h-10 w-full" key={row} />
          ))}
        </div>
      ) : list.items.length === 0 ? (
        <div className={cn(EMPTY, "flex flex-wrap items-center gap-2")}>
          {filtered ? (
            <>
              No transactions match these filters.
              <Button
                onClick={() => onFiltersChange(NO_FILTERS)}
                size="xs"
                variant="ghost"
              >
                Clear filters
              </Button>
            </>
          ) : (
            "No transactions yet. Add one, or import a CSV from your bank."
          )}
        </div>
      ) : (
        <TransactionTable
          accounts={accounts}
          busy={list.loading}
          categories={categories}
          items={list.items}
          onOpen={onOpen}
          today={today}
        />
      )}
      {list.nextCursor && !list.error ? (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={list.loadingMore || list.loading}
            onClick={onShowMore}
            size="sm"
            variant="outline"
          >
            {list.loadingMore ? "Loading…" : "Show more"}
          </Button>
          {list.moreError ? (
            <p className="text-destructive text-sm" role="alert">
              {list.moreError}
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

function TransactionFiltersBar({
  accounts,
  categories,
  filters,
  today,
  onChange,
}: {
  accounts: Account[];
  categories: Category[];
  filters: TransactionFilters;
  today: string;
  onChange: (patch: Partial<TransactionFilters>) => void;
}) {
  const archivedCategories = categories.filter((category) => category.archived);
  return (
    <form
      aria-label="Filter transactions"
      className="flex flex-wrap items-center gap-2"
      onSubmit={(event) => event.preventDefault()}
      role="search"
    >
      <div className="relative w-full sm:w-64">
        <Search
          aria-hidden="true"
          className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2"
        />
        <Input
          aria-label="Search transactions"
          className="pl-8"
          maxLength={200}
          onChange={(event) => onChange({ q: event.target.value })}
          placeholder="Search counterparty, description, notes"
          type="search"
          value={filters.q}
        />
      </div>
      <NativeSelect
        aria-label="Account"
        onChange={(event) => onChange({ accountId: event.target.value })}
        value={filters.accountId}
      >
        <NativeSelectOption value="">All accounts</NativeSelectOption>
        {accounts.map((account) => (
          <NativeSelectOption key={account.id} value={account.id}>
            {account.name}
            {account.archived ? " (archived)" : ""}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      <NativeSelect
        aria-label="Category"
        onChange={(event) => onChange({ categoryId: event.target.value })}
        value={filters.categoryId}
      >
        <NativeSelectOption value="">All categories</NativeSelectOption>
        <NativeSelectOption value={UNCATEGORIZED}>
          Uncategorized
        </NativeSelectOption>
        <NativeSelectOption value={BY_AI}>Chosen by AI</NativeSelectOption>
        {categoryGroups(categories).map((group) => (
          <NativeSelectOptGroup key={group.kind} label={group.label}>
            {group.items.map((category) => (
              <NativeSelectOption key={category.id} value={category.id}>
                {category.name}
              </NativeSelectOption>
            ))}
          </NativeSelectOptGroup>
        ))}
        {archivedCategories.length ? (
          <NativeSelectOptGroup label="Archived">
            {archivedCategories.map((category) => (
              <NativeSelectOption key={category.id} value={category.id}>
                {category.name}
              </NativeSelectOption>
            ))}
          </NativeSelectOptGroup>
        ) : null}
      </NativeSelect>
      <DatePicker
        className="w-36"
        label="From date"
        max={filters.to || undefined}
        onChange={(from) => onChange({ from })}
        placeholder="From"
        today={today}
        value={filters.from}
      />
      <DatePicker
        className="w-36"
        label="To date"
        min={filters.from || undefined}
        onChange={(to) => onChange({ to })}
        placeholder="To"
        today={today}
        value={filters.to}
      />
      <NativeSelect
        aria-label="Direction"
        onChange={(event) =>
          onChange({
            direction: event.target.value as TransactionFilters["direction"],
          })
        }
        value={filters.direction}
      >
        <NativeSelectOption value="">In and out</NativeSelectOption>
        <NativeSelectOption value="out">Money out</NativeSelectOption>
        <NativeSelectOption value="in">Money in</NativeSelectOption>
      </NativeSelect>
      <NativeSelect
        aria-label="Status"
        onChange={(event) =>
          onChange({
            status: event.target.value as TransactionFilters["status"],
          })
        }
        value={filters.status}
      >
        <NativeSelectOption value="">Booked and pending</NativeSelectOption>
        <NativeSelectOption value="booked">Booked</NativeSelectOption>
        <NativeSelectOption value="pending">Pending</NativeSelectOption>
      </NativeSelect>
      <NativeSelect
        aria-label="Source"
        onChange={(event) =>
          onChange({
            source: event.target.value as TransactionFilters["source"],
          })
        }
        value={filters.source}
      >
        <NativeSelectOption value="">All sources</NativeSelectOption>
        {(["manual", "csv", "bank"] as const).map((source) => (
          <NativeSelectOption key={source} value={source}>
            {SOURCE_LABELS[source]}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      {activeFilterCount(filters) > 0 ? (
        <Button onClick={() => onChange(NO_FILTERS)} size="sm" variant="ghost">
          <X aria-hidden="true" />
          Clear filters
        </Button>
      ) : null}
    </form>
  );
}

function TransactionTable({
  accounts,
  busy,
  categories,
  items,
  today,
  onOpen,
}: {
  accounts: Account[];
  busy: boolean;
  categories: Category[];
  items: Transaction[];
  today: string;
  onOpen: (transaction: Transaction) => void;
}) {
  const locale = useBrowserLocale();
  const accountsById = byId(accounts);
  const categoriesById = byId(categories);
  return (
    <div
      aria-busy={busy}
      className={cn(
        "bg-card rounded-lg border transition-opacity",
        busy && "opacity-60",
      )}
    >
      <Table data-testid="finance-transactions">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="w-px pl-3">Date</TableHead>
            <TableHead>Transaction</TableHead>
            <TableHead className="hidden md:table-cell">Category</TableHead>
            <TableHead className="hidden lg:table-cell">Account</TableHead>
            <TableHead className="w-px pr-3 text-right">Amount</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((transaction) => {
            const title = transactionTitle(transaction);
            const subtitle = transactionSubtitle(transaction);
            const badge = sourceBadge(transaction);
            const category = transaction.categoryId
              ? categoriesById.get(transaction.categoryId)
              : undefined;
            const byAI = chosenByAI(transaction);
            const account = accountsById.get(transaction.accountId);
            const day = bookingDayLabel(transaction.bookingDate, today, locale);
            return (
              <TableRow
                className="cursor-pointer"
                data-testid="finance-transaction"
                key={transaction.id}
                onClick={() => onOpen(transaction)}
              >
                <TableCell className="text-muted-foreground w-px pl-3 align-top text-xs tabular-nums sm:align-middle">
                  <time dateTime={transaction.bookingDate}>{day}</time>
                </TableCell>
                <TableCell className="w-full max-w-0">
                  <button
                    className="focus-visible:ring-ring/50 block w-full min-w-0 rounded-sm text-left outline-none focus-visible:ring-3"
                    onClick={(event) => {
                      event.stopPropagation();
                      onOpen(transaction);
                    }}
                    type="button"
                  >
                    <span className="flex min-w-0 items-center gap-1.5">
                      <span className="truncate font-medium">{title}</span>
                      {transaction.status === "pending" ? (
                        <Badge variant="outline">Pending</Badge>
                      ) : null}
                      {badge ? (
                        <Badge
                          title={`Imported (${SOURCE_LABELS[transaction.source.kind]}): only the category and notes can change`}
                          variant="secondary"
                        >
                          {badge}
                        </Badge>
                      ) : null}
                    </span>
                    {subtitle ? (
                      <span className="text-muted-foreground block truncate text-xs">
                        {subtitle}
                      </span>
                    ) : null}
                    <span className="text-muted-foreground block truncate text-xs md:hidden">
                      {category?.name ?? "Uncategorized"}
                      {byAI ? " (by AI)" : ""}
                      {account ? ` · ${account.name}` : ""}
                    </span>
                  </button>
                </TableCell>
                <TableCell className="hidden max-w-40 md:table-cell">
                  <CategoryName byAI={byAI} category={category} />
                </TableCell>
                <TableCell className="text-muted-foreground hidden max-w-40 truncate lg:table-cell">
                  {account?.name ?? "Unknown account"}
                </TableCell>
                <TableCell className="w-px pr-3 text-right align-top font-medium sm:align-middle">
                  <Amount money={transaction.amount} signed />
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

/**
 * A category with its color dot, or "Uncategorized"; `byAI` adds the
 * subtle mark of a category the AI chose.
 */
export function CategoryName({
  category,
  byAI = false,
}: {
  category?: Category;
  byAI?: boolean;
}) {
  if (!category)
    return <span className="text-muted-foreground">Uncategorized</span>;
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span
        aria-hidden="true"
        className="bg-muted-foreground/40 size-2 shrink-0 rounded-full"
        style={category.color ? { backgroundColor: category.color } : undefined}
      />
      <span className="truncate">{category.name}</span>
      {byAI ? <AIMark /> : null}
    </span>
  );
}

/** Marks a category the AI chose; hovering says how to correct it. */
export function AIMark() {
  return (
    <span
      className="text-muted-foreground inline-flex shrink-0 items-center"
      data-testid="finance-ai-mark"
      title="Chosen by AI. Pick another category to correct it."
    >
      <Sparkles aria-hidden="true" className="size-3.5" />
      <span className="sr-only">by AI</span>
    </span>
  );
}
