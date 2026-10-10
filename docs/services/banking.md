# Banking service

Banking is the bank integration. It connects the owner's bank accounts through [Enable Banking](https://enablebanking.com/docs/), a licensed PSD2 provider, and pushes their balances and transactions into [Finance](finance.md). Gateway base: `/api/integrations/banking` (every integration gets `/api/integrations/<name>/`). The contract is `services/banking/api/openapi.yaml`; `/health` and `/ready` are available. Setup, reconnecting, and revoking are in [Banking operations](../operations/banking.md).

## Boundaries

- **Read-only.** Banking asks only for account information consent (balances and transactions; the Enable Banking application has the AIS service only). It never asks for payments.
- **Banking owns everything bank-side**: the provider adapter, the consent and the provider session, the sync schedule within the banks' limits, the consent reminders, and the reconnect. Its state lives in its own `banking_db`.
- **Finance never calls a bank, and Banking never touches Finance** beyond its bank import API: `POST /v1/imports/bank` for each sync and `DELETE /v1/imports/bank/{connectionId}` when the owner removes a connection (Finance's `importBankSync` and `removeBankConnection`), as `system:banking`. Banking does not read Finance's database or import its code; its client mirrors the contract's types (`internal/finance`).
- **Secrets stay in Banking.** The provider session id (the credential for reading the accounts) is stored only sealed (AES-256-GCM with `LIFE_OS_BANKING_TOKEN_KEY`, the approach of Calendar's Google refresh token) and wiped on disconnect. The session id, the application's private key, its JWTs, and full IBANs never appear in responses, logs, activity events, or notifications; accounts carry a masked IBAN (`DE89…3000`) and a hash of the IBAN as their id.

```mermaid
flowchart LR
  Owner["Owner (Dashboard, Finance page)"] -- "/api/integrations/banking" --> Gateway --> B[Banking / banking_db]
  B -- "JWT (RS256), PSU headers when the owner is present" --> EB[Enable Banking API] --> Bank[Deutsche Bank]
  B -- "POST /v1/imports/bank (system:banking)" --> F[Finance]
  B -- "pkg/notify: consent reminders" --> N[Notifications]
  B -. record .-> A[Activity]
```

## Connections

A connection is one consent at one bank (`bank_connections`):

| Status | Meaning |
| --- | --- |
| `pending` | Consented; the first sync has not succeeded yet (it is retried automatically). |
| `connected` | Syncing. |
| `expired` | The consent ran out, or the provider says the session ended (expired or revoked at the bank). Not synced again: reconnect. |
| `error` | The bank needs the owner to act (`ASPSP_PSU_ACTION_REQUIRED`), or the stored session no longer decrypts (the token key changed). Reconnect. |
| `disconnected` | The owner removed it. Kept as history (accounts, last sync), never synced again. |

There is at most one live (not disconnected) connection per bank (`provider`, `institution`, `country`). **Reconnecting** a live connection, after it expired, went into error, or while it is still valid, is the same consent flow: it renews the connection in place, keeps its id, so Finance keeps the same accounts (Finance keys bank accounts by connection id and account id), keeps its sync cursor and any import still waiting for Finance, and ends the replaced session at the provider. After a **disconnect** Finance has archived the accounts; connecting the bank again creates a new connection with new Finance accounts, and the archived ones keep their history.

Stored per connection: the consent's end (`consent_valid_until`), the sealed session id, the consented accounts (`[{uid, key, name, product, currency, cashAccountType, ibanMasked}]`: `uid` is the provider's per-session account id, `key` the stable id sent to Finance), the last attempt and last success, the next sync, failures in a row and the last error, the last sync's counts, the sync cursor, the day's unattended reads, the last owner-started sync, an import Finance did not take yet, whether Finance still has to archive the accounts, and which consent notice was sent. Pending authorizations (`bank_auth_states`) keep only the SHA-256 of the state, for 10 minutes, single use, with the connection a reconnect renews.

**Failed consents** (`bank_consent_failures`): when the bank or provider sends the owner back to the callback with an `error` instead of a `code` (for example Deutsche Bank's `server_error`), the Dashboard reports it and Banking keeps it durably, so the reason survives container restarts: when, the error, its description (whitespace collapsed, session-like ids and IBANs removed, at most 300 characters), the bank and connection of the authorization the state belonged to, and whether the state matched a pending authorization, had expired, or was unknown. The state is used up like a completed one; the state itself and any code are never stored or logged. The newest failure is listed as `lastConsentFailure` until a connection succeeds, which marks all of them cleared (they stay as history; the newest 100 are kept).

## API

Writes need `X-Life-OS-Actor` (the Dashboard sends `user`).

| Operation | Meaning |
| --- | --- |
| `GET /v1/connections` | Every connection (live first), whether banking is configured (`configured`, and `setupProblem` naming what is missing, never values), the schedule (`times`, `timeZone`, `unattendedPerDay`, `manualSyncMinutes`), and `lastConsentFailure` (`{at, error, description, institution, stateMatched}`, the newest consent the bank did not complete since a connection last succeeded, or null). |
| `POST /v1/connections/authorize` `{institution?, country?}` | Starts a consent (Deutsche Bank, DE by default) and returns the bank's page (`{url}`). The consent asks for balances and transactions, `psu_type` personal, valid as long as the bank allows (its `maximum_consent_validity`, at most 180 days, less 10 minutes), with the redirect URI `LIFE_OS_BANKING_REDIRECT_URI` (`https://lifeos.gamch1k.com/finance/bank/callback`). `422 unknown_bank` when the provider does not offer the bank. |
| `POST /v1/connections` `{code, state}` | The callback exchange (the Dashboard's `/finance/bank/callback` posts what the bank returned). Checks the state (`400 invalid_state` when unknown, used, or older than 10 minutes), creates the provider session (`400 bank_code_rejected`), stores it sealed, creates or renews the connection, and runs the first sync while the owner waits (owner-present, 90 days back, bounded to 45 s). A first sync that fails leaves the connection `pending` with `lastError`; the scheduler retries it. `422 no_accounts` when the consent covers no account. |
| `POST /v1/connections/consent-failure` `{state?, error, errorDescription?}` | The callback's report when the bank returned an error instead of a code: records it (above), uses the state up (an unknown or expired state is accepted and recorded as such), and records `banking.consent.failed`. Anything else in the body, such as a code, is ignored. `204`; `400 bad_request` without an error, or with an error over 100, a state over 256, or a description over 2000 characters. |
| `POST /v1/connections/{id}/sync` | "Sync now". From the owner (actor `user`) with the owner's address and browser, the read is owner-present and does not count against the daily limit. At most once every 10 minutes (`429 sync_throttled` with `Retry-After`). `409 sync_in_progress`, `409 connection_inactive` (expired, error, or disconnected), `429 daily_limit` (a non-owner sync with no unattended read left), `502 bank_unavailable` / `bank_expired` / `bank_refused` / `finance_unavailable` / `finance_refused` with a plain message. |
| `DELETE /v1/connections/{id}` | Disconnects: ends the session at the provider (which closes the bank consent where it can; one already ended is fine), wipes it, stops syncing, and asks Finance to archive the connection's accounts (keeping their transactions). When Finance is unavailable, the archive is retried by the scheduler. Idempotent. `204`. |

A connection in a response has its status, consent end, accounts (`id` = the stable key, name, product, currency, `kind` bank/card/savings, `ibanMasked`), last attempt and success, next sync (null unless live), failures in a row, last error, last counts, unattended reads today, `manualSyncAvailableAt`, and `waitingForFinance`. Never the session id or the provider's account uids.

### Owner presence (PSU headers)

PSD2 lets a bank refuse more than about four reads a day made without the account holder present. A read made while the owner uses Life OS carries PSU headers and is not counted: Banking sends `Psu-Ip-Address` and `Psu-User-Agent` (and `Psu-Accept-language` when known) only when the request comes from the owner (`X-Life-OS-Actor: user`) with an IP address in `X-Real-IP` (the gateway sets it from the visitor's address; through Cloudflare, from `CF-Connecting-IP`) and a `User-Agent`. A development tool (`dev:…`) is never owner-present, whatever it sends. When the bank refuses the headers (`PSU_HEADER_NOT_PROVIDED` / `PSU_HEADER_INVALID`), the sync falls back to an unattended read if one is left.

## Syncing

**Schedule.** The scheduler checks every connection once a minute (`BANKING_TICK_SECONDS`). Scheduled syncs run at `BANKING_SYNC_TIMES`, owner-local (`LIFE_OS_TIMEZONE`), by default `06:00,12:00,18:00,23:00`: at most four times, all within one local day.

**Four unattended reads a day.** Every read without the owner present (scheduled syncs, their retries, a non-owner "sync now") takes one of the day's reads (`BANKING_MAX_UNATTENDED_PER_DAY`, 1–4, default 4; the counter is per owner-local day and is taken atomically in the database before the bank is read). With none left, the next sync waits for the first sync time of the next day; nothing is read and nothing is lost.

**Window.** The first sync reads 90 days back (`BANKING_INITIAL_DAYS`). Later syncs start 7 days (`BANKING_OVERLAP_DAYS`) before the latest booking date of a booked transaction synced so far (the cursor), never further back than 90 days, up to today (dates in UTC, as the provider takes them). Every sync reads all consented accounts: balances, then every page of transactions (`continuation_key`, until none is returned). A bank that does not serve the whole window (`WRONG_TRANSACTIONS_PERIOD`) is asked again with `strategy=longest`.

**Never losing data.** The cursor moves only when Finance accepted a sync, so a failed sync is covered by the next one.

| Failure | What happens |
| --- | --- |
| The bank or provider fails (5xx, timeout, `ASPSP_ERROR`) | Retried after 15 minutes, doubling up to 2 hours, never later than the next sync time; each retry is an unattended read. |
| Too many reads (`429`, `ASPSP_RATE_LIMIT_EXCEEDED`) | The next attempt is the first sync time at least 6 hours later, as the provider recommends. |
| The application is refused (`UNAUTHORIZED_ACCESS`, `ACCESS_DENIED`) | Retried at the next sync time; check the application id and key. |
| The session ended (`EXPIRED_SESSION`, `REVOKED_SESSION`, `CLOSED_SESSION`) | The connection expires; the Status check needs attention, so the monitor tells the owner. |
| The bank needs the owner (`ASPSP_PSU_ACTION_REQUIRED`) | The connection goes into `error`: reconnect. |
| Finance unavailable (5xx, no answer) | What was read is kept (`pending_import`) and sent again after 1 minute, doubling up to 30, without reading the bank again. |
| Finance refuses the import (400 `currency_mismatch`, `unknown_account`, `invalid_request`) | Not retried in a loop: the import is dropped, the error recorded, and the next sync time reads again. |

### Mapping to Finance's bank import

One sync is one `POST /v1/imports/bank` (Finance applies it in one transaction): `connectionId` (the connection's id), `provider` `enablebanking`, `institution`, `window` (`from` = the first day read, `to` = today), every account, and every transaction read.

| Finance field | From Enable Banking |
| --- | --- |
| account `externalId` | `iban-` + SHA-256 of the normalised IBAN (never the IBAN), so reconnecting keeps the same Finance account; without an IBAN, a hash of the provider's `identification_hash` (stable across sessions) or of another account number; only as a last resort the per-session `uid`. |
| account `name` | `details`, else `product` (such as "Das Junge Konto"), else "<bank> account". The provider's `name` is the account holder and is never used. |
| account `kind` | `cash_account_type`: `CARD` card; `SVGS`, `LLSV`, `ONDP`, `MOMA` savings; otherwise bank. |
| `ibanMasked` | The IBAN's first and last four characters, `DE89…3000`. |
| `bookedBalance` | `CLBD` (else `ITBD`, `OPBD`); date `reference_date`, else the date of `last_change_date_time`, else today. |
| `availableBalance` | `ITAV` (else `CLAV`, `XPCD`, `OPAV`). Balances in another currency than the account are left out. |
| transaction `externalId` | `entry_reference`, which the provider documents as the stable reference (its `transaction_id` may change between reads), else `transaction_id`, else `h-` + a hash of the transaction's content. A reference the bank repeats within the sync gets `~` + a content hash, identical twins `~2`, `~3`; at most 200 characters. |
| `status` | `BOOK` booked; `PDNG`, `HOLD`, `OTHR` pending; `CNCL`, `RJCT`, `SCHD` are not sent. |
| `amount` | Minor units from the decimal string without floating point (the currency's ISO 4217 digits: cents for EUR), negative for `credit_debit_indicator` `DBIT`, positive for `CRDT`. A transaction in another currency than its account, or with an unreadable amount, is not sent (counted as ignored), so one odd transaction cannot block every sync. |
| `bookingDate` | `booking_date`, else `transaction_date`, else `value_date`, else today (pending transactions often have none). |
| `valueDate` | `value_date`. |
| `counterparty` | `creditor.name` for a debit, `debtor.name` for a credit. |
| `description` | `remittance_information` joined with spaces (else `note`), whitespace collapsed, at most 1000 characters. |
| `merchantCategoryCode`, `bankReference` | `merchant_category_code` (up to 8 characters), `reference_number`. |

Finance answers with its account ids and counts (`accounts.created/updated`, `transactions.created/updated/unchanged/removedPending/skipped`). Banking stores them as the connection's `lastCounts`, with how many transactions were read (`fetched`) and left out (`ignored`). Finance removes pending transactions of the connection booked inside `window` that the sync no longer has, and replaces a pending one by its booked transaction.

## Consent

A consent lasts as long as the bank allows (Deutsche Bank: 180 days). The owner hears of each consent event exactly once.

- **Before it ends, Banking reminds the owner** through [Notifications](notifications.md) (`pkg/notify`, importance `medium`, category `banking.consent`, link `/finance`, a dedupe key per connection, consent end, and reminder):
  - 7 days before `consent_valid_until`: "Deutsche Bank access expires on Sat 7 Apr 2027";
  - 1 day before: "Deutsche Bank access expires within a day".

  A reminder that fails to send is tried again at the next check; the dedupe key prevents doubles. Reconnecting starts the reminders over for the new consent. Meanwhile the Status page shows the coming end ("Deutsche Bank access ends on …") but the Bank connection check stays OK, so the [monitor](monitor.md) sends nothing.
- **When it has ended** (the connection becomes `expired`: the consent ran out, or the provider says the session ended or was revoked at the bank), Banking sends no notice of its own. The Bank connection check turns to Needs attention, so the monitor sends "Bank connection needs attention" (medium, link `/status`) with the check's summary and "Reconnect the bank in Finance …", and a low "Bank connection recovered" once the owner has reconnected. Banking records `banking.consent.expired` in Activity.

## Activity

Events (source `banking`, subject `bank_connection`), never with transaction details, amounts, counterparties, or account numbers:

| Type | When | Data |
| --- | --- | --- |
| `banking.connected` | A consent completed (also a reconnect) | institution, number of accounts, consent end, `reconnected` |
| `banking.disconnected` | The owner disconnected | institution, `endedAtProvider` |
| `banking.sync.completed` | A sync changed something in Finance, or succeeded after failures | Finance's counts, `fetched`, `ignored`, trigger (`connect`, `manual`, `scheduled`), window, `afterFailures` |
| `banking.sync.failed` | The first failure of a streak | institution, trigger, the plain error, the provider's or Finance's error code |
| `banking.consent.expiring` | The 7-day and 1-day notices | institution, notice, consent end |
| `banking.consent.expired` | The connection expired | institution, reason (`consent_ended`, `ended_at_bank`), consent end |
| `banking.consent.failed` | The bank returned a consent with an error instead of a code | the error and its description only (subject: the connection a reconnect was for, else `bank_consent` with the failure's id) |

Connect, disconnect, and owner-started syncs carry the owner as actor; scheduled work is `system:banking`.

## Configuration

| Variable | Meaning |
| --- | --- |
| `LIFE_OS_ENABLE_BANKING_APP_ID` | The Enable Banking application id (the JWT's `kid`). |
| `LIFE_OS_ENABLE_BANKING_KEY_FILE` | Inside the container, the application's private key (PEM): Compose bind-mounts the host file named by the same variable in `.env` read-only at `/run/secrets/enablebanking.pem` (an empty `/dev/null` when unset). |
| `LIFE_OS_BANKING_TOKEN_KEY` | 32 bytes in base64 (`openssl rand -base64 32`): seals the provider session id. A new key means reconnecting. |
| `LIFE_OS_BANKING_REDIRECT_URI` | The Dashboard callback registered on the application (default `https://lifeos.gamch1k.com/finance/bank/callback`; https, http only for localhost). |
| `ENABLE_BANKING_API_URL` | The provider's API (default `https://api.enablebanking.com`; the verification stack points it nowhere). |
| `FINANCE_SERVICE_URL`, `NOTIFICATIONS_SERVICE_URL`, `ACTIVITY_SERVICE_URL` | Finance (receives every sync), consent reminders, activity events. |
| `LIFE_OS_TIMEZONE` | The owner's timezone: sync times and the daily read counter follow it. |
| `BANKING_SYNC_TIMES` | Scheduled syncs, HH:MM, at most four (`.env`: `LIFE_OS_BANKING_SYNC_TIMES`). |
| `BANKING_MAX_UNATTENDED_PER_DAY`, `BANKING_MANUAL_SYNC_MINUTES`, `BANKING_INITIAL_DAYS`, `BANKING_OVERLAP_DAYS`, `BANKING_TICK_SECONDS` | 4, 10, 90, 7, 60. |

Not configured (no application id, key, or token key), Banking still runs: it lists connections, reports `configured: false` with the missing settings in `setupProblem`, sends consent reminders and expires ended consents of stored connections, disconnects, and refuses the rest with `503 banking_not_configured`. The JWT is RS256 with header `{typ: JWT, alg: RS256, kid: <app id>}` and claims `{iss: enablebanking.com, aud: api.enablebanking.com, iat, exp}`, valid for one hour and renewed 5 minutes before it expires.

## Tests

`internal/enablebanking/fakebank` is an in-memory Enable Banking API: it verifies every request's JWT against a test key and serves `/aspsps`, `/auth`, `/sessions`, balances, and paged transactions, with failures on demand (401/403/429/5xx, expired or revoked sessions, a limited history). `internal/finance/fakefinance` records the imports and removals. Integration tests (`BANKING_TEST_DATABASE_URL`, one schema per test) cover the connect flow with its state checks, the first sync's mapping, the incremental window, the four reads a day, owner-present sync with its throttle and PSU fallback, consent reminders and expiry, reconnecting, disconnecting with the archive retried, bank and Finance failures without data loss, and that no session id, IBAN, token, or transaction detail leaves through logs, activity, notifications, or responses. The browser test `apps/dashboard/e2e/bank-connection.spec.ts` stubs the banking API in the page; the verification stack never reaches a bank.
