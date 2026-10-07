# Finance and CSV import

Finance answers two different questions: an account balance is the cumulative transaction position; a monthly summary is activity within a selected month. Categories classify transactions, accounts hold balances, and transactions are the immutable financial events used for both views.

## Import workflow

1. Send a CSV file to `POST /api/finance/v1/imports/csv`.
2. The importer maps supported headers/formats into dated, categorized transactions.
3. Rows are validated before persistence. Invalid values, dates, or required columns return useful validation errors rather than partial silent success.
4. Duplicate protection makes a retry safe: an already imported row is skipped instead of producing another transaction.
5. The response reports created and skipped rows so a user can reconcile the result.

The dashboard Finance screen exposes the same operation. Detailed accepted-header and mapping behavior lives in [`docs/finance-csv-import.md`](https://github.com/GAMch1k/life-os/blob/master/docs/finance-csv-import.md) in the repository; keep that document and this page synchronized when importing changes.

Manual entry uses `POST /api/finance/v1/transactions`. Fetch accounts/categories first, then submit the typed transaction payload defined by Finance OpenAPI. Treat 4xx responses as data correction signals and 5xx responses as retryable operational failures after checking service logs.
