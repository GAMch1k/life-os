# Finance service

Finance is the owner's personal-finance tool: accounts and their balances,
every transaction, manual and cash entries, categories with simple rules, CSV
imports, bank syncs pushed by the banking integration service, monthly
summaries, monthly budgets with their notifications, and subscriptions with
the money alerts (renewals, large debits, low balances). It keeps them in `finance_db` and is their only source of truth:
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

**Balances.** `balance` is the money the account has now, what the owner can
spend: `bookedBalance` plus `pending`.

- `bookedBalance` is the opening balance plus every **booked** transaction.
- A bank account that reported a booked balance (`bankBalance`, with the day
  it is for; see [Bank syncs](#bank-syncs)) starts from it instead: its
  booked balance is the bank's balance plus every booked transaction with a
  later booking date. A balance dated D includes all bookings through D,
  which holds for a bank's closing balance of a day (Deutsche Bank's) and
  for an interim balance dated today. Without one, a bank account's booked
  balance is computed like any other.
- `pending` is the sum of the pending transactions: mostly card payments the
  bank has not booked yet, so negative. When the bank books one, it moves
  from `pending` to `bookedBalance` and `balance` stays.
- `balanceAsOf` is the owner-local day of a bank account's last sync, the
  newest data in its balance; null for a manual account, whose balance is
  always current.
- `available` is the bank's own available balance, as it reported it.
  Finance does not use it: banks differ in what it holds back (some subtract
  card holds, some add the overdraft line), so adding pending payments to it
  could count them twice. The Dashboard does not show it.

For example, Friday's closing balance of €300.67, Monday's bookings of
−€49.62, and card holds of −€145.35 make a booked balance of €251.05 and a
balance of €105.70. `transactionCount` counts booked and pending
transactions. `lowBalance` is true while the low-balance alert is on and the
account is a watched bank account whose balance is below its threshold; Cash
and other manual accounts never are (see [Alerts](#alerts)).

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
manual transaction without an `accountId` goes there: also cash the AI records from a capture, such as "spent 12 euros at the bakery" or a photo of a cash receipt (Kassenbon, BAR; see [Letters and receipts](mcp.md#letters-and-receipts)). Card receipts are left to the bank connection. It can be renamed and
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
- A bank transaction no rule matches that has a `merchantCategoryCode` gets
  the default category of its code (`categorizedBy: mcc`, no rule), when that
  default category exists and is not archived. The standard code ranges map
  to the default categories: Groceries 5411, 5422, 5441, 5451, 5462, 5499;
  Eating out 5812–5814; Transport 4111, 4112, 4121, 4131, 4784, 4789, 5541,
  5542, 7523; Subscriptions 4899, 5815–5818; Utilities & phone 4812, 4814,
  4900; Health & care 5122, 5912, 8011–8099; Fun & leisure 7832, 7922,
  7991–7999; Shopping 5200–5399, 5600–5699, 5732, 5734, 5940–5999. Other
  codes give none. Deutsche Bank sends no code at all (none of the 182
  transactions of its first sync in October 2026 had one), so its card
  payments are categorized by the rules on the merchant, which the banking
  integration puts into the counterparty (see
  [Banking](banking.md#mapping-to-finances-bank-import)).
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
  are never deleted. A sync applies all of this at once, so a payment the
  bank books leaves the account's balance as it was (see
  [Accounts](#accounts)).
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
balance of the accounts in that currency (archived ones included), pending
money included: `totalBookedBalance` plus `totalPending`.
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

## Subscriptions

A subscription is a recurring payment. Finance finds them itself, in
deterministic code (no AI), and the owner confirms or ignores each one, or
marks a transaction as one.

**Detection.** Finance looks at booked money out of the last 800 days (any
account, archived ones too), leaving out transfer categories (money moved
between own accounts is not a payment), and groups it by merchant:

- A known service named in the counterparty or the description, as the
  default Subscriptions rules match: Netflix, Spotify, Disney+ (also Disney
  Plus), Amazon Prime (also Prime Video), YouTube Premium, iCloud, Apple
  Music, Google One, Audible, DAZN, Microsoft 365. So "PayPal Europe …" with
  "Ihr Einkauf bei Spotify AB", or a Google Play payment naming YouTube
  Premium, groups with the direct ones. Words match whole ("Inaudible" is not
  Audible).
- Else, for an intermediary counterparty (PayPal, Google Payment/Google
  Play/"GOOGLE *…", Apple/iTunes, Klarna) the merchant it paid: the card
  descriptor after "*" ("GOOGLE *Duolingo" → "Duolingo via Google"), else
  "Einkauf bei X" / "purchase at X" in the description ("Patreon via
  PayPal"), else the description's first words.
- Else the counterparty, compared without case, punctuation, words with
  digits (references, store numbers), and legal forms: "Stadtwerke Darmstadt
  GmbH" and "STADTWERKE DARMSTADT GMBH 4711" are one merchant.
- Else, with no counterparty, the description's first three words without
  digits, months, and SEPA terms ("Miete Oktober 2026 Wohnung 3" →
  "Miete Wohnung").

Within a merchant (in the currency of its latest payment), payments of
similar amounts (within 10% or 2 units of the currency, €2, whichever is
more) form a group, starting from the latest. A group is regular when its
latest payments follow each other at one cadence's interval: weekly 6–8
days, monthly 26–35, quarterly 84–98, yearly 350–380; at least three in a
row, or two for yearly (then with `confidence` low). A shorter gap ends the
run, so a café paid at random is not a subscription. A run whose next
payment is already more than a week late is not suggested. Each merchant
has at most one subscription per cadence. The typical amount is the median
of the last three payments; the next expected date is the last payment
plus the cadence (a week, or one, three, or twelve months; the 31st becomes
the month's last day).

Detection runs in the background loop (see [Alerts](#alerts)): at startup,
right after every transaction change, CSV import, bank sync, rule
application, account or setting change, and once a day. It stores each
candidate as a subscription with state `suggested`, the cadence, typical
amount, last and next date, account and category of the latest payment, and
the matched transactions. Later runs keep a suggestion up to date and remove
it when its payments are no longer regular. A **confirmed** subscription
follows its merchant's payments of a similar amount: the latest gives its
last and next date, the median of the last three its amount. An **ignored**
one is never touched again, and detection never suggests the same merchant
and cadence again. A name, amount, or category the owner set stays.

**Renewal.** A confirmed subscription's `renewal` is `upcoming` from the
renewal reminders' lead days before its next date (today included),
`overdue` up to a week after it without a payment, `missed` more than a week
late (shown, never alerted), and `scheduled` otherwise.

**API.** `GET /v1/subscriptions` lists them, suggested first, then
confirmed, then ignored (`state` filters), each with `amount`,
`monthlyAmount` (weekly × 52/12, quarterly ÷ 3, yearly ÷ 12), `transactionIds`
(latest first), `source` (`detected` or `manual`), and `totals`: the monthly
equivalent of the confirmed ones in the default currency, how many, and how
many in other currencies are left out. `POST /v1/subscriptions/{id}/confirm`
and `/ignore` change the state (ignored ones can be confirmed again; a
repeat changes nothing). `PATCH /v1/subscriptions/{id}` changes `name`,
`cadence` (moving the next date), `amountMinor`, and `categoryId` (`null`
removes it). `DELETE` removes one; its transactions stay, and detection may
suggest it again (ignore it instead to keep it away).

**This is a subscription.** `POST /v1/subscriptions` with a money-out
`transactionId` (400 `invalid_request` for money in or a transaction with
neither counterparty nor description, 400 `unknown_transaction`) makes a
confirmed subscription (`source` manual) named after the merchant, with the
transaction's amount and category, repeating at `cadence` (default
monthly); optional `name` and `categoryId`. It follows the merchant's other
similar payments at once (201). When the merchant already has a
subscription with this cadence, that one is confirmed (if it was not), takes
the transaction, and comes back with 200.

## Alerts

`GET /v1/settings` and `PUT /v1/settings` read and replace the alert
settings (every field required; amounts are minor units of
`FINANCE_DEFAULT_CURRENCY`):

| Setting | Default | Meaning |
| --- | --- | --- |
| `largeDebit` | on, €250 | Money out of at least `thresholdMinor` (at least 1), not in a transfer category |
| `lowBalance` | on, €100 | A bank account whose balance, pending payments included, is below `thresholdMinor` (0 or more) |
| `renewals` | on, 3 days | A reminder `leadDays` (0–30; 0 is on the day) before a confirmed subscription's next payment |

Only accounts and transactions in the default currency are checked. The
low-balance alert compares an account's `balance`, what the owner can spend:
the booked balance with the pending payments (see [Accounts](#accounts)). It
watches bank accounts only: active (not archived) accounts from a bank
connection of kind `bank` (with the bank's reported booked balance, or
their transactions before the bank reported one), and of kind `card` or
`savings` once they have a bank-reported balance. The built-in Cash account
and other manual accounts are never watched: cash spending is logged without
the withdrawals, so Cash goes negative by design. The large-debit alert leaves out money in a
`transfer`-kind category (Savings, Transfers, or any other transfer
category): moving money between the owner's own accounts is not a debit to
warn about.

**Notifications.** A background loop (every 5 minutes, at startup, and
right after a change) sends them through Notifications (`pkg/notify`):

| When | Category | Importance | Title, for example | Link | Dedupe key |
| --- | --- | --- | --- | --- | --- |
| Lead days before a confirmed subscription's next payment, until that day | `finance.subscription` | low | "Netflix renews in 3 days: €13.99" ("tomorrow", "today") | `/finance?tab=subscriptions` | `finance.subscription:<id>:<YYYY-MM-DD>` |
| A money-out transaction of at least the threshold, booked or pending, booked on one of the last three days or later, not in a transfer category | `finance.large_debit` | medium | "Large debit: €480.00 at MediaMarkt" | `/finance` | `finance.large_debit:<transactionId>` |
| A watched bank account falls below the threshold | `finance.low_balance` | medium | "Low balance: Girokonto at €84.20" | `/finance` | `finance.low_balance:<accountId>:<episode>` |

A renewal reminder's body says the date, the account, and, for another
cadence than monthly, the monthly cost ("Expected on Tue, 13 Oct 2026 from
Girokonto. Renews yearly, €7.83 a month."). A large debit is notified once
per transaction; only transactions booked in the last three days count, so
a first 90-day bank import does not notify old ones, and a booked bank
transaction that replaces a pending one keeps the pending one's claim (the
same debit is not notified twice). The category is looked at when the
alert is due: a transaction given a transfer category before that is left
out, and one already notified stays notified whatever its category
becomes. A low balance is notified once per
episode: an account that falls below starts a new episode, and one back at
or above the threshold is re-armed. A card hold the bank books keeps the
balance, so it keeps the episode too; a hold the shop releases gives the
money back and may re-arm the alert. The body splits the balance when some
of it is pending ("Below your €150.00 alert. Booked €251.05, pending -€145.35.").
Episodes are tracked while the alert is off too; switching it on while an
account is low notifies that episode. A
switched-off alert sends nothing; missed renewals are never alerted.

Each notification is claimed in `alert_notifications` before it is sent,
so a restart repeats nothing; a send that fails for now gives the claim back
and is retried at the next check, and one Notifications refuses (4xx) is not
retried. Without `NOTIFICATIONS_SERVICE_URL` nothing is sent, but
subscriptions are still detected.

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
| `finance.subscription.created` | `finance_subscription` | "“Urban Sports Club” marked as a subscription (€29.99 monthly)" |
| `finance.subscription.confirmed` / `.ignored` | `finance_subscription` | "Subscription “Netflix” confirmed (€13.99 monthly)", "Subscription “FitX” ignored" |
| `finance.subscription.updated` / `.deleted` | `finance_subscription` | "Subscription “iCloud+” updated: name, cadence, amount, category" |
| `finance.settings.updated` | `finance_settings` (`alerts`) | "Alert settings updated: large debits from €300.00, low balance below €100.00 (off), renewal reminders 1 day ahead" |

Amounts are formatted in the transaction's currency ("€1,234.50", "12.00
CHF"). `data` holds the account, amount, booking date, status, source,
counterparty, description, and category id of a transaction, and the counts of
an import or sync; never the owner's notes, and never transaction details for
an import or a bank sync, which are one summary event each. Budget events
hold the month, scope, counts, and amounts only. Detection's own
suggestions and the alerts it sends are not recorded. Creates and
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
`000007_subscriptions_alerts` adds `subscriptions` (identified by merchant
key and the cadence found, so an ignored one keeps its identity; the owner's
cadence, name, amount, and category beside it, with flags for what the owner
set) with `subscription_transactions`, the one-row `finance_settings` with
the defaults above, the `alert_notifications` claims, and
`low_balance_states` (each account's episode); existing rows are unchanged.
Its rollback drops those tables and keeps every transaction, account, and
category.

Configuration: `DATABASE_URL`, `ACTIVITY_SERVICE_URL`, `LIFE_OS_TIMEZONE`
(unset or unknown means UTC, logged at startup; budget months and the summary
time follow it), `FINANCE_DEFAULT_CURRENCY` (default EUR; also the budget
currency), and `NOTIFICATIONS_SERVICE_URL` (compose:
`http://notifications:8080`; empty sends no budget or alert notifications). Tests: `make db-test SERVICE=finance`
with `FINANCE_TEST_DATABASE_URL` pointing at a throwaway Postgres runs the
integration tests, each in its own schema.
