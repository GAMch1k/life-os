# Finance CSV import

`POST /v1/imports/csv?accountId=<uuid>` imports a CSV file into one Finance
account, the one the owner picks in the Dashboard's Finance workspace. The body
is the file itself (`content-type: text/csv`, at most 4 MB, the Finance
service's request body limit; split a larger export into several files). Rows
are stored right away and the response reports what happened:

```json
{"id": "…", "accountId": "…", "status": "completed", "imported": 12, "duplicates": 3, "failed": 1, "categorized": 7,
 "errors": [{"row": 9, "message": "date must be YYYY-MM-DD or DD.MM.YYYY"}]}
```

`imported` counts new transactions, `duplicates` rows the account already
had, `categorized` imported rows a category rule categorized, and each failed
row is listed with its line number (the header is row 1; at most 200 errors are
listed, `failed` stays complete). Valid rows are imported even when others
fail; `status` is `failed` only when nothing was imported and some rows failed.

Imported rows are `csv` transactions: booked, and read-only except their
category and notes (see [Finance](services/finance.md#transactions)). The
account must exist (400 `unknown_account`), be active (409 `account_archived`),
and not be a bank account (409 `managed_by_source`; its transactions come from
the bank).

## The file

UTF-8, with or without a byte-order mark; comma- or semicolon-separated (the
header line decides). Columns are matched by header name, case-insensitively
and in any order; spaces and hyphens in names count as underscores, and unknown
columns are ignored.

| Column | Meaning |
| --- | --- |
| `date` | Required. `YYYY-MM-DD` or `DD.MM.YYYY`. Also accepted as `booking_date` or `occurred_on`. |
| `amount` | Signed decimal in major units, at most two decimals (three for currencies such as KWD, none for JPY): `-12.50`, `-12,50`, `1.234,56`, `1,234.56`, `12,50-`. The last `.` or `,` is the decimal mark when both occur, or when it occurs once followed by one or two digits; otherwise the marks group thousands (`1,234` is 1234). |
| `amount_minor` | Signed integer in minor units (`-1250`), instead of `amount`. One of the two is required; with both, `amount_minor` wins. |
| `currency` | Optional. Must be the account's currency, which it defaults to. |
| `value_date` | Optional, like `date`. |
| `counterparty` | Who was paid, or who paid. |
| `description` | What it was for (a bank's purpose text). |
| `notes` | The owner's notes (also `note`). |
| `category` | A category name, case-insensitive. An unknown or archived name leaves the choice to the rules. |
| `category_id` | A category id instead (an unknown id fails the row). |
| `external_id` | Optional stable id of the row at its source. |

A row with a category (by name or id) counts as categorized by hand; a row
without one gets one from the [category rules](services/finance.md#categories-and-rules).

```csv
date;amount;counterparty;description;category;notes
05.10.2026;-23,45;REWE Markt;Einkauf;;weekly shop
01.10.2026;-950,00;Hausverwaltung;Miete Oktober;Rent;
```

## Duplicates

Re-importing is idempotent. A row is a duplicate when the account already has
its `external_id`; a row without one is identified by its content (date,
amount, currency, counterparty, description) and its position among identical
rows of the file, so two equal coffees on one day are two transactions and
importing the same file again finds both. Identity is per account: two
accounts' files may use the same ids.

## The older format

Files written for the first importer still work without `accountId`: every row
names its account in an `account_id` column.

```csv
account_id,occurred_on,amount_minor,currency,external_id,category_id,note
11111111-1111-1111-1111-111111111111,2026-10-07,-1250,USD,bank-row-42,,Coffee
```

With `accountId` given as well, a row naming another account fails. Without
`accountId` and without an `account_id` column the request is refused (400
`account_required`); a file without a `date` or amount column is 400
`invalid_csv`.
