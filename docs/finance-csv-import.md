# Finance CSV import v1

`POST /v1/imports/csv` accepts UTF-8 CSV. The first row is a header; column
order is arbitrary. Required columns are `account_id`, `occurred_on`,
`amount_minor`, `currency`, and `external_id`. Optional columns are
`category_id` and `note`.

`account_id` and `category_id` are Life OS UUIDs. `occurred_on` uses
`YYYY-MM-DD`; `amount_minor` is a signed integer in the currency's minor unit;
and `currency` is a three-letter ISO-style code. Positive amounts are income,
negative amounts are spending.

Every row is stored with source `csv` and its `external_id`. Re-importing the
same source/external ID skips that row rather than creating a duplicate. The
response reports `created`, `skipped`, `failed`, and row-level errors.
