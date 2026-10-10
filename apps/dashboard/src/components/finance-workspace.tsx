"use client";

import { Plus } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import {
  AccountDialog,
  type AccountDialogState,
  FinanceAccounts,
} from "@/components/finance-accounts";
import { FinanceCategories } from "@/components/finance-categories";
import { FinanceImport } from "@/components/finance-import";
import { FinanceSummary } from "@/components/finance-summary";
import {
  TransactionDialog,
  type TransactionDialogState,
} from "@/components/finance-transaction-dialog";
import {
  FinanceTransactions,
  type TransactionList,
} from "@/components/finance-transactions";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { monthRange } from "@/lib/finance-summary";
import {
  type FinanceWorkspaceState,
  NO_FILTERS,
  type Transaction,
  type TransactionFilters,
  type TransactionPage,
  UNCATEGORIZED,
  accountCurrencies,
  fetchAccounts,
  fetchRules,
  fetchSummary,
  fetchTransactions,
  upsert,
} from "@/lib/finance-workspace";

type Tab = "transactions" | "summary" | "categories";

/** Waits this long after the last keystroke before searching. */
const SEARCH_DELAY_MS = 250;

/** The list state for a loaded page. */
function listOf(page: TransactionPage): TransactionList {
  return { ...page, loading: false, loadingMore: false };
}

function failedList(error: string): TransactionList {
  return {
    items: [],
    nextCursor: null,
    error,
    loading: false,
    loadingMore: false,
  };
}

/**
 * The Finance workspace: accounts with their balances, then tabs for every
 * transaction (search, filters, add, edit), the monthly summary, and
 * categories with their rules. The server renders the first view; every
 * change after that is read back from Finance in the browser, so balances,
 * lists, and the summary stay in step.
 */
export function FinanceWorkspace({
  state,
  month,
  today,
}: {
  state: FinanceWorkspaceState;
  /** The owner's current month (YYYY-MM). */
  month: string;
  /** The owner's today (YYYY-MM-DD), decided once by the server render. */
  today: string;
}) {
  if (state.kind === "unavailable")
    return (
      <div className="mx-auto max-w-6xl space-y-4">
        <h1 className="text-xl font-semibold tracking-tight">Finance</h1>
        <Card>
          <CardHeader>
            <CardTitle>Finance is unavailable</CardTitle>
            <CardDescription>
              Finance is unavailable right now ({state.error.replace(/\.$/, "")}
              ). Your data has not been replaced with placeholders.
            </CardDescription>
          </CardHeader>
        </Card>
      </div>
    );
  return <ReadyWorkspace initial={state} month={month} today={today} />;
}

