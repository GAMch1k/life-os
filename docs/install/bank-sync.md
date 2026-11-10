# Bank sync (Enable Banking)

Life OS can read your bank account's balances and transactions into Finance, four times a day. It reads only: it can never make a payment. It goes through [Enable Banking](https://enablebanking.com/), a licensed open-banking (PSD2) provider for banks in the EU and the EEA. You log in at your bank's own page, so Life OS never sees your banking password.

> [!NOTE]
> **Which banks.** In this preview, the **Connect** button in Finance is built and tested for one bank: **Deutsche Bank** (Germany). Enable Banking covers many more banks, but Life OS has not been tested with them, and Finance cannot choose another bank yet. Without bank sync, Finance still works with accounts you keep by hand and CSV imports.

## 1. Enable Banking

1. Sign up at [enablebanking.com](https://enablebanking.com/) and create an **application** in the control panel, in **production**, with **account information** only. For your own accounts, its "restricted" mode is enough: you link the accounts the application may read.
2. Its **redirect URL** is your Life OS address plus `/finance/bank/callback`, such as `https://lifeos.example.com/finance/bank/callback`. It must match exactly.
3. The control panel makes the application's key pair: download the private key (a `.pem` file) and copy it to the server, for example next to `setup.sh`. Make it private: `chmod 600 enablebanking.pem`.
4. **Link your accounts** to the application, as the control panel asks.

## 2. On the server

```sh
./setup.sh banking
./setup.sh up
```

It asks for the application ID and the key file, shows the redirect URL, and generates the key that encrypts the bank session in Life OS's database. Keep `.env` and the key file: they are not in the backups.

## 3. Connect

In Life OS: **Finance → Bank connection → Connect Deutsche Bank**. Log in at the bank and allow access. Finance then imports the last 90 days and, in the background, older history as far as the bank allows.

## The access ends after 180 days

Banks give access for at most 180 days. Life OS reminds you 7 days and 1 day before; then choose **Reconnect** in Finance and log in at the bank again. Your accounts and history stay. If it ends anyway, syncing stops until you reconnect, and nothing is lost if you reconnect within 90 days.

Banks allow about four reads a day without you; Life OS syncs at 06:00, 12:00, 18:00, and 23:00 (`LIFE_OS_BANKING_SYNC_TIMES`). **Sync now** in Finance doesn't count against that.

## Disconnect

**Disconnect** in Finance ends the access at the bank and keeps the transactions, archived. To remove it entirely, also delete the application in Enable Banking, the key file, and the `LIFE_OS_ENABLE_BANKING_*` lines in `.env`.

More, and troubleshooting: [Bank connection](../operations/banking.md).
