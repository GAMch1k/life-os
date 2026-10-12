# Bank connection (Enable Banking)

Life OS reads the owner's Deutsche Bank accounts (balances and transactions, never payments) through [Enable Banking](https://enablebanking.com/docs/), a licensed PSD2 provider, in its "restricted" production mode for the owner's own accounts. The [banking service](../services/banking.md) holds the consent and syncs into Finance. This page is the setup and the day-to-day operations.

## What the owner already did

1. **Created the Enable Banking application** in the Enable Banking control panel, in **production**, with the account information service (AIS) only, and the redirect URL `https://lifeos.gamch1k.com/finance/bank/callback`. The control panel generated the application's RSA key pair; the application id is the JWT's `kid`.
2. **Linked the accounts** the restricted application may read (the owner's own Deutsche Bank account, "Das Junge Konto").
3. **Put the private key on the server**, outside the repository, readable only by its owner (for example `chmod 0600`).

Never commit the key or the token key, and never paste them into tickets or logs.

## Setting it up on the server

1. In the ignored `.env`:

   ```sh
   LIFE_OS_BANKING_DB_PASSWORD=<a new random password>
   LIFE_OS_ENABLE_BANKING_APP_ID=<the application id>
   LIFE_OS_ENABLE_BANKING_KEY_FILE=/absolute/path/to/enablebanking.pem
   LIFE_OS_BANKING_TOKEN_KEY=<openssl rand -base64 32>
   LIFE_OS_BANKING_RUNTIME_UID=<id -u of the key file's owner>
   ```

   The banking container runs as `LIFE_OS_BANKING_RUNTIME_UID` (default 1000) so it can read the `0600` key, which Compose bind-mounts read-only at `/run/secrets/enablebanking.pem`. Keep the token key: it encrypts the stored bank session, and a new one means reconnecting. Optional: `LIFE_OS_BANKING_REDIRECT_URI` (must match the application's redirect URL exactly) `LIFE_OS_BANKING_SYNC_TIMES` (default `06:00,12:00,18:00,23:00`, owner-local, at most four), and `LIFE_OS_BANKING_CONSENT_DAYS` (default 180, at most the bank's maximum; lower it if the bank refuses a long consent).
2. Provision the database on an existing PostgreSQL volume (a fresh one does it by itself): recreate the PostgreSQL container so it sees the new password and the updated `infra/postgres/service-databases.conf`, then run the idempotent provisioning.

   ```sh
   docker compose up --detach postgres
   make db-provision
   ```

3. Start banking, then the Dashboard and the gateway (which routes `/api/integrations/banking/` and waits for banking):

   ```sh
   docker compose up --detach --build banking-migrate banking dashboard gateway
   ```

4. Check: `docker compose logs banking` says `bank connections configured` (or names the missing setting, never its value), and Status lists Banking as ready with a **Bank connection** check (Not set up until a bank is connected).

## Connecting

Open **Finance** and choose **Connect Deutsche Bank** on the Bank connection card. Life OS opens Deutsche Bank's consent page (through Enable Banking); log in, choose the account, and allow access. The bank returns to `/finance/bank/callback`, which completes the connection and imports the last 90 days, then shows Finance with "Deutsche Bank is connected." Older transactions follow in the background, as far as the bank shares them right after this login (up to two years, `BANKING_HISTORY_DAYS`): the card says "Loading older history from Deutsche Bank…" and then "Older history loaded … · 1,204 transactions" (or that the bank shared none). The card then lists the account with its masked IBAN, the last and next sync, how far back the transactions go ("Transactions since 12 Oct 2024"), and how long the access lasts (180 days). From then on banking syncs four times a day; **Sync now** reads at once (at most every 10 minutes; it does not count against the bank's daily limit because you are present).

If the callback says the consent expired or was already used, start again from Finance: a consent link is valid for 10 minutes and can be used once.

