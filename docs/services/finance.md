# Finance service

Finance records accounts, categories, transactions, CSV imports, and monthly summaries in `finance_db`. It exists to make financial history and current position understandable; it does not create Tasks or Calendar events. It depends on PostgreSQL and the shared Go runtime. Its gateway base is `/api/finance`.

Accounts are balance containers. Categories are `income` or `expense`. A transaction has account, optional category, integer minor-unit Money, date, and note. `totalBalance` is cumulative account position; a monthly result (`income`, `spending`, `net`) covers the requested month only.

Manual entry posts a validated transaction. CSV import accepts `text/csv`, maps supported headers/formats, validates rows, and reports `created`, `skipped`, `failed`, and row errors. Re-importing matching data is idempotent: duplicates are skipped. See [CSV import detail](../finance-csv-import.md). Invalid dates, amounts, columns, or referenced IDs return 400; unavailable infrastructure returns a safe 5xx envelope. `/health` and `/ready` are runtime probes. Run `make db-test SERVICE=finance`, `make check`, or the gateway smoke scripts before extending it.