function ReadyWorkspace({
  initial,
  month: thisMonth,
  today,
}: {
  initial: Extract<FinanceWorkspaceState, { kind: "ready" }>;
  month: string;
  today: string;
}) {
  const [tab, setTab] = useState<Tab>("transactions");
  const [accounts, setAccounts] = useState(initial.accounts);
  const [categories, setCategories] = useState(initial.categories.data ?? []);
  const [rules, setRules] = useState(initial.rules.data ?? []);
  const [filters, setFilters] = useState(NO_FILTERS);
  const [list, setList] = useState<TransactionList>(() =>
    initial.transactions.error !== undefined
      ? failedList(initial.transactions.error)
      : listOf(initial.transactions.data),
  );
  const [summaryMonth, setSummaryMonth] = useState(thisMonth);
  const [currency, setCurrency] = useState("");
  const [summary, setSummary] = useState(initial.summary);
  const [summaryLoading, setSummaryLoading] = useState(false);
  const [transactionDialog, setTransactionDialog] =
    useState<TransactionDialogState>(null);
  const [accountDialog, setAccountDialog] = useState<AccountDialogState>(null);
  // The transaction request in flight (a newer one aborts it), the pending
  // search, and the latest summary request (older answers are dropped).
  const request = useRef<AbortController | null>(null);
  const searchTimer = useRef<number | undefined>(undefined);
  const summaryTicket = useRef(0);

  useEffect(
    () => () => {
      window.clearTimeout(searchTimer.current);
      request.current?.abort();
    },
    [],
  );

  const loadTransactions = (next: TransactionFilters) => {
    window.clearTimeout(searchTimer.current);
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setList((current) => ({
      ...current,
      loading: true,
      error: undefined,
      moreError: undefined,
    }));
    void fetchTransactions(next, undefined, controller.signal).then(
      (result) => {
        if (controller.signal.aborted) return;
        setList(
          result.error !== undefined
            ? failedList(result.error)
            : listOf(result.data),
        );
      },
    );
  };

  const changeFilters = (patch: Partial<TransactionFilters>) => {
    const next = { ...filters, ...patch };
    setFilters(next);
    // Typing waits for a pause; every other filter applies at once.
    if (Object.keys(patch).length === 1 && "q" in patch) {
      window.clearTimeout(searchTimer.current);
      searchTimer.current = window.setTimeout(
        () => loadTransactions(next),
        SEARCH_DELAY_MS,
      );
    } else loadTransactions(next);
  };

  const showMore = () => {
    const cursor = list.nextCursor;
    if (!cursor) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setList((current) => ({
      ...current,
      loadingMore: true,
      moreError: undefined,
    }));
    void fetchTransactions(filters, cursor, controller.signal).then(
      (result) => {
        if (controller.signal.aborted) return;
        setList((current) => {
          if (result.error !== undefined)
            return { ...current, loadingMore: false, moreError: result.error };
          const known = new Set(current.items.map((item) => item.id));
          return {
            ...current,
            items: [
              ...current.items,
              ...result.data.items.filter((item) => !known.has(item.id)),
            ],
            nextCursor: result.data.nextCursor,
            loadingMore: false,
          };
        });
      },
    );
  };

  const loadSummary = (month: string, code: string) => {
    const ticket = ++summaryTicket.current;
    setSummaryLoading(true);
    void fetchSummary(month, code || undefined).then((result) => {
      if (ticket !== summaryTicket.current) return;
      setSummary(result);
      setSummaryLoading(false);
    });
  };

  const refreshAccounts = () =>
    void fetchAccounts().then((result) => {
      if (result.error === undefined) setAccounts(result.data);
    });

  /**
   * Transactions changed: balances and the summary follow, and the list is
   * read again unless the change was already applied to it in place.
   */
  const transactionsChanged = (reload: boolean) => {
    refreshAccounts();
    loadSummary(summaryMonth, currency);
    if (reload) loadTransactions(filters);
  };

  const replaceInList = (transaction: Transaction) =>
    setList((current) => ({
      ...current,
      items: current.items.map((item) =>
        item.id === transaction.id ? transaction : item,
      ),
    }));

  const removeFromList = (transaction: Transaction) =>
    setList((current) => ({
      ...current,
      items: current.items.filter((item) => item.id !== transaction.id),
    }));

  const showAccount = (accountId: string) => {
    changeFilters({ accountId });
    setTab("transactions");
  };

  // The summary's numbers are booked transactions of the month, so its
  // category opens exactly those.
  const showCategory = (categoryId: string | null) => {
    changeFilters({
      ...NO_FILTERS,
      ...monthRange(summaryMonth),
      categoryId: categoryId ?? UNCATEGORIZED,
      status: "booked",
    });
    setTab("transactions");
  };

  const reloadRules = () =>
    void fetchRules().then((result) => {
      if (result.error === undefined) setRules(result.data);
    });

  return (
    <div
      className="mx-auto max-w-6xl space-y-4"
      data-testid="finance-workspace"
      data-today={today}
    >
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold tracking-tight">Finance</h1>
        <div className="flex flex-wrap gap-2">
          <FinanceImport
            accounts={accounts}
            onImported={() => transactionsChanged(true)}
          />
          <Button
            onClick={() =>
              setTransactionDialog({
                mode: "new",
                accountId: filters.accountId || undefined,
              })
            }
          >
            <Plus aria-hidden="true" />
            Add transaction
          </Button>
        </div>
      </header>

      <FinanceAccounts
        accounts={accounts}
        onEdit={setAccountDialog}
        onSelect={showAccount}
        selectedId={filters.accountId}
      />

      <Tabs
        className="gap-3"
        onValueChange={(value) => setTab(value as Tab)}
        value={tab}
      >
        <TabsList className="max-w-full overflow-x-auto">
          <TabsTrigger value="transactions">Transactions</TabsTrigger>
          <TabsTrigger value="summary">Summary</TabsTrigger>
          <TabsTrigger value="categories">Categories &amp; rules</TabsTrigger>
        </TabsList>
        <TabsContent value="transactions">
          <FinanceTransactions
            accounts={accounts}
            categories={categories}
            filters={filters}
            list={list}
            onFiltersChange={changeFilters}
            onOpen={(transaction) =>
              setTransactionDialog({ mode: "edit", transaction })
            }
            onRetry={() => loadTransactions(filters)}
            onShowMore={showMore}
            today={today}
          />
        </TabsContent>
        <TabsContent value="summary">
          <FinanceSummary
            categories={categories}
            currencies={accountCurrencies(accounts)}
            currency={currency}
            loading={summaryLoading}
            month={summaryMonth}
            onCurrencyChange={(code) => {
              setCurrency(code);
              loadSummary(summaryMonth, code);
            }}
            onMonthChange={(month) => {
              setSummaryMonth(month);
              loadSummary(month, currency);
            }}
            onShowCategory={showCategory}
            summary={summary}
            thisMonth={thisMonth}
          />
        </TabsContent>
        <TabsContent value="categories">
          <FinanceCategories
            categories={categories}
            categoriesError={initial.categories.error}
            onCategorySaved={(category) => {
              setCategories((current) => upsert(current, category));
              loadSummary(summaryMonth, currency);
            }}
            onRuleDeleted={(rule) =>
              setRules((current) =>
                current.filter((item) => item.id !== rule.id),
              )
            }
            onRuleSaved={(rule) => {
              setRules((current) => upsert(current, rule));
              // The list's order is the rules' priority; read it back.
              reloadRules();
            }}
            onRulesApplied={() => transactionsChanged(true)}
            rules={rules}
            rulesError={initial.rules.error}
          />
        </TabsContent>
      </Tabs>

      <TransactionDialog
        accounts={accounts}
        categories={categories}
        onClose={() => setTransactionDialog(null)}
        onCreated={() => transactionsChanged(true)}
        onDeleted={(transaction) => {
          removeFromList(transaction);
          transactionsChanged(false);
        }}
        onSaved={(transaction) => {
          replaceInList(transaction);
          transactionsChanged(false);
        }}
        state={transactionDialog}
        today={today}
      />
      <AccountDialog
        accounts={accounts}
        onClose={() => setAccountDialog(null)}
        onSaved={(account) => {
          setAccounts((current) => upsert(current, account));
          loadSummary(summaryMonth, currency);
        }}
        state={accountDialog}
      />
    </div>
  );
}