If the bank sends you back with an error instead (for example "The bank did not complete the consent (server_error)." with the bank's own description), the callback reports it to banking, which keeps it: the Bank connection card then shows it next to **Connect Deutsche Bank**, for example "Last attempt failed on 10 Oct, 20:19 CEST: server_error — …", until a connection succeeds. The Event Log has it as "Bank consent failed: server_error". See [Troubleshooting](#troubleshooting).

## When the access is about to end, or has ended

PSD2 consents end: Deutsche Bank's after 180 days. Seven days before, and again one day before, Life OS reminds you ("Deutsche Bank access expires on …", then "… expires within a day", both linking to Finance); the card shows **Expires soon**, and Status shows the coming end without raising an alert. When it has ended (or you revoke it at the bank), the connection is **Expired** and syncing stops; Status's Bank connection check needs attention, so you get one "Bank connection needs attention" notification (from the monitor, linking to Status), and "Bank connection recovered" after you reconnect.

To renew, choose **Reconnect** on the card and give consent again. Reconnecting keeps the same connection and the same Finance accounts and history; the next sync starts where the last one stopped (up to 90 days back), so nothing is missing if you reconnect within 90 days of the last sync. A Reconnect while the access is still valid ends the old consent.

**Load older history** on the card is a Reconnect for older transactions: banks share transactions older than 90 days only right after a login at the bank, so it explains that, then opens the bank's login; afterwards banking reads the older history again in the background. A history read that failed (the card shows why) changes nothing else: syncing and the last 90 days are not affected.

## Disconnecting and revoking

**Disconnect** on the card (it asks first) ends Life OS's session at Enable Banking, which closes the bank consent where the bank allows it, stops syncing, and archives the accounts in Finance with all their transactions. If Finance is down at that moment, the archive is retried automatically. Connecting again later creates new Finance accounts; the archived ones keep their history.

You can also revoke the consent at Deutsche Bank (online banking, third-party access) or in the Enable Banking control panel. Life OS notices at the next sync (the provider reports the session as revoked) and marks the connection Expired, and Status (through the monitor) tells you; then Disconnect or Reconnect.

To take Life OS's access away entirely, also remove the application or its key in the Enable Banking control panel, and delete the key file and the `LIFE_OS_ENABLE_BANKING_*` and `LIFE_OS_BANKING_TOKEN_KEY` lines from `.env` (`docker compose up --detach banking`). Banking then reports "not configured".

## Troubleshooting

| Symptom | Meaning and fix |
| --- | --- |
| Card: "Bank connections are not set up on this server" | Banking lacks a setting; the line under it (and the log) names which. A key file that cannot be read means its owner or mode does not match `LIFE_OS_BANKING_RUNTIME_UID`. |
| "Enable Banking refused this server's application (UNAUTHORIZED_ACCESS)" | Wrong application id, a key that does not belong to it, or the application is not active. Retried at each sync time. |
| "Deutsche Bank refused more reads for now" | The bank's daily limit for reads without you was reached (for example after retries). Banking waits at least 6 hours; Sync now from Finance still works. |
| "No more bank reads without you are allowed today" (a sync by a tool) | Four unattended reads were used today; the next scheduled sync is tomorrow morning. |
| "Finance could not be reached; the sync is kept and sent again shortly" | What was read waits in banking and is sent again within minutes, without reading the bank again. |
| "Finance refused the sync (400 …)" | Finance rejected the import; it is read again at the next sync time. Check Finance's log for the reason. |
| Status: "Syncing Deutsche Bank keeps failing: N attempts in a row" | The bank or provider keeps failing; transactions from the last good sync are kept, and retries continue. |
| "The stored bank session cannot be decrypted with LIFE_OS_BANKING_TOKEN_KEY" | The token key changed. Reconnect. |
| Card: "Last attempt failed on …: server_error — …" | Deutsche Bank or Enable Banking ended the consent with an error, so no code came back. Try again from a desktop browser (the bank's consent pages and its app hand-off can fail on a phone); on Enable Banking's page, check the Deutsche Bank ID you enter is the one you use for Deutsche Bank online banking; and if the bank refuses a long consent, lower `LIFE_OS_BANKING_CONSENT_DAYS` in `.env` (for example to 90) and run `docker compose up --detach banking`. Every attempt's reason is kept in banking (the card shows the newest; earlier ones are in the Event Log as "Bank consent failed"). |

Logs never contain the session id, the key, its tokens, or IBANs. Events in the Event Log (source Bank connection) show connects, disconnects, syncs that changed something (counts only), failures, consents the bank did not complete, and the consent's expiry.

## The verification stack never reaches the bank

`tools/verify-compose` blanks `LIFE_OS_ENABLE_BANKING_APP_ID`, `LIFE_OS_ENABLE_BANKING_KEY_FILE`, and `LIFE_OS_BANKING_TOKEN_KEY`; `compose.verify.yaml` also blanks them, mounts `/dev/null` instead of the key, and points banking at `http://enablebanking.invalid`; and `verify-compose` refuses any plan in which banking has an application id, a token key, or a key mount. Browser tests stub the banking API in the page.
