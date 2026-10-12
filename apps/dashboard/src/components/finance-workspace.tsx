"use client";

import { Bell, Plus } from "lucide-react";
import { useEffect, useRef, useState, useTransition } from "react";

import {
  AccountDialog,
  type AccountDialogState,
  FinanceAccounts,
} from "@/components/finance-accounts";
import { BankConnections } from "@/components/bank-connections";
import { AlertsDialog } from "@/components/finance-alerts-dialog";
import { FinanceBudget } from "@/components/finance-budget";
import {
  BudgetEditor,
  type BudgetEditorState,
} from "@/components/finance-budget-editor";
import { FinanceCategories } from "@/components/finance-categories";
import { FinanceImport } from "@/components/finance-import";
import { RuleOfferBanner } from "@/components/finance-rule-offer";
import { SubscriptionDialog } from "@/components/finance-subscription-dialog";
import { FinanceSubscriptions } from "@/components/finance-subscriptions";
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
import { financeClient } from "@/lib/api/finance-client";
import { type FinanceTab, budgetHref } from "@/lib/finance-budget";
import type { Subscription } from "@/lib/finance-subscriptions";
import { monthRange } from "@/lib/finance-summary";
import {
  type FinanceWorkspaceState,
  NO_FILTERS,
  type RuleOffer,
  type Transaction,
  type TransactionFilters,
  type TransactionPage,
  UNCATEGORIZED,
  accountCurrencies,
  fetchAccounts,
  fetchBudget,
  fetchRules,
  fetchSubscriptions,
  fetchSummary,
  fetchTransactions,
  financeErrorMessage,
  ruleOffer,
  upsert,
} from "@/lib/finance-workspace";

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
 * transaction (search, filters, add, edit), the monthly summary, the
 * monthly budget, and categories with their rules. The server renders the
 * first view; every change after that is read back from Finance in the
 * browser, so balances, lists, the summary, and the budget stay in step.
 * The tab and the budget's month are kept in the URL
 * (`/finance?tab=budget&month=2026-10`, `/finance?tab=subscriptions`), so
 * notifications can link there. The header's Alerts button opens the alert
 * settings.
 */
export function FinanceWorkspace({
  state,
  month,
  today,
  timeZone,
  initialTab = "transactions",
  budgetMonth = month,
}: {
  state: FinanceWorkspaceState;
  /** The zone the bank connection's times are shown in. */
  timeZone: string;
  /** The owner's current month (YYYY-MM). */
  month: string;
  /** The owner's today (YYYY-MM-DD), decided once by the server render. */
  today: string;
  /** The tab the URL named. */
  initialTab?: FinanceTab;
  /** The budget's month the URL named (YYYY-MM). */
  budgetMonth?: string;
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
        <BankConnections timeZone={timeZone} />
      </div>
    );
  return (
    <ReadyWorkspace
      budgetMonth={budgetMonth}
      initial={state}
      initialTab={initialTab}
      month={month}
      timeZone={timeZone}
      today={today}
    />
  );
}

