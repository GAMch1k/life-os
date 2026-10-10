# Finance service

Finance is the owner's personal-finance tool: accounts and their balances,
every transaction, manual and cash entries, categories with simple rules, CSV
imports, bank syncs pushed by the banking integration service, monthly
summaries, and monthly budgets with their notifications. It keeps them in `finance_db` and is their only source of truth:
clients (the Dashboard's Finance workspace, MCP, the banking service) use its
API, never its database. It does not create Tasks or Calendar events. Gateway
base: `/api/finance`; the contract is `services/finance/api/openapi.yaml`;
`/health` and `/ready` are runtime probes.

Money is an integer amount in the currency's minor units (cents) with a
three-letter ISO currency; a negative transaction amount is money out, a
positive one money in. Every write needs `X-Life-OS-Actor` (see
[Activity](activity.md#the-actor-gate)). Errors use the shared envelope
`{"error": {"code", "message"}, "request_id"}`; the codes below are the
`code`.

## Accounts

An account has a `name` (unique; 409 `account_name_taken`), a `kind` (`bank`,
`cash`, `card`, `savings`, or `other`), a `currency`, and an opening balance.
`balance` is the opening balance plus every **booked** transaction; `pending`
is the sum of the pending ones, which are not in the balance;
`transactionCount` counts both. A bank account that reported a booked balance
shows the bank's balance instead, as of `balanceAsOf`, with the bank's
`available` balance next to it (see [Bank syncs](#bank-syncs)).

`GET /v1/accounts` lists the active accounts, the built-in Cash account first,
then by name (`includeArchived=true` adds archived ones); `POST /v1/accounts`
creates one (`name`, `currency`, optional `kind`, default `other`, and
`openingBalanceMinor`, default 0); `GET`/`PATCH /v1/accounts/{id}` read and
change one. `PATCH {"archived": true}` archives an account: it leaves the list
and takes no new transactions (409 `account_archived`), but keeps its history,
and `{"archived": false}` restores it. The currency changes only while the
account has no transactions (409 `account_has_transactions`).

**The built-in Cash account.** Finance keeps one account for cash spending
(`builtIn: true`, kind `cash`, stored with `system_key = 'cash'`). The service
ensures it when it starts, before it serves requests, and again before a
transaction that needs it; this is idempotent and safe to run concurrently (an
advisory lock). If the owner already has a manual account called Cash (any
case), that account becomes the built-in one; otherwise a new "Cash" account is
created in `FINANCE_DEFAULT_CURRENCY` (compose: `LIFE_OS_FINANCE_CURRENCY`,
default EUR), named "Cash (Life OS)" if a bank account already took "Cash". A
manual transaction without an `accountId` goes there. It can be renamed and
given another opening balance, but not archived or made another kind (409
`built_in_account`).

## Transactions

A transaction has an `accountId`, an `amount`, a `bookingDate` (the owner-local
date it counts on), an optional `valueDate`, a `status` (`booked` or
`pending`), optional `counterparty` (who was paid, or who paid), `description`
(what it was for, such as a bank's purpose text), and `notes` (the owner's
own), a category (below), and a `source`: `kind` `manual` (entered in Life
OS), `csv` (a CSV import), or `bank` (a bank sync), with the bank
`connectionId` and the `externalId` at its source. Bank transactions also keep
the bank's `merchantCategoryCode` (for later rules) and `bankReference`.

**Listing and search.** `GET /v1/transactions` returns every account's
transactions, latest `bookingDate` first, then latest created. Filters
combine: `q` (every word, case-insensitive, must occur in the counterparty,
description, or notes; at most 10 words), `accountId`, `categoryId` (an id or
`uncategorized`), `from`/`to` (booking dates, inclusive), `direction` (`in` or
`out`), `amountMin`/`amountMax` (absolute minor units: 1250 matches −12.50 and
12.50), `status`, and `source`. Paging is by keyset cursor: with more matches
than `limit` (1–100, default 50) the response has `nextCursor`, passed back as
`cursor` with the same filters; a malformed cursor is 400 `invalid_cursor`.

**Manual entries.** `POST /v1/transactions` records a manual transaction.
Only `amount` is required: without `accountId` it goes to the built-in Cash
account, without `bookingDate` it is dated today in the owner's timezone
(`LIFE_OS_TIMEZONE`), `status` defaults to booked, and without `categoryId`
the rules choose. The amount's currency must be the account's (400
`currency_mismatch`); an unknown account or category is 400 `unknown_account`
or `unknown_category`, an archived one 409, and a bank account 409
`managed_by_source`. An `Idempotency-Key` header makes the request safe to
retry: an identical repeat returns the original transaction, and the key with
another body is 409 `idempotency_conflict` (the key and a body hash are kept
with the row). `GET`, `PATCH`, and `DELETE /v1/transactions/{id}` read, change,
and delete one. `PATCH` changes only the fields present and `null` clears a
nullable one; an update that changes nothing changes nothing.

**Imported transactions are read-only.** A `csv` or `bank` transaction
(`readOnly: true`) takes only `categoryId` and `notes`; naming any other field,
and any `DELETE`, is 409 `managed_by_source`. A bank sync never changes them
either, so the owner's category and notes survive re-syncs.

## Categories and rules

A category has a `name` (unique per kind; 409 `category_exists`), a `kind`
(`expense`, `income`, or `transfer`: money moved between own accounts, which is
neither income nor spending), an optional `color` (`#RRGGBB`), and, for a
default category, a `systemKey` (below).
`GET`/`POST /v1/categories` and `PATCH /v1/categories/{id}` list, create, and
change them; `color: null` removes the color. `archived: true` archives one:
transactions keep it, but it cannot be given to transactions or rules any more
(409 `category_archived`) and its rules are skipped; `archived: false` restores
it.

Categories are chosen by hand, by **rules**, or for a card payment no rule
matches by its **merchant category code** (the owner decided against AI
categorization for now). A rule (`/v1/categories/rules`) gives a
category when its `match` fits: `field` `counterparty`, `description`, or
`any` (either), `operator` `contains` or `equals`, and `text`, compared
case-insensitively, ignoring surrounding and repeated spaces; for example
counterparty contains "REWE" → Groceries. Rules are tried by `priority`, lowest
first, then oldest first, and the first match wins; a new rule without a
priority comes after the owner's existing ones (the highest priority below
100000 plus 10), so before the default merchant rules.

- New manual, CSV, and bank transactions that come without a category get one
  from the rules (`categorizedBy: rule`, `categoryRuleId` names the rule).
- A bank transaction no rule matches that has a `merchantCategoryCode`
  (Deutsche Bank sends it with card payments) gets the default category of
  its code (`categorizedBy: mcc`, no rule), when that default category exists
  and is not archived. The standard code ranges map to the default
  categories: Groceries 5411, 5422, 5441, 5451, 5462, 5499; Eating out
  5812–5814; Transport 4111, 4112, 4121, 4131, 4784, 4789, 5541, 5542, 7523;
  Subscriptions 4899, 5815–5818; Utilities & phone 4812, 4814, 4900; Health &
  care 5122, 5912, 8011–8099; Fun & leisure 7832, 7922, 7991–7999; Shopping
  5200–5399, 5600–5699, 5732, 5734, 5940–5999. Other codes give none.
- A category chosen by hand (`categorizedBy: manual`: set in the Dashboard, by
  the AI, or by the import file, also when cleared with `null`) is never
  changed by a rule.
- When the counterparty or description of a transaction whose category was not
  chosen by hand changes (an edit or a bank re-sync, also when the bank's code
  changes), the rules and the code choose again.
- `POST /v1/categories/rules/apply` re-applies the rules to existing
  transactions: `{"scope": "uncategorized"}` (the default) to those without a
  category, `{"scope": "all"}` also to every transaction a rule categorized,
  which may get another category (`recategorized`) or none when no rule matches
  any more (`cleared`). It applies the merchant category code fallback too, and
  scope `all` re-checks what a code chose. It answers `examined`, `categorized`, `recategorized`,
  `cleared`, and `unchanged`. Deleting a rule leaves its categories in place
  until the rules are applied with scope `all`.

**Default categories and merchant rules.** When Finance starts the first time
with this feature it adds, once (recorded in `finance_seeds`), the budget's
starting categories, each with a stable `systemKey` that stays when the owner
renames it: Groceries, Eating out, Housing & rent, Utilities & phone,
Transport, Subscriptions, Shopping, Health & care, Fun & leisure, and Other
(expense); Savings and Transfers (transfer); Income (income). A default is
added only when no category has its name in any case. An existing category
with the same name and kind becomes the default one (only its system key is
set; its name, color, archive state, and transactions stay); one with the same
name but another kind is left alone and that default is skipped. The owner's
other categories and their transactions are never touched.

The same run adds common German merchants as ordinary, editable rules with
priorities from 100000, so the owner's rules always come first: Groceries
(counterparty contains REWE, EDEKA, Lidl, Aldi Süd, Aldi Sued, Aldi Nord,
Penny, Netto, Kaufland, dm-drogerie, dm drogerie, Rossmann; is exactly Aldi
or dm), Transport (Deutsche Bahn, DB Vertrieb, DB Fernverkehr, RMV, DADINA,
HEAG mobilo, Shell, Aral, Esso Station, Esso Tankstelle; is exactly Esso), and
Subscriptions (counterparty or description contains Netflix, Spotify,
Disney+, Disney Plus, Amazon Prime, YouTube Premium). Short names that occur
inside other words (dm, Aldi, Esso in "Espresso") match exactly or with more
of the name, and over-broad words such as Apple, Google, or Amazon alone are
not used. A default rule is skipped when the owner already has a rule with
the same match, or when its category was skipped. A deleted default rule does
not come back. The rules apply to new transactions; **Apply rules** applies
them to existing ones.

## CSV import

`POST /v1/imports/csv?accountId=…` imports a CSV file into the account picked
in the Dashboard, idempotently, and reports `imported`, `duplicates`,
`failed`, `categorized`, and each failed row with its reason. The columns,
amount and date formats, duplicate identity, and the older `account_id` format
are described in [Finance CSV import](../finance-csv-import.md).

## Bank syncs

Deutsche Bank (through the open-banking provider Enable Banking) is a separate
banking integration service. It never touches Finance's internals: after each
sync it pushes what it got through one Finance endpoint, as `system:banking`
on the private network (the gateway does not pass `system:` actors).

`POST /v1/imports/bank` takes the connection (`connectionId`, `provider`,
`institution`), the `window` of booking dates the sync covers, the
`accounts`, and the `transactions`, and applies them in one database
transaction (a failed request stores nothing; syncs of one connection run one
at a time):

- **Accounts** are upserted by (`connectionId`, `externalId`) as bank accounts
  (`source.kind` `bank`, with `provider`, `institution`, `ibanMasked`, and
  `syncedAt`). A new account takes the bank's name, kind (`bank`, `card`, or
  `savings`), and currency; when another account has the name, the institution
  (then the masked IBAN) is added. Later syncs refresh the reported booked and
  available balances (a balance the sync leaves out stays) but keep the
  owner's name, kind, and archive state. An account's currency never changes
  (400 `currency_mismatch`). Bank accounts take no manual transactions, CSV
  imports, or currency changes (409 `managed_by_source`).
- **Transactions** are upserted by (`connectionId`, `externalId`) as `bank`
  transactions. A re-sync updates the bank's fields (status, dates, amount,
  counterparty, description, merchant category code, bank reference), never
  the owner's category or notes. New ones without a category get one from the
  rules. Zero amounts are skipped.
- **Pending transactions.** A pending transaction that the bank books under
  the same id simply becomes booked. A new booked transaction that replaces a
  pending one under another id (same account and amount, the same counterparty
  ignoring case and spacing, booking dates at most 3 days apart, the pending
  one absent from the sync) removes the pending one and takes over its notes
  and a category chosen by hand. Pending transactions of the connection booked
  inside `window` that the sync no longer has are removed. Booked transactions
  are never deleted.
- **Errors.** A transaction naming an account that is neither in the sync nor
  already imported for the connection is 400 `unknown_account`; an amount or
  balance in another currency than its account is 400 `currency_mismatch`;
  duplicate external ids, an empty name, an unknown kind or status, or
  `window.from` after `window.to` are 400 `invalid_request`.

The answer maps each account's `externalId` to its Finance id (`accountIds`)
and counts `accounts.created`/`updated` and `transactions.created`/`updated`/
`unchanged`/`removedPending`/`skipped`. `DELETE /v1/imports/bank/{connectionId}`
removes a connection: it archives its accounts and keeps all their
transactions as history (204, also when there is nothing left to archive).

## Monthly summary

`GET /v1/summaries/monthly?month=YYYY-MM` sums the month's **booked**
transactions in one currency (`currency`, by default the built-in Cash
account's): `income`, `spending` (positive), and `net`. Transactions in a
`transfer` category are left out of those three. `totalBalance` is the current
balance of the accounts in that currency (archived ones included).
`spendingByCategory` lists the spending per category (not transfers or
uncategorized), largest first, and `byCategory` has every category with
transactions that month, uncategorized included (`categoryId` null), with its
`income`, `spending`, `net`, and `transactionCount`: the basis for budgets.
`accountId` limits everything to one account.

## Monthly budgets

A budget plans a month: the expected income, an amount per expense category
(envelope style), and a savings goal; what the plan leaves of the income is
`unallocated`. Amounts are minor units of the budget currency,
`FINANCE_DEFAULT_CURRENCY` (EUR live). Transactions in other currencies do
not count; `excludedCurrencies` says how many there were. Months are the
owner's (`LIFE_OS_TIMEZONE`).

**Plans.** One regular plan rolls forward from month to month: it applies from
the month it was saved for to every later month, until a later regular plan
starts. A month can have its own plan instead, without changing the regular
plan. `PUT /v1/budgets/{YYYY-MM}` saves a plan (`expectedIncomeMinor`,
`savingsGoalMinor`, and `lines` of `categoryId` and `amountMinor`; 0 leaves a
category out; lines must be expense categories, each once) with a `scope`:

- `month`: only this month gets this plan;
- `following`: it becomes the regular plan from this month on. Later months
  that had their own plan follow it too (their own plans are removed); earlier
  months keep the plan they had.

`DELETE /v1/budgets/{YYYY-MM}` removes a month's own plan, so it follows the
regular plan again (204, also when it had none). Carry-over is off: what a
category leaves unspent does not move to the next month.

**Progress.** `GET /v1/budgets/{YYYY-MM}` returns the plan that applies
(`plan`, null when none: `source` `month` or `regular`, `since`, the expected
income, savings goal, and `unallocated`) and the month's progress, live during
the month:

- **Spending** is money out in expense categories, **booked and pending**, less
  refunds (money in) in the same category, never below 0. Pending money counts
  so a warning comes early; it can still change or disappear, and each line
  says how much of it is pending. Transfers, the Savings category, and income
  are not spending. This differs on purpose from the monthly summary, which
  counts booked transactions only.
- `lines` lists the plan's categories by name with `planned`, `spent`,
  `pending`, `remaining` (negative when over), `percent` (rounded down), and
  `status` (`under`; `near` from 80%; `over` above 100%), then expense
  categories with spending the plan has no amount for (`unplanned`).
- `uncategorized` is money out without a category, its own line so nothing
  hides; it counts as spending.
- `totals` are the planned total, all spending (with its pending part), and
  what remains.
- `income` is money in, booked and pending, in income categories (less money
  out there) and without a category.
- `savings.saved` is the income minus the spending: `movedToSavings` (money
  moved out in the Savings category from accounts that are not savings
  accounts, less money moved back) plus the `leftover`; `percent` is against
  the savings goal.

**Suggestions.** `GET /v1/budgets/{YYYY-MM}/suggestions` suggests starting
amounts from real spending: per active expense category, the median of its
booked spending over the last three complete months before the month (never
later than the three before the current month), rounded up to a whole euro.
Only months with booked transactions in the budget currency count (bank data
covers about 90 days once connected); a month with history but no spending in
a category counts as 0. A category without spending in those months has
`suggested: null` (the Dashboard shows "no history yet"), and `months` is
empty when there is no history at all. `income` and `saved` are the medians of
the monthly income and of income minus spending.

**Notifications.** Finance sends budget notifications through Notifications
(`pkg/notify`, `NOTIFICATIONS_SERVICE_URL`), all at importance **low**:

| When | Category | Title, for example | Link | Dedupe key |
| --- | --- | --- | --- | --- |
| A category of the current month's plan reaches 80% of its amount | `finance.budget` | "Groceries budget at 85%" | `/finance?tab=budget` | `finance.budget:<YYYY-MM>:<categoryId>:80` |
| It reaches 100% ("… budget used up") or goes over ("Groceries is over budget") | `finance.budget` | "Eating out is over budget" | `/finance?tab=budget` | `finance.budget:<YYYY-MM>:<categoryId>:100` |
| The 1st of a month at 09:00 owner-local, for the previous month when it had a plan | `finance.budget.summary` | "October: spent €1,234.00 of €2,000.00, saved €500.00" | `/finance?tab=budget&month=<YYYY-MM>` | `finance.budget:<YYYY-MM>:summary` |

Each is sent once per category and month: a category that is already past
100% at the first check gets only the 100% one. The body gives the amounts,
what is left or over, and the pending part; the summary adds the savings goal,
what was moved to Savings, the categories over budget, and uncategorized
spending. A background loop checks every 5 minutes, at startup, and right
after a transaction, import, sync, rule application, category change, or plan
change. It claims each notification in `budget_notifications` before sending,
so a restart repeats nothing; a send that fails for now gives the claim back
and is retried at the next check, and the dedupe key makes a repeat harmless.
A summary due while Finance was down is still sent during the first week of
the month. Without `NOTIFICATIONS_SERVICE_URL` nothing is sent (logged at
startup).

## Activity

Finance records each change in [Activity](activity.md), attributed to the
request's actor (the owner, the AI, the banking service, or a development
tool) and tied to its correlation and causation:

| Type | Subject | Summary, for example |
| --- | --- | --- |
| `finance.account.created` / `.updated` / `.archived` / `.restored` | `finance_account` | "Account “Girokonto” created (bank, EUR)"; creating the Cash account at startup is `system:finance` |
| `finance.transaction.created` / `.updated` / `.deleted` | `finance_transaction` | "Spent €12.00 at Bäckerei (Cash)", "Transaction “ACME GmbH” (€2,500.00, Cash) updated: status, notes" |
| `finance.category.created` / `.updated` / `.archived` / `.restored` | `finance_category` | "Category “Groceries” created (expense)" |
| `finance.category_rule.created` / `.updated` / `.deleted` | `finance_category_rule` | "Category rule created: counterparty contains “REWE” → Groceries" |
| `finance.category_rules.applied` | `finance_category_rules` | "Category rules applied to uncategorized transactions: 12 categorized" |
| `finance.import.completed` / `.failed` | `finance_import` | "Imported 12 transactions into Girokonto (3 duplicates, 1 failed)" |
| `finance.bank_import.completed` | `finance_bank_connection` | "Deutsche Bank sync: 5 new, 1 updated, 2 pending removed (2 accounts)" |
| `finance.bank_connection.removed` | `finance_bank_connection` | "Bank connection to Deutsche Bank removed: 2 accounts archived" |
| `finance.budget.saved` | `finance_budget` (the month) | "Regular budget from October 2026 on saved: 9 categories, €2,100.00 planned, €400.00 savings goal; 1 later month's own budget replaced", "Budget for December 2026 saved: …" |
| `finance.budget.reset` | `finance_budget` | "Budget for December 2026 returned to the regular plan" |
| `finance.budget_defaults.added` | `finance_category_defaults` | "Default budget categories added: 12 created, 1 existing category used; 31 merchant rules added" (`system:finance`) |

Amounts are formatted in the transaction's currency ("€1,234.50", "12.00
CHF"). `data` holds the account, amount, booking date, status, source,
counterparty, description, and category id of a transaction, and the counts of
an import or sync; never the owner's notes, and never transaction details for
an import or a bank sync, which are one summary event each. Budget events
hold the month, scope, counts, and amounts only. Creates and
deletes have deterministic ids (an idempotent replay records nothing new);
updates are identified by the state they wrote; an update that changes nothing
records nothing. Recording needs `ACTIVITY_SERVICE_URL` (set in compose).

## Storage and operations

Migrations live in `services/finance/db/migrations` and run with the
`finance-migrate` container. `000004_finance_workspace` adds account kinds,
archive, the Cash system key, and the bank source identity; transaction
counterparty, description, status, value date, connection, and who chose the
category; category colors, archive, and the `transfer` kind; and
`category_rules`. Existing data is kept: accounts become kind `other`,
transactions stay booked, a CSV row stays a `csv` import (any other recorded
import source becomes `csv`), and an existing category counts as chosen by
hand; an import identity becomes unique per account. Its rollback folds
counterparty and description into the note, makes transfer categories expense
categories, and fails while two accounts share an imported external id.
`000005_bank_imports` adds the bank-reported account details and balances,
the merchant category code and bank reference, and makes a bank transaction id
unique per connection. `000006_budgets` adds the categories' system key, the
`mcc` value of `categorized_by`, `budget_plans` (scope `regular` or `month`,
one per month and scope) with `budget_plan_lines`, the
`budget_notifications` claims, and `finance_seeds`; existing rows are
unchanged. Its rollback keeps every category and rule (the defaults stay as
ordinary ones), turns a code's choice into a rule's (so applying the rules
with scope `all` re-checks it), and drops the plans and claims.

Configuration: `DATABASE_URL`, `ACTIVITY_SERVICE_URL`, `LIFE_OS_TIMEZONE`
(unset or unknown means UTC, logged at startup; budget months and the summary
time follow it), `FINANCE_DEFAULT_CURRENCY` (default EUR; also the budget
currency), and `NOTIFICATIONS_SERVICE_URL` (compose:
`http://notifications:8080`; empty sends no budget notifications). Tests: `make db-test SERVICE=finance`
with `FINANCE_TEST_DATABASE_URL` pointing at a throwaway Postgres runs the
integration tests, each in its own schema.
