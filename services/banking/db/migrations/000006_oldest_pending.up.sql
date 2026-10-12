-- The oldest booking date of a pending transaction in the connection's last
-- sync Finance accepted (NULL: none; BUG-39). Finance removes a pending
-- transaction the bank no longer sends only inside a sync's window, and a
-- later sync starts BANKING_OVERLAP_DAYS before the cursor; so the next
-- sync starts no later than this date (never further back than
-- BANKING_INITIAL_DAYS), and a hold the bank books or releases is removed
-- instead of staying in Finance's balance for good.
ALTER TABLE bank_connections ADD COLUMN oldest_pending_date DATE;
