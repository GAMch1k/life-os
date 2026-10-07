# Finance CSV import

The Finance dashboard imports a UTF-8 CSV file through `POST /v1/imports/csv`.
The endpoint persists valid rows immediately and returns the created, skipped,
failed, and row-error counts used by the dashboard.

## Required columns

| Column | Meaning |
| --- | --- |
| `account_id` | UUID of an existing Finance account. |
| `occurred_on` | Transaction date in `YYYY-MM-DD`. |
| `amount_minor` | Non-zero integer in the currency's minor units; negative is spending. |
| `currency` | Three-letter ISO currency code. |
| `external_id` | Stable bank/source row ID used for idempotency. |

`category_id` and `note` are optional. Column order does not matter.

```csv
account_id,occurred_on,amount_minor,currency,external_id,category_id,note
11111111-1111-1111-1111-111111111111,2026-10-07,-1250,USD,bank-row-42,,Coffee
```

Re-importing the same `external_id` does not create a second transaction: the
result counts it as `skipped`. Invalid rows are retained as row-numbered errors
in the import response; valid rows in the same file are still imported.