function ReadyWorkspace({
  initial,
  initialTab,
  budgetMonth: firstBudgetMonth,
  month: thisMonth,
  today,
  timeZone,
}: {
  initial: Extract<FinanceWorkspaceState, { kind: "ready" }>;
  initialTab: FinanceTab;
  budgetMonth: string;
  month: string;
  today: string;
  timeZone: string;
}) {
  const [tab, setTab] = useState<FinanceTab>(initialTab);
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
  const [budgetMonth, setBudgetMonth] = useState(firstBudgetMonth);
  const [budget, setBudget] = useState(initial.budget);
  const [budgetLoading, setBudgetLoading] = useState(false);
  const [budgetEditor, setBudgetEditor] = useState<BudgetEditorState>(null);
  const [budgetPending, startBudgetChange] = useTransition();
  const [budgetActionError, setBudgetActionError] = useState<string>();
  const [subscriptions, setSubscriptions] = useState(initial.subscriptions);
  const [subscriptionsLoading, setSubscriptionsLoading] = useState(false);
  const [subscriptionPending, startSubscriptionChange] = useTransition();
  const [subscriptionError, setSubscriptionError] = useState<string>();
  const [editingSubscription, setEditingSubscription] =
    useState<Subscription | null>(null);
  const [settings, setSettings] = useState(initial.settings);
  const [alertsOpen, setAlertsOpen] = useState(false);
  const [transactionDialog, setTransactionDialog] =
    useState<TransactionDialogState>(null);
  const [accountDialog, setAccountDialog] = useState<AccountDialogState>(null);
  // After the owner corrects a category the AI chose: the offer to make a
  // rule of it.
  const [offer, setOffer] = useState<RuleOffer | null>(null);
  // The transaction request in flight (a newer one aborts it), the pending
  // search, and the latest summary request (older answers are dropped).
  const request = useRef<AbortController | null>(null);
  const searchTimer = useRef<number | undefined>(undefined);
  const summaryTicket = useRef(0);
  const budgetTicket = useRef(0);
  const subscriptionsTicket = useRef(0);

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

  const loadBudget = (month: string) => {
    const ticket = ++budgetTicket.current;
    setBudgetLoading(true);
    void fetchBudget(month).then((result) => {
      if (ticket !== budgetTicket.current) return;
      setBudget(result);
      setBudgetLoading(false);
    });
  };

  const loadSubscriptions = () => {
    const ticket = ++subscriptionsTicket.current;
    setSubscriptionsLoading(true);
    void fetchSubscriptions().then((result) => {
      if (ticket !== subscriptionsTicket.current) return;
      // A failed refresh keeps the list in view.
      setSubscriptions((current) =>
        result.error !== undefined && current.data !== undefined
          ? current
          : result,
      );
      setSubscriptionsLoading(false);
    });
  };

  /** Confirms or ignores a subscription, then reads the list (and total) back. */
  const changeSubscription = (
    subscription: Subscription,
    action: "confirm" | "ignore",
  ) =>
    startSubscriptionChange(async () => {
      setSubscriptionError(undefined);
      const params = { path: { subscriptionId: subscription.id } };
      let result: { data?: Subscription; error?: unknown } = {};
      try {
        result =
          action === "confirm"
            ? await financeClient.POST(
                "/v1/subscriptions/{subscriptionId}/confirm",
                { params },
              )
            : await financeClient.POST(
                "/v1/subscriptions/{subscriptionId}/ignore",
                { params },
              );
      } catch {
        result = {};
      }
      const { data, error } = result;
      if (!data) {
        setSubscriptionError(
          financeErrorMessage(
            error,
            action === "confirm"
              ? `Could not confirm ${subscription.name}.`
              : `Could not ignore ${subscription.name}.`,
          ),
        );
        return;
      }
      loadSubscriptions();
    });

  /** Keeps the tab and the budget's month in the URL. */
  const showInURL = (next: FinanceTab, month: string) => {
    const href =
      next === "budget"
        ? budgetHref(month === thisMonth ? undefined : month)
        : next === "transactions"
          ? "/finance"
          : `/finance?tab=${next}`;
    window.history.replaceState(null, "", href);
  };

  const changeTab = (next: FinanceTab) => {
    setTab(next);
    showInURL(next, budgetMonth);
    // Detection runs in the background after changes: read it fresh.
    if (next === "subscriptions") loadSubscriptions();
  };

  const changeBudgetMonth = (month: string) => {
    setBudgetMonth(month);
    setBudgetActionError(undefined);
    loadBudget(month);
    showInURL("budget", month);
  };

  const resetBudget = (month: string) =>
    startBudgetChange(async () => {
      setBudgetActionError(undefined);
      const { error, response } = await financeClient
        .DELETE("/v1/budgets/{month}", { params: { path: { month } } })
        .catch(() => ({ error: undefined, response: undefined }));
      if (!response?.ok) {
        setBudgetActionError(
          financeErrorMessage(error, "Could not return to the regular plan."),
        );
        return;
      }
      loadBudget(month);
    });

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
    loadBudget(budgetMonth);
    loadSubscriptions();
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
    changeTab("transactions");
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
    changeTab("transactions");
  };

  // The budget counts booked and pending money, so its category opens both.
  const showBudgetCategory = (categoryId: string | null) => {
    changeFilters({
      ...NO_FILTERS,
      ...monthRange(budgetMonth),
      categoryId: categoryId ?? UNCATEGORIZED,
    });
    changeTab("transactions");
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
          <Button onClick={() => setAlertsOpen(true)} variant="outline">
            <Bell aria-hidden="true" />
            Alerts
          </Button>
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

      <BankConnections
        onChanged={() => transactionsChanged(true)}
        timeZone={timeZone}
      />

      <Tabs
        className="gap-3"
        onValueChange={(value) => changeTab(value as FinanceTab)}
        value={tab}
      >
        <TabsList className="max-w-full overflow-x-auto">
          <TabsTrigger value="transactions">Transactions</TabsTrigger>
          <TabsTrigger value="summary">Summary</TabsTrigger>
          <TabsTrigger value="budget">Budget</TabsTrigger>
          <TabsTrigger value="subscriptions">Subscriptions</TabsTrigger>
          <TabsTrigger value="categories">Categories &amp; rules</TabsTrigger>
        </TabsList>
        <TabsContent value="transactions">
          {offer ? (
            <RuleOfferBanner
              key={`${offer.counterparty}|${offer.categoryId}`}
              offer={offer}
              onCreated={(rule) => {
                setRules((current) => upsert(current, rule));
                reloadRules();
                transactionsChanged(true);
              }}
              onDismiss={() => setOffer(null)}
            />
          ) : null}
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
        <TabsContent value="budget">
          <FinanceBudget
            actionError={budgetActionError}
            budget={budget}
            loading={budgetLoading}
            month={budgetMonth}
            onEdit={(data) => setBudgetEditor({ budget: data })}
            onMonthChange={changeBudgetMonth}
            onReset={resetBudget}
            onShowCategory={showBudgetCategory}
            pending={budgetPending}
            thisMonth={thisMonth}
            today={today}
          />
        </TabsContent>
        <TabsContent value="subscriptions">
          <FinanceSubscriptions
            actionError={subscriptionError}
            categories={categories}
            list={subscriptions}
            loading={subscriptionsLoading}
            onConfirm={(subscription) =>
              changeSubscription(subscription, "confirm")
            }
            onEdit={setEditingSubscription}
            onIgnore={(subscription) =>
              changeSubscription(subscription, "ignore")
            }
            pending={subscriptionPending}
            today={today}
          />
        </TabsContent>
        <TabsContent value="categories">
          <FinanceCategories
            categories={categories}
            categoriesError={initial.categories.error}
            onCategorySaved={(category) => {
              setCategories((current) => upsert(current, category));
              loadSummary(summaryMonth, currency);
              loadBudget(budgetMonth);
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
          const before =
            transactionDialog?.mode === "edit"
              ? transactionDialog.transaction
              : undefined;
          const next = ruleOffer(before, transaction, categories);
          if (next) setOffer(next);
          replaceInList(transaction);
          transactionsChanged(false);
        }}
        onSubscriptionSaved={() => loadSubscriptions()}
        state={transactionDialog}
        subscriptions={subscriptions.data?.items ?? []}
        today={today}
      />
      <SubscriptionDialog
        categories={categories}
        onClose={() => setEditingSubscription(null)}
        onDeleted={() => loadSubscriptions()}
        onSaved={() => loadSubscriptions()}
        subscription={editingSubscription}
      />
      <AlertsDialog
        onClose={() => setAlertsOpen(false)}
        onSaved={(saved) => {
          setSettings({ data: saved });
          // Low balances and the renewals' lead days follow the settings.
          refreshAccounts();
          loadSubscriptions();
        }}
        open={alertsOpen}
        settings={settings}
      />
      <BudgetEditor
        categories={categories}
        onClose={() => setBudgetEditor(null)}
        onSaved={(saved) => {
          // The answer is the month's budget with the new plan.
          budgetTicket.current++;
          setBudget({ data: saved });
          setBudgetLoading(false);
        }}
        state={budgetEditor}
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
